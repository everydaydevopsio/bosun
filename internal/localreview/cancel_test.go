package localreview

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	kt "k8s.io/client-go/testing"
)

func TestCancellationOnlyCleansAfterConfirmedShutdown(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "confirmed", true: "unconfirmed"}[fail], func(t *testing.T) {
			t.Setenv("BOSUN_STATE_DIR", t.TempDir())
			dir, e := os.MkdirTemp("/tmp/bosun-repos", "review.")
			if e != nil {
				t.Fatal(e)
			}
			defer os.RemoveAll(dir)
			write(t, filepath.Join(dir, "file"), "snapshot")
			c := fake.NewSimpleClientset(&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "job", Namespace: "bosun"}}, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "bosun", Labels: map[string]string{"job-name": "job"}}})
			c.PrependReactor("delete", "jobs", func(kt.Action) (bool, runtime.Object, error) {
				if fail {
					return true, nil, errors.New("API unreachable")
				}
				e := c.Tracker().Delete(schema.GroupVersionResource{Version: "v1", Resource: "pods"}, "bosun", "pod")
				return false, nil, e
			})
			r := record{Job: "job", Namespace: "bosun", Snapshot: dir}
			if code := cancelReview(c, &r, func(string, string) {}); code != 130 {
				t.Fatal(code)
			}
			_, e = os.Stat(dir)
			if fail && e != nil {
				t.Fatal("deleted live snapshot")
			}
			if !fail && !os.IsNotExist(e) {
				t.Fatal("snapshot retained after confirmed shutdown")
			}
		})
	}
}
