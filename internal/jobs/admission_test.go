package jobs

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/review"
	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestConcurrentAdmissionAcrossReplicas(t *testing.T) {
	client := fake.NewSimpleClientset()
	// The fake client normally ignores resource versions; model the API's CAS
	// semantics so concurrent, independently created electors truly contend.
	client.PrependReactor("update", "leases", func(action clienttesting.Action) (bool, runtime.Object, error) {
		proposed := action.(clienttesting.UpdateAction).GetObject().(*coordinationv1.Lease).DeepCopy()
		current, err := client.Tracker().Get(coordinationv1.SchemeGroupVersion.WithResource("leases"), proposed.Namespace, proposed.Name)
		if err != nil {
			return true, nil, err
		}
		old := current.(*coordinationv1.Lease)
		if old.ResourceVersion != proposed.ResourceVersion {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "coordination.k8s.io", Resource: "leases"}, proposed.Name, fmt.Errorf("stale resource version"))
		}
		version, _ := strconv.Atoi(old.ResourceVersion)
		proposed.ResourceVersion = strconv.Itoa(version + 1)
		err = client.Tracker().Update(coordinationv1.SchemeGroupVersion.WithResource("leases"), proposed, proposed.Namespace)
		return true, proposed, err
	})
	cfg := config.Config{Namespace: "test", MaxConcurrentReviews: 3, ReviewTimeoutSeconds: 60}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var accepted atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := Submit(ctx, client, cfg, review.Request{Repo: "acme/repo", Ref: "feature"}, fmt.Sprint(i))
			if err == nil {
				accepted.Add(1)
			} else if _, ok := err.(CapacityError); !ok {
				t.Errorf("submit: %v", err)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if accepted.Load() != 3 {
		t.Fatalf("accepted %d instead of 3", accepted.Load())
	}
	n, err := Active(ctx, client, "test")
	if err != nil || n != 3 {
		t.Fatalf("active=%d err=%v", n, err)
	}
	// Redelivery must be recognized even while all slots are occupied.
	list, _ := client.BatchV1().Jobs("test").List(ctx, metav1.ListOptions{})
	var delivery string
	for i := 0; i < 12; i++ {
		if Name("acme/repo", "feature", fmt.Sprint(i)) == list.Items[0].Name {
			delivery = fmt.Sprint(i)
		}
	}
	_, err = Submit(ctx, client, cfg, review.Request{Repo: "acme/repo", Ref: "feature"}, delivery)
	if _, ok := err.(ExistsError); !ok {
		t.Fatalf("duplicate at capacity: %v", err)
	}
	completed := list.Items[0].DeepCopy()
	completed.Status.Succeeded = 1
	if _, err = client.BatchV1().Jobs("test").UpdateStatus(ctx, completed, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = Submit(ctx, client, cfg, review.Request{Repo: "acme/repo", Ref: "feature"}, "next"); err != nil {
		t.Fatal(err)
	}
}
func TestRepositoryLabelIsBoundedAndUnique(t *testing.T) {
	cfg := config.Config{ReviewTimeoutSeconds: 60}
	a := "Org/" + strings.Repeat("Long_Repository.", 20)
	b := a + "different"
	job, err := build(cfg, review.Request{Repo: a, Ref: "feature"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	label := job.Spec.Template.Labels["bosun/repository"]
	if errs := validation.IsValidLabelValue(label); len(errs) > 0 {
		t.Fatal(errs)
	}
	if label == repositoryLabel(b) {
		t.Fatal("truncated repositories collide")
	}
	if job.Annotations["bosun/repository"] != a || job.Spec.Template.Annotations["bosun/repository"] != a {
		t.Fatal("full repository name not preserved")
	}
}

// Reporting a termination talks to GitHub and can hold its lock for seconds. A
// webhook delivery waiting to learn whether it may start a review must not
// queue behind that, so the two use different Leases.
func TestSerializeUsesALeaseOfItsOwn(t *testing.T) {
	if SentinelLease == admissionLease {
		t.Fatal("the sentinel shares the admission lease; a GitHub call would block submissions")
	}
	client := fake.NewSimpleClientset()
	ran := 0
	if err := Serialize(context.Background(), client, "bosun", SentinelLease, 5*time.Second, func(context.Context) error {
		ran++
		return nil
	}); err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	if ran != 1 {
		t.Fatalf("want the operation run once, got %d", ran)
	}
	lease, err := client.CoordinationV1().Leases("bosun").Get(context.Background(), SentinelLease, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the sentinel lease was never taken: %v", err)
	}
	if lease.Name != SentinelLease {
		t.Fatalf("took the wrong lease: %s", lease.Name)
	}
}

// An operation that fails must surface, not be swallowed by the lease wrapper.
func TestSerializeReportsTheOperationError(t *testing.T) {
	client := fake.NewSimpleClientset()
	want := fmt.Errorf("GitHub POST returned HTTP 502")
	err := Serialize(context.Background(), client, "bosun", SentinelLease, 5*time.Second, func(context.Context) error { return want })
	if !strings.Contains(fmt.Sprint(err), "502") {
		t.Fatalf("the operation error did not reach the caller: %v", err)
	}
}

// The acquisition wait and the operation budget are different things. Binding
// the operation to the acquisition deadline means a caller that asked for 60
// seconds to publish gets whatever is left of a 35-second window -- five
// seconds, after waiting out another replica -- and a termination that needs
// a token mint and paginated reads never gets reported.
func TestTheOperationBudgetSurvivesTheAcquisitionWindow(t *testing.T) {
	original := leaseAcquireTimeout
	leaseAcquireTimeout = 50 * time.Millisecond
	defer func() { leaseAcquireTimeout = original }()

	client := fake.NewSimpleClientset()
	var deadline time.Time
	var cancelled error
	err := Serialize(context.Background(), client, "bosun", SentinelLease, 5*time.Second, func(ctx context.Context) error {
		deadline, _ = ctx.Deadline()
		// Outlive the acquisition window. Nothing about waiting for a lease
		// should shorten the work done once it is held.
		select {
		case <-time.After(200 * time.Millisecond):
		case <-ctx.Done():
			cancelled = ctx.Err()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	if cancelled != nil {
		t.Fatalf("the operation was cancelled by the acquisition window, not by its own budget: %v", cancelled)
	}
	if remaining := time.Until(deadline); remaining < 4*time.Second {
		t.Errorf("operation budget was %v, want close to the 5s requested", remaining)
	}
}

// Propagating caller cancellation is the half that must survive the fix: a
// drained webhook delivery or a shutting-down controller has to stop the work
// it is holding the lease for.
func TestTheOperationStopsWhenTheCallerGoesAway(t *testing.T) {
	client := fake.NewSimpleClientset()
	ctx, cancel := context.WithCancel(context.Background())
	var observed error
	err := Serialize(ctx, client, "bosun", SentinelLease, 30*time.Second, func(opCtx context.Context) error {
		cancel()
		select {
		case <-opCtx.Done():
			observed = opCtx.Err()
		case <-time.After(5 * time.Second):
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	if observed == nil {
		t.Error("cancelling the caller did not stop the operation")
	}
}
