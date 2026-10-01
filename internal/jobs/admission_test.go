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
	job := build(cfg, review.Request{Repo: a, Ref: "feature"}, "test")
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
