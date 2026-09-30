package review

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	bridgev1 "github.com/everydaydevopsio/bosun/internal/bridgev1"
	"github.com/everydaydevopsio/bosun/internal/progress"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func bridgeTarget() string {
	dir := os.Getenv("BRIDGECTL_STATE_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config", "bridgectl")
	}
	socket := filepath.Join(dir, "server.sock")
	if _, err := os.Stat(socket); err == nil {
		return "unix://" + socket
	}
	if b, err := os.ReadFile(filepath.Join(dir, "server.addr")); err == nil {
		v := strings.TrimSpace(string(b))
		if strings.HasPrefix(v, "/") {
			return "unix://" + v
		}
		return v
	}
	return ""
}
func connectBridge(ctx context.Context) (*grpc.ClientConn, error) {
	target := bridgeTarget()
	if target == "" {
		config := os.Getenv("BOSUN_BRIDGE_CONFIG")
		if config == "" {
			config = "/app/bosun/config/bridge-bosun.yaml"
		}
		cmd := exec.CommandContext(ctx, "bridgectl", "server", "start", "--config", config)
		cmd.Env = agentEnvironment()
		diagnostic := &diagnosticBuffer{}
		cmd.Stderr = diagnostic
		cmd.Stdout = diagnostic
		sanitize := diagnosticRedactor()
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("start bridgectl: %w", err)
		}
		exited := make(chan error, 1)
		go func() { exited <- cmd.Wait() }()
		deadline := time.Now().Add(30 * time.Second)
		for target == "" && time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case err := <-exited:
				if err != nil {
					return nil, fmt.Errorf("bridgectl startup exited: %w: %s", err, sanitize(diagnostic.String()))
				}
				exited = nil
			case <-time.After(200 * time.Millisecond):
			}
			target = bridgeTarget()
		}
		if target == "" {
			return nil, fmt.Errorf("bridgectl server did not start within 30s: %s", sanitize(diagnostic.String()))
		}
	}
	return grpc.DialContext(ctx, target, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		dialer := net.Dialer{}
		if strings.HasPrefix(target, "unix://") {
			return dialer.DialContext(ctx, "unix", strings.TrimPrefix(target, "unix://"))
		}
		return dialer.DialContext(ctx, "tcp", target)
	}))
}
func runBridge(ctx context.Context, workspace, provider, prompt string, max time.Duration) (string, error) {
	if provider == "codex-bosun" {
		if err := checkCodexCredentials(); err != nil {
			return "", err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, max)
	defer cancel()
	conn, err := connectBridge(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	client := bridgev1.NewBridgeServiceClient(conn)
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	health, err := client.Health(checkCtx, &bridgev1.HealthRequest{}, grpc.WaitForReady(true))
	if err != nil {
		return "", fmt.Errorf("bridgectl readiness: %w", err)
	}
	providers, err := client.ListProviders(checkCtx, &bridgev1.ListProvidersRequest{})
	if err != nil {
		return "", fmt.Errorf("list providers: %w", err)
	}
	found := false
	for _, p := range providers.Providers {
		if p.Provider == provider && p.Available {
			found = true
		}
	}
	if !found {
		return "", fmt.Errorf("provider %q is missing or unavailable in bridgectl", provider)
	}
	ctx = context.WithValue(ctx, bridgeInstanceKey{}, health.ServerInstanceId)
	return runSession(ctx, client, workspace, provider, prompt)
}

type bridgeInstanceKey struct{}

func runSession(ctx context.Context, client bridgev1.BridgeServiceClient, workspace, provider, prompt string) (string, error) {
	ctx, cancelSession := context.WithCancel(ctx)
	defer cancelSession()
	sessionID, clientID := uuid.NewString(), uuid.NewString()
	var opts map[string]string
	if argumentProvider(provider) {
		opts = map[string]string{"arg:prompt": prompt}
	}
	response, err := client.StartSession(ctx, &bridgev1.StartSessionRequest{ProjectId: "bosun-review", SessionId: sessionID, RepoPath: workspace, Provider: provider, AgentOpts: opts, InitialCols: 200, InitialRows: 50})
	if err != nil {
		return "", fmt.Errorf("start bridge session: %w", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := client.StopSession(stopCtx, &bridgev1.StopSessionRequest{SessionId: sessionID, Force: true}); err != nil {
			progress.From(ctx).Emit("warning", "Could not confirm agent session cleanup")
		}
	}()
	if response.SessionId != sessionID {
		return "", fmt.Errorf("bridgectl returned unexpected session identity")
	}
	if response.Status == bridgev1.SessionStatus_SESSION_STATUS_FAILED {
		return "", fmt.Errorf("bridgectl session failed during startup")
	}
	stream, err := client.AttachSession(ctx, &bridgev1.AttachSessionRequest{SessionId: sessionID, ClientId: clientID, Role: bridgev1.AttachRole_ATTACH_ROLE_WRITER})
	if err != nil {
		return "", fmt.Errorf("attach bridge session: %w", err)
	}
	var out strings.Builder
	reporter := progress.From(ctx)
	reporter.Emit("stage", "reviewing")
	type received struct {
		event *bridgev1.AttachSessionEvent
		err   error
	}
	incoming := make(chan received)
	receive := func(s bridgev1.BridgeService_AttachSessionClient) {
		go func() {
			for {
				e, err := s.Recv()
				select {
				case incoming <- received{e, err}:
				case <-ctx.Done():
					return
				}
				if err != nil {
					return
				}
			}
		}()
	}
	receive(stream)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	var seq uint64
	sent := opts != nil
	retries := 0
	for {
		var event *bridgev1.AttachSessionEvent
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
			checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			state, err := client.GetSession(checkCtx, &bridgev1.GetSessionRequest{SessionId: sessionID})
			cancel()
			if err != nil {
				reporter.Emit("warning", "Session status unavailable")
				continue
			}
			if state.Status == bridgev1.SessionStatus_SESSION_STATUS_FAILED || (state.ExitRecorded && (state.ExitCode != 0 || state.Error != "")) {
				return "", fmt.Errorf("agent session failed: %s", state.Error)
			}
			reporter.Emit("session", fmt.Sprintf("Session %s; last sequence %d", state.Status, state.LastSeq))
			continue
		case r := <-incoming:
			if r.err != nil {
				if ctx.Err() != nil {
					return "", ctx.Err()
				}
				if retries >= 3 {
					return "", fmt.Errorf("review stream ended before successful session exit: %w", r.err)
				}
				instance, _ := ctx.Value(bridgeInstanceKey{}).(string)
				if instance == "" {
					return "", fmt.Errorf("review stream ended before successful session exit: %w", r.err)
				}
				retries++
				checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
				health, e := client.Health(checkCtx, &bridgev1.HealthRequest{})
				cancel()
				if e != nil || health.ServerInstanceId != instance {
					return "", fmt.Errorf("bridgectl unavailable or restarted; original review cannot be recovered")
				}
				reporter.Emit("warning", "Reconnecting to original review session")
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-time.After(time.Duration(retries) * time.Second):
				}
				next, e := client.AttachSession(ctx, &bridgev1.AttachSessionRequest{SessionId: sessionID, ClientId: clientID, Role: bridgev1.AttachRole_ATTACH_ROLE_WRITER, AfterSeq: seq})
				if e != nil {
					return "", fmt.Errorf("reattach session: %w", e)
				}
				receive(next)
				continue
			}
			event = r.event
		}
		if event.Type == bridgev1.AttachEventType_ATTACH_EVENT_TYPE_REPLAY_GAP {
			return "", fmt.Errorf("review output incomplete: bridgectl replay gap")
		}
		if event.Seq > 0 {
			if event.Seq <= seq {
				continue
			}
			seq = event.Seq
		}

		switch event.Type {
		case bridgev1.AttachEventType_ATTACH_EVENT_TYPE_ATTACHED:
			if sent {
				continue
			}
			data := append([]byte(prompt), '\n')
			response, e := client.WriteInput(ctx, &bridgev1.WriteInputRequest{SessionId: sessionID, ClientId: clientID, Data: data})
			if e != nil {
				return "", fmt.Errorf("deliver prompt (not retried): %w", e)
			}
			if !response.Accepted || response.BytesWritten != uint32(len(data)) {
				return "", fmt.Errorf("provider rejected or partially accepted review prompt")
			}
			sent = true
		case bridgev1.AttachEventType_ATTACH_EVENT_TYPE_OUTPUT:
			reporter.Emit("activity", "Provider output received")
			out.Write(event.Payload)
			if argumentProvider(provider) && !strings.HasSuffix(string(event.Payload), "\n") {
				out.WriteByte('\n')
			}
		case bridgev1.AttachEventType_ATTACH_EVENT_TYPE_SESSION_EXIT:
			if event.ExitCode != 0 || event.Error != "" {
				return "", fmt.Errorf("agent session failed (exit %d): %s", event.ExitCode, event.Error)
			}
			if strings.TrimSpace(out.String()) == "" {
				return "", fmt.Errorf("agent session produced no output")
			}
			return CleanOutput(out.String()), nil
		case bridgev1.AttachEventType_ATTACH_EVENT_TYPE_THINKING:
			reporter.Emit("activity", "Provider active")
		case bridgev1.AttachEventType_ATTACH_EVENT_TYPE_ERROR:
			return "", fmt.Errorf("agent session error: %s", event.Error)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
	}
}
func CleanOutput(raw string) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n"))
}

// Retain only bounded startup diagnostics; credentials are redacted before use.
type diagnosticBuffer struct {
	mu   sync.Mutex
	data []byte
}

func (b *diagnosticBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	b.data = append(b.data, p...)
	if len(b.data) > 8192 {
		b.data = append([]byte(nil), b.data[len(b.data)-8192:]...)
	}
	return n, nil
}
func (b *diagnosticBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.data))
}

// Bosun-owned headless providers receive one positional prompt and closed stdin.
func argumentProvider(provider string) bool {
	return provider == "codex-bosun" || provider == "claude-bosun"
}
