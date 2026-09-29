package review

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	bridgev1 "github.com/everydaydevopsio/bosun/internal/bridgev1"
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
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("start bridgectl: %w", err)
		}
		go cmd.Wait()
		deadline := time.Now().Add(30 * time.Second)
		for target == "" && time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
			target = bridgeTarget()
		}
		if target == "" {
			return nil, fmt.Errorf("bridgectl server did not start within 30s")
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
	ctx, cancel := context.WithTimeout(ctx, max)
	defer cancel()
	conn, err := connectBridge(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	client := bridgev1.NewBridgeServiceClient(conn)
	return runSession(ctx, client, workspace, provider, prompt)
}

func runSession(ctx context.Context, client bridgev1.BridgeServiceClient, workspace, provider, prompt string) (string, error) {
	sessionID, clientID := uuid.NewString(), uuid.NewString()
	var opts map[string]string
	if provider == "codex-exec" {
		opts = map[string]string{"arg:prompt": prompt}
	}
	if _, err := client.StartSession(ctx, &bridgev1.StartSessionRequest{ProjectId: "bosun-review", SessionId: sessionID, RepoPath: workspace, Provider: provider, AgentOpts: opts, InitialCols: 200, InitialRows: 50}); err != nil {
		return "", fmt.Errorf("start bridge session: %w", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		client.StopSession(stopCtx, &bridgev1.StopSessionRequest{SessionId: sessionID, Force: true})
	}()
	stream, err := client.AttachSession(ctx, &bridgev1.AttachSessionRequest{SessionId: sessionID, ClientId: clientID, Role: bridgev1.AttachRole_ATTACH_ROLE_WRITER})
	if err != nil {
		return "", fmt.Errorf("attach bridge session: %w", err)
	}
	var out strings.Builder
	for {
		event, e := stream.Recv()
		if e != nil {
			return "", fmt.Errorf("review stream ended before successful session exit: %w", e)
		}
		switch event.Type {
		case bridgev1.AttachEventType_ATTACH_EVENT_TYPE_ATTACHED:
			if opts != nil {
				continue
			}
			if _, e = client.WriteInput(ctx, &bridgev1.WriteInputRequest{SessionId: sessionID, ClientId: clientID, Data: append([]byte(prompt), '\n')}); e != nil {
				return "", e
			}
		case bridgev1.AttachEventType_ATTACH_EVENT_TYPE_OUTPUT:
			out.Write(event.Payload)
		case bridgev1.AttachEventType_ATTACH_EVENT_TYPE_SESSION_EXIT:
			if event.ExitCode != 0 || event.Error != "" {
				return "", fmt.Errorf("agent session failed (exit %d): %s", event.ExitCode, event.Error)
			}
			if strings.TrimSpace(out.String()) == "" {
				return "", fmt.Errorf("agent session produced no output")
			}
			return CleanOutput(out.String()), nil
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
