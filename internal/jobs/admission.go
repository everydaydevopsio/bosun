package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

// All controller replicas serialize the count/create transaction using the
// namespace's admission Lease. It is renewed while held and automatically
// becomes available after a crashed holder stops renewing it.
func withAdmission(ctx context.Context, client kubernetes.Interface, namespace string, submit func(context.Context) (string, error)) (string, error) {
	ctx, stopWaiting := context.WithTimeout(ctx, 35*time.Second)
	defer stopWaiting()
	electionCtx, stopElection := context.WithCancel(context.Background())
	acquired := make(chan context.Context, 1)
	done := make(chan struct{})
	elector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		Lock: &resourcelock.LeaseLock{
			LeaseMeta: metav1.ObjectMeta{Name: "bosun-review-admission", Namespace: namespace},
			Client:    client.CoordinationV1(), LockConfig: resourcelock.ResourceLockConfig{Identity: uuid.NewString()},
		},
		LeaseDuration: 30 * time.Second, RenewDeadline: 10 * time.Second, RetryPeriod: 200 * time.Millisecond,
		ReleaseOnCancel: true,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(leaderCtx context.Context) { acquired <- leaderCtx },
			OnStoppedLeading: func() {},
		},
	})
	if err != nil {
		stopElection()
		return "", err
	}
	go func() { defer close(done); elector.Run(electionCtx) }()
	// Release only after the guarded operation has returned, never while a
	// request cancellation still has the create operation unwinding.
	defer func() { stopElection(); <-done }()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-done:
		return "", fmt.Errorf("could not acquire review admission lease")
	case leaderCtx := <-acquired:
		opCtx, cancel := context.WithTimeout(leaderCtx, 5*time.Second)
		defer cancel()
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return submit(opCtx)
	}
}
