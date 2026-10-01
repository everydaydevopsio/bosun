package cluster

import (
	"context"
	"sort"

	"github.com/everydaydevopsio/bosun/internal/credentials"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// managed marks the objects the CLI creates, so an operator can tell them apart
// from anything the Helm chart installed.
var managed = map[string]string{"app.kubernetes.io/managed-by": "bosun"}

// EnsureNamespace creates the review namespace when it is absent. Bootstrap
// runs on every review, so an existing namespace is success, not a conflict.
func EnsureNamespace(ctx context.Context, client kubernetes.Interface, name string) error {
	_, err := client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: managed}}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// EnsureSecret merges discovered credentials into the AI Secret and reports the
// keys it wrote.
//
// Keys that were already there and were not rediscovered are left alone: a user
// may have loaded a provider credential by another route, and a review for that
// provider must keep working.
func EnsureSecret(ctx context.Context, client kubernetes.Interface, namespace, name string, found []credentials.Credential) ([]string, error) {
	if len(found) == 0 {
		return nil, nil
	}
	written := make([]string, 0, len(found))
	for _, c := range found {
		written = append(written, c.Key)
	}
	sort.Strings(written)

	secrets := client.CoreV1().Secrets(namespace)
	existing, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		data := map[string][]byte{}
		for _, c := range found {
			data[c.Key] = []byte(c.Value)
		}
		_, err = secrets.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: managed}, Type: corev1.SecretTypeOpaque, Data: data}, metav1.CreateOptions{})
		return written, err
	}
	if err != nil {
		return nil, err
	}
	updated := existing.DeepCopy()
	if updated.Data == nil {
		updated.Data = map[string][]byte{}
	}
	for _, c := range found {
		updated.Data[c.Key] = []byte(c.Value)
	}
	_, err = secrets.Update(ctx, updated, metav1.UpdateOptions{})
	return written, err
}
