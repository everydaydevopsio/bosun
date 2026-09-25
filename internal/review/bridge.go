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
		if err := exec.Command("bridgectl", "server", "start").Start(); err != nil {
			return nil, fmt.Errorf("start bridgectl: %w", err)
		}
		deadline := time.Now().Add(30 * time.Second)
		for target == "" && time.Now().Before(deadline) {
			time.Sleep(200 * time.Millisecond)
			target = bridgeTarget()
		}
		if target == "" {
			return nil, fmt.Errorf("bridgectl server did not start within 30s")
		}
	}
	return grpc.DialContext(ctx, target, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return net.Dial("unix", strings.TrimPrefix(target, "unix://"))
	}))
}
func runBridge(ctx context.Context, workspace, provider, prompt string, max time.Duration) (string, error) {
	conn, err := connectBridge(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	client := bridgev1.NewBridgeServiceClient(conn)
	sessionID, clientID := uuid.NewString(), uuid.NewString()
	if _, err = client.StartSession(ctx, &bridgev1.StartSessionRequest{ProjectId: "bosun-review", SessionId: sessionID, RepoPath: workspace, Provider: provider, InitialCols: 200, InitialRows: 50}); err != nil {
		return "", fmt.Errorf("start bridge session: %w", err)
	}
	defer client.StopSession(context.Background(), &bridgev1.StopSessionRequest{SessionId: sessionID})
	stream, err := client.AttachSession(ctx, &bridgev1.AttachSessionRequest{SessionId: sessionID, ClientId: clientID, Role: bridgev1.AttachRole_ATTACH_ROLE_WRITER})
	if err != nil {
		return "", fmt.Errorf("attach bridge session: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, max)
	defer cancel()
	var out strings.Builder
	for {
		event, e := stream.Recv()
		if e != nil {
			if out.Len() > 0 {
				return CleanOutput(out.String()), nil
			}
			return "", e
		}
		switch event.Type {
		case bridgev1.AttachEventType_ATTACH_EVENT_TYPE_ATTACHED:
			if _, e = client.WriteInput(ctx, &bridgev1.WriteInputRequest{SessionId: sessionID, ClientId: clientID, Data: append([]byte(prompt), '\n')}); e != nil {
				return "", e
			}
		case bridgev1.AttachEventType_ATTACH_EVENT_TYPE_OUTPUT:
			out.Write(event.Payload)
		case bridgev1.AttachEventType_ATTACH_EVENT_TYPE_SESSION_EXIT:
			if out.Len() == 0 {
				return "", fmt.Errorf("agent session produced no output")
			}
			return CleanOutput(out.String()), nil
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("review exceeded %s", max)
		default:
		}
	}
}
func CleanOutput(raw string) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n"))
}
