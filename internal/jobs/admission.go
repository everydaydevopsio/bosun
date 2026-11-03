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

// admissionLease serializes the count/create transaction that decides whether
// one more review fits.
const admissionLease = "bosun-review-admission"

// SentinelLease serializes reporting a reviewer termination. It is deliberately
// a different lease from admission: reporting talks to GitHub and can hold the
// lock for seconds, and a webhook delivery waiting to find out whether it may
// start a review must not queue behind that.
const SentinelLease = "bosun-termination-sentinel"

// leaseAcquireTimeout bounds how long a caller waits for the lease. It bounds
// only the wait: the operation gets its own budget once the lease is held, so
// waiting out another replica cannot leave the work with seconds to finish in.
// A variable so tests can shorten it.
var leaseAcquireTimeout = 35 * time.Second

// AdmissionBudget is the longest one submission can take: the wait for the
// lease plus the transaction it guards. The webhook server's write timeout has
// to exceed it, or a delivery that queued behind another replica is cut off
// while the handler is still legitimately working.
const AdmissionBudget = 35*time.Second + admissionOperationTimeout

const admissionOperationTimeout = 5 * time.Second

// All controller replicas serialize the count/create transaction using the
// namespace's admission Lease. It is renewed while held and automatically
// becomes available after a crashed holder stops renewing it.
func withAdmission(ctx context.Context, client kubernetes.Interface, namespace string, submit func(context.Context) (string, error)) (string, error) {
	return underLease(ctx, client, namespace, admissionLease, admissionOperationTimeout, submit)
}

// Serialize runs op while holding one of the namespace's named Leases, so that
// two controller replicas cannot run it at the same time.
//
// operationTimeout bounds op itself once the lease is held. Callers doing
// network work need more than a local API transaction does, and a caller that
// overruns must lose the lease rather than hold it indefinitely.
func Serialize(ctx context.Context, client kubernetes.Interface, namespace, lease string, operationTimeout time.Duration, op func(context.Context) error) error {
	_, err := underLease(ctx, client, namespace, lease, operationTimeout, func(ctx context.Context) (string, error) {
		return "", op(ctx)
	})
	return err
}

func underLease(ctx context.Context, client kubernetes.Interface, namespace, lease string, operationTimeout time.Duration, op func(context.Context) (string, error)) (string, error) {
	waitCtx, stopWaiting := context.WithTimeout(ctx, leaseAcquireTimeout)
	defer stopWaiting()
	electionCtx, stopElection := context.WithCancel(context.Background())
	acquired := make(chan context.Context, 1)
	done := make(chan struct{})
	elector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		Lock: &resourcelock.LeaseLock{
			LeaseMeta: metav1.ObjectMeta{Name: lease, Namespace: namespace},
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
	case <-waitCtx.Done():
		return "", waitCtx.Err()
	case <-done:
		return "", fmt.Errorf("could not acquire the %s lease", lease)
	case leaderCtx := <-acquired:
		opCtx, cancel := context.WithTimeout(leaderCtx, operationTimeout)
		defer cancel()
		// The caller's own context, not the acquisition window: a drained
		// delivery or a shutting-down controller must still stop the work,
		// but time spent waiting for the lease must not come out of the
		// budget for doing it.
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return op(opCtx)
	}
}
