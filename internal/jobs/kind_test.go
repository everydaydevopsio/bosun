package jobs

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/review"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	typedbatch "k8s.io/client-go/kubernetes/typed/batch/v1"
	"k8s.io/client-go/tools/clientcmd"
)

// Keep real API admission tests inert: suspended Jobs never start a provider.
type suspendedClient struct{ kubernetes.Interface }

func (c suspendedClient) BatchV1() typedbatch.BatchV1Interface {
	return suspendedBatch{c.Interface.BatchV1()}
}

type suspendedBatch struct{ typedbatch.BatchV1Interface }

func (b suspendedBatch) Jobs(namespace string) typedbatch.JobInterface {
	return suspendedJobs{b.BatchV1Interface.Jobs(namespace)}
}

type suspendedJobs struct{ typedbatch.JobInterface }

func (j suspendedJobs) Create(ctx context.Context, job *batchv1.Job, opts metav1.CreateOptions) (*batchv1.Job, error) {
	job = job.DeepCopy()
	job.Spec.Suspend = ptr(true)
	return j.JobInterface.Create(ctx, job, opts)
}
func TestKindAdmissionAcrossReplicas(t *testing.T) {
	if os.Getenv("BOSUN_KIND_TEST") != "1" {
		t.Skip("set BOSUN_KIND_TEST=1 to test against kind-bosun")
	}
	contextName := "kind-bosun"
	if v := os.Getenv("BOSUN_KIND_CLUSTER"); v != "" {
		contextName = "kind-" + v
	}
	rest, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{CurrentContext: contextName}).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	client, err := kubernetes.NewForConfig(rest)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	ns, err := client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "bosun-admission-test-"}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := client.CoreV1().Namespaces().Delete(cleanup, ns.Name, metav1.DeleteOptions{}); err != nil {
			t.Error(err)
		}
	}()
	cfg := config.Config{Namespace: ns.Name, MaxConcurrentReviews: 3, ReviewTimeoutSeconds: 60, ReviewImage: "bosun:dev", RunAsUser: 1001, GitHubTokenSecret: "bosun-github", AISecret: "bosun-ai"}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			// Independent REST clients/electors model independent controller replicas.
			replica, err := kubernetes.NewForConfig(rest)
			if err != nil {
				t.Error(err)
				return
			}
			_, err = Submit(ctx, suspendedClient{replica}, cfg, review.Request{Repo: "acme/widget", Ref: "feature"}, fmt.Sprint(i))
			if err == nil {
				accepted.Add(1)
			} else if _, ok := err.(CapacityError); !ok {
				t.Error(err)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if accepted.Load() != 3 {
		t.Fatalf("accepted %d, want 3", accepted.Load())
	}
	n, err := Active(ctx, client, ns.Name)
	if err != nil || n != 3 {
		t.Fatalf("active %d, error %v", n, err)
	}
}
