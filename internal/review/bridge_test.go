package review

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	pb "github.com/everydaydevopsio/bosun/internal/bridgev1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeBridge struct {
	pb.BridgeServiceClient
	events   []*pb.AttachSessionEvent
	request  *pb.StartSessionRequest
	writes   int
	stopped  bool
	silent   bool
	startErr error
}

func TestFailedProviderExitExplainsRevokedLocalClaudeCredentials(t *testing.T) {
	t.Setenv("BOSUN_LOCAL_PATH", "/repo")
	f := &fakeBridge{events: []*pb.AttachSessionEvent{
		{Type: pb.AttachEventType_ATTACH_EVENT_TYPE_OUTPUT, Payload: []byte(`Failed to authenticate. API Error: 401 {"error":{"message":"OAuth access token has been revoked."},"request_id":"private-request"}`)},
		{Type: pb.AttachEventType_ATTACH_EVENT_TYPE_SESSION_EXIT, ExitCode: 1},
	}}
	_, err := runSession(context.Background(), f, "/repo", "claude-bosun", "review")
	if err == nil {
		t.Fatal("expected provider failure")
	}
	for _, want := range []string{"authentication", "revoked", "claude auth login", "bosun up --local-credentials", "review-status"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q from %q", want, err)
		}
	}
	if strings.Contains(err.Error(), "private-request") || strings.Contains(err.Error(), `{"error"`) {
		t.Fatalf("provider output leaked into failure: %q", err)
	}
}

func TestUnknownProviderFailureDoesNotEchoOutput(t *testing.T) {
	f := &fakeBridge{events: []*pb.AttachSessionEvent{
		{Type: pb.AttachEventType_ATTACH_EVENT_TYPE_OUTPUT, Payload: []byte("secret-value-123")},
		{Type: pb.AttachEventType_ATTACH_EVENT_TYPE_SESSION_EXIT, ExitCode: 1, Error: "private-secret-456"},
	}}
	_, err := runSession(context.Background(), f, "/repo", "claude-bosun", "review")
	if err == nil || !strings.Contains(err.Error(), "exit 1") {
		t.Fatalf("expected diagnosed exit, got %v", err)
	}
	if strings.Contains(err.Error(), "secret-value") || strings.Contains(err.Error(), "private-secret") {
		t.Fatalf("provider output leaked into failure: %q", err)
	}
}

func TestProviderFailureDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, provider, errorText, want, absent string
		local                                   bool
	}{
		{"expired local Claude OAuth", "claude-bosun", "invalid_grant", "claude auth login", "invalid_grant", true},
		{"rejected local Codex credential", "codex-bosun", "API Error: 401", "codex login", "API Error: 401", true},
		{"hosted authentication", "claude-bosun", "unauthorized", "cluster Secret", "claude auth login", false},
		{"provider rate limit", "claude-bosun", "API Error: 429", "RATE_LIMITED", "API Error: 429", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.local {
				t.Setenv("BOSUN_LOCAL_PATH", "/repo")
			} else {
				t.Setenv("BOSUN_LOCAL_PATH", "")
			}
			got := providerExitError(tc.provider, 1, tc.errorText, "").Error()
			if !strings.Contains(got, tc.want) || strings.Contains(got, tc.absent) {
				t.Errorf("incorrect diagnostic: %q", got)
			}
		})
	}
}

func (f *fakeBridge) StartSession(_ context.Context, r *pb.StartSessionRequest, _ ...grpc.CallOption) (*pb.StartSessionResponse, error) {
	f.request = r
	if f.startErr != nil {
		return nil, f.startErr
	}
	return &pb.StartSessionResponse{SessionId: r.SessionId, Status: pb.SessionStatus_SESSION_STATUS_RUNNING}, nil
}

func TestSnapshotPathPolicyFailureIsSetupError(t *testing.T) {
	t.Setenv("BOSUN_WORKER_IMAGE", "bosun:test")
	denied := status.Error(codes.InvalidArgument, `start session: invalid argument: repo_path "/repos/review.123" is not under any allowed path`)
	f := &fakeBridge{startErr: denied}
	_, err := runSession(context.Background(), f, "/repos/review.123", "claude-bosun", "review")
	if err == nil || !isSnapshotPathPolicyError(err) {
		t.Fatalf("want snapshot path configuration failure, got %v", err)
	}
	for _, want := range []string{"bosun:test", "config/bridge-bosun.yaml", "/repos", "nonretryable"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q from %q", want, err)
		}
	}
	if !errors.Is(err, denied) {
		t.Fatal("original bridgectl error was lost")
	}
	if got := agentFailureStage(err); got != StageSetup {
		t.Fatalf("path policy stage = %s, want setup", got)
	}
}

func TestUnrelatedStartSessionFailureStaysProviderError(t *testing.T) {
	for _, tc := range []struct {
		workspace string
		err       error
	}{
		{"/repos/review.123", status.Error(codes.InvalidArgument, "invalid provider")},
		{"/tmp/other", status.Error(codes.InvalidArgument, `repo_path "/tmp/other" is not under any allowed path`)},
		{"/repos/review.123", status.Error(codes.PermissionDenied, `repo_path "/repos/review.123" is not under any allowed path`)},
	} {
		f := &fakeBridge{startErr: tc.err}
		_, err := runSession(context.Background(), f, tc.workspace, "claude-bosun", "review")
		if agentFailureStage(err) != StageProvider {
			t.Fatalf("unrelated error classified as setup: %v", err)
		}
	}
}

func TestHostedWorkspacePathPolicyFailureIsSetupError(t *testing.T) {
	workspace := HostedWorkspaceRoot + "/ws-123"
	denied := status.Error(codes.InvalidArgument, `repo_path "`+workspace+`" is not under any allowed path`)
	f := &fakeBridge{startErr: denied}
	_, err := runSession(context.Background(), f, workspace, "claude-bosun", "review")
	if !isSnapshotPathPolicyError(err) || agentFailureStage(err) != StageSetup {
		t.Fatalf("hosted workspace denial must be a setup error: %v", err)
	}
}
func (f *fakeBridge) StopSession(_ context.Context, _ *pb.StopSessionRequest, _ ...grpc.CallOption) (*pb.StopSessionResponse, error) {
	f.stopped = true
	return &pb.StopSessionResponse{}, nil
}
func (f *fakeBridge) WriteInput(_ context.Context, _ *pb.WriteInputRequest, _ ...grpc.CallOption) (*pb.WriteInputResponse, error) {
	f.writes++
	return &pb.WriteInputResponse{}, nil
}
func (f *fakeBridge) AttachSession(ctx context.Context, _ *pb.AttachSessionRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[pb.AttachSessionEvent], error) {
	return &fakeStream{ctx: ctx, events: f.events, silent: f.silent}, nil
}

type fakeStream struct {
	grpc.ClientStream
	ctx    context.Context
	events []*pb.AttachSessionEvent
	silent bool
}

func (s *fakeStream) Recv() (*pb.AttachSessionEvent, error) {
	if len(s.events) == 0 {
		if s.silent {
			<-s.ctx.Done()
			return nil, s.ctx.Err()
		}
		return nil, io.EOF
	}
	e := s.events[0]
	s.events = s.events[1:]
	return e, nil
}
func TestReviewRequiresSuccessfulExit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ending  *pb.AttachSessionEvent
		success bool
	}{
		{"success", &pb.AttachSessionEvent{Type: pb.AttachEventType_ATTACH_EVENT_TYPE_SESSION_EXIT}, true},
		{"failed exit", &pb.AttachSessionEvent{Type: pb.AttachEventType_ATTACH_EVENT_TYPE_SESSION_EXIT, ExitCode: 1}, false},
		{"error", &pb.AttachSessionEvent{Type: pb.AttachEventType_ATTACH_EVENT_TYPE_ERROR, Error: "failed"}, false},
		{"truncated stream", nil, false},
	} {
		for _, provider := range []string{"codex-bosun", "claude-bosun"} {
			t.Run(provider+"/"+tc.name, func(t *testing.T) {
				f := &fakeBridge{events: []*pb.AttachSessionEvent{
					{Type: pb.AttachEventType_ATTACH_EVENT_TYPE_ATTACHED},
					{Type: pb.AttachEventType_ATTACH_EVENT_TYPE_OUTPUT, Payload: []byte("Review findings")},
				}}
				if tc.ending != nil {
					f.events = append(f.events, tc.ending)
				}
				out, err := runSession(context.Background(), f, "/repo", provider, "review branch")
				if (err == nil) != tc.success {
					t.Fatalf("output %q, error %v", out, err)
				}
				if f.request.AgentOpts["arg:prompt"] != "review branch" || f.writes != 0 {
					t.Fatal("prompt must be delivered once via session arguments")
				}
				if !f.stopped {
					t.Fatal("session not stopped")
				}
			})
		}
	}
}
func TestSilentReviewHonorsDeadline(t *testing.T) {
	f := &fakeBridge{silent: true}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := runSession(ctx, f, "/repo", "codex-bosun", "review")
	if err == nil || !f.stopped {
		t.Fatalf("expected cancelled session, got %v", err)
	}
}

func TestReplayGapRejectsPartialReview(t *testing.T) {
	f := &fakeBridge{events: []*pb.AttachSessionEvent{{Type: pb.AttachEventType_ATTACH_EVENT_TYPE_OUTPUT, Payload: []byte("partial")}, {Type: pb.AttachEventType_ATTACH_EVENT_TYPE_REPLAY_GAP}, {Type: pb.AttachEventType_ATTACH_EVENT_TYPE_SESSION_EXIT}}}
	out, err := runSession(context.Background(), f, "/repo", "codex-bosun", "review")
	if err == nil || out != "" {
		t.Fatalf("accepted incomplete output: %q %v", out, err)
	}
}
func TestRejectedPromptFails(t *testing.T) {
	f := &fakeBridge{events: []*pb.AttachSessionEvent{{Type: pb.AttachEventType_ATTACH_EVENT_TYPE_ATTACHED}}}
	_, err := runSession(context.Background(), f, "/repo", "claude", "review")
	if err == nil || f.writes != 1 {
		t.Fatalf("rejected input: %v writes %d", err, f.writes)
	}
}
func TestReplayedOutputIsDeduplicated(t *testing.T) {
	f := &fakeBridge{events: []*pb.AttachSessionEvent{{Type: pb.AttachEventType_ATTACH_EVENT_TYPE_OUTPUT, Seq: 1, Payload: []byte("findings")}, {Type: pb.AttachEventType_ATTACH_EVENT_TYPE_OUTPUT, Seq: 1, Payload: []byte("findings"), Replay: true}, {Type: pb.AttachEventType_ATTACH_EVENT_TYPE_SESSION_EXIT, Seq: 2}}}
	out, err := runSession(context.Background(), f, "/repo", "codex-bosun", "review")
	if err != nil || out != "findings" {
		t.Fatalf("%q %v", out, err)
	}
}

type recoveringBridge struct {
	fakeBridge
	attaches int
	after    uint64
	changed  bool
}

func (f *recoveringBridge) Health(context.Context, *pb.HealthRequest, ...grpc.CallOption) (*pb.HealthResponse, error) {
	id := "instance"
	if f.changed {
		id = "restarted"
	}
	return &pb.HealthResponse{ServerInstanceId: id}, nil
}
func (f *recoveringBridge) WriteInput(_ context.Context, r *pb.WriteInputRequest, _ ...grpc.CallOption) (*pb.WriteInputResponse, error) {
	f.writes++
	return &pb.WriteInputResponse{Accepted: true, BytesWritten: uint32(len(r.Data))}, nil
}
func (f *recoveringBridge) AttachSession(ctx context.Context, r *pb.AttachSessionRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[pb.AttachSessionEvent], error) {
	f.attaches++
	f.after = r.AfterSeq
	events := []*pb.AttachSessionEvent{{Type: pb.AttachEventType_ATTACH_EVENT_TYPE_ATTACHED}, {Type: pb.AttachEventType_ATTACH_EVENT_TYPE_OUTPUT, Seq: 1, Payload: []byte("findings")}}
	if f.attaches > 1 {
		events = append(events, &pb.AttachSessionEvent{Type: pb.AttachEventType_ATTACH_EVENT_TYPE_SESSION_EXIT, Seq: 2})
	}
	return &fakeStream{ctx: ctx, events: events}, nil
}
func TestReconnectDoesNotResendPrompt(t *testing.T) {
	f := &recoveringBridge{}
	ctx := context.WithValue(context.Background(), bridgeInstanceKey{}, "instance")
	out, err := runSession(ctx, f, "/repo", "claude", "review")
	if err != nil || out != "findings" || f.writes != 1 || f.attaches != 2 || f.after != 1 {
		t.Fatalf("out %q err %v writes %d attaches %d after %d", out, err, f.writes, f.attaches, f.after)
	}
}
func TestDaemonRestartDoesNotRerunReview(t *testing.T) {
	f := &recoveringBridge{changed: true}
	ctx := context.WithValue(context.Background(), bridgeInstanceKey{}, "instance")
	_, err := runSession(ctx, f, "/repo", "claude", "review")
	if err == nil || f.attaches != 1 || f.writes != 1 {
		t.Fatalf("err %v attaches %d writes %d", err, f.attaches, f.writes)
	}
}
