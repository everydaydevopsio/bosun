package review

import (
	"context"
	"io"
	"testing"
	"time"

	pb "github.com/everydaydevopsio/bosun/internal/bridgev1"
	"google.golang.org/grpc"
)

type fakeBridge struct {
	pb.BridgeServiceClient
	events  []*pb.AttachSessionEvent
	request *pb.StartSessionRequest
	writes  int
	stopped bool
	silent  bool
}

func (f *fakeBridge) StartSession(_ context.Context, r *pb.StartSessionRequest, _ ...grpc.CallOption) (*pb.StartSessionResponse, error) {
	f.request = r
	return &pb.StartSessionResponse{}, nil
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
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeBridge{events: []*pb.AttachSessionEvent{
				{Type: pb.AttachEventType_ATTACH_EVENT_TYPE_ATTACHED},
				{Type: pb.AttachEventType_ATTACH_EVENT_TYPE_OUTPUT, Payload: []byte("Review findings")},
			}}
			if tc.ending != nil {
				f.events = append(f.events, tc.ending)
			}
			out, err := runSession(context.Background(), f, "/repo", "codex-exec", "review branch")
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
func TestSilentReviewHonorsDeadline(t *testing.T) {
	f := &fakeBridge{silent: true}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := runSession(ctx, f, "/repo", "codex-exec", "review")
	if err == nil || !f.stopped {
		t.Fatalf("expected cancelled session, got %v", err)
	}
}
