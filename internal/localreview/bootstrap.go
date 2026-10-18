package localreview

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/everydaydevopsio/bosun/internal/cluster"
	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/credentials"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// loadCredentials copies credentials discovered on this machine into the AI
// Secret the reviewer Job reads.
//
// Only the source of each credential is reported, never its value: the whole
// point of the flag is to move secrets without a human handling them, and a
// value echoed to a terminal ends up in scrollback and shell history.
func loadCredentials(ctx context.Context, client kubernetes.Interface, namespace, secret, provider string, found []credentials.Credential, notice func(string), probe func(context.Context, string) error) error {
	if err := credentials.Missing(provider, found); err != nil {
		return err
	}
	if err := probeSourceMismatch(provider, found); err != nil {
		return err
	}
	if err := probe(ctx, provider); err != nil {
		return err
	}
	notice("Verified " + provider + " with a headless provider request")
	written, err := cluster.EnsureSecret(ctx, client, namespace, secret, found)
	if err != nil {
		return fmt.Errorf("write the %s secret: %w", secret, err)
	}
	sources := make([]string, 0, len(found))
	for _, c := range found {
		sources = append(sources, fmt.Sprintf("%s from %s", c.Key, c.From))
	}
	notice(fmt.Sprintf("Loaded %d credential(s) into %s/%s: %s", len(written), namespace, secret, strings.Join(sources, ", ")))
	return nil
}

// A JSON credential exported only for Bosun is not automatically used by the
// host CLI. Refuse to call a different saved login a verification of it.
func probeSourceMismatch(provider string, found []credentials.Credential) error {
	have := map[string]bool{}
	for _, c := range found {
		have[c.Key] = true
	}
	for _, c := range found {
		if strings.HasPrefix(provider, "claude") && c.Key == "claude-credentials" && c.From == "CLAUDE_CREDENTIALS" && !have["claude-code-oauth-token"] && !have["anthropic-api-key"] {
			return fmt.Errorf("cannot live-check CLAUDE_CREDENTIALS with the host Claude CLI; use a CLI sign-in file or export CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY")
		}
		if strings.HasPrefix(provider, "codex") && c.Key == "codex-auth" && c.From == "CODEX_AUTH" && !have["openai-api-key"] {
			return fmt.Errorf("cannot live-check CODEX_AUTH with the host Codex CLI; use a CLI sign-in file or export OPENAI_API_KEY")
		}
	}
	return nil
}

// checkClusterCredentials reports the selected provider's stored credential
// status without returning or printing any Secret data. This is an offline
// check; --local-credentials also performs a live provider request.
func checkClusterCredentials(ctx context.Context, client kubernetes.Interface, namespace, secret, provider string) error {
	s, err := client.CoreV1().Secrets(namespace).Get(ctx, secret, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("provider credential Secret %s/%s is missing; run bosun up --local-credentials --provider %s", namespace, secret, provider)
	}
	if err != nil {
		return fmt.Errorf("check provider credential Secret %s/%s: %w", namespace, secret, err)
	}
	found := make([]credentials.Credential, 0, len(s.Data))
	for key, value := range s.Data {
		found = append(found, credentials.Credential{Key: key, Value: string(value)})
	}
	return credentials.Missing(provider, found)
}

// ensureCluster creates the Kind cluster when it is absent, so a user who has
// never run the repository's scripts still gets a working cluster.
func ensureCluster(ctx context.Context, name string, notice func(string)) error {
	if err := cluster.RequireTools(); err != nil {
		return err
	}
	k := cluster.New(name)
	exists, err := k.Exists(ctx)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	hostRepos, err := cluster.HostRepos()
	if err != nil {
		return err
	}
	notice(fmt.Sprintf("Creating Kind cluster %q with %s mounted at %s; this takes a minute on a cold machine", name, hostRepos, cluster.ContainerRepos))
	if err = k.Create(ctx); err != nil {
		return err
	}
	notice(fmt.Sprintf("Kind cluster %q is ready", name))
	return nil
}

// runUp prepares the cluster, namespace and credentials a review needs.
func runUp(ctx context.Context, o options, out, errout io.Writer) int {
	notice := func(m string) { fmt.Fprintln(errout, m) }
	if e := ensureCluster(ctx, o.cluster, notice); e != nil {
		fmt.Fprintln(errout, e)
		return 1
	}
	contextName := "kind-" + o.cluster
	c, e := client(contextName)
	if e != nil {
		fmt.Fprintln(errout, e)
		return 1
	}
	cfg := config.Load()
	nsCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	if e = cluster.EnsureNamespace(nsCtx, c, o.namespace); e != nil {
		cancel()
		fmt.Fprintln(errout, "create namespace:", e)
		return 1
	}
	cancel()
	if o.localCredentials {
		credCtx, credCancel := context.WithTimeout(ctx, 90*time.Second)
		e = loadCredentials(credCtx, c, o.namespace, cfg.AISecret, o.provider, credentials.Discover(credCtx), notice, probeLocalProvider)
		credCancel()
		if e != nil {
			fmt.Fprintln(errout, e)
			return 2
		}
	}
	checkCtx, checkCancel := context.WithTimeout(ctx, 15*time.Second)
	e = checkClusterCredentials(checkCtx, c, o.namespace, cfg.AISecret, o.provider)
	checkCancel()
	if e != nil {
		fmt.Fprintln(errout, e)
		return 2
	}
	fmt.Fprintf(out, "%s\n", contextName)
	notice(fmt.Sprintf("Cluster %q is ready for reviews in namespace %q", o.cluster, o.namespace))
	if !o.localCredentials {
		notice("Credential for " + o.provider + " is present in the cluster (offline check only); pass --local-credentials to verify a live request and refresh it")
	}
	return 0
}

// runDown removes the cluster and, unless asked otherwise, the snapshots that
// were mounted into it.
func runDown(ctx context.Context, o options, out, errout io.Writer) int {
	if e := cluster.RequireTools(); e != nil {
		fmt.Fprintln(errout, e)
		return 1
	}
	k := cluster.New(o.cluster)
	exists, e := k.Exists(ctx)
	if e != nil {
		fmt.Fprintln(errout, e)
		return 1
	}
	if exists {
		if e = k.Delete(ctx); e != nil {
			fmt.Fprintln(errout, e)
			return 1
		}
		fmt.Fprintf(out, "deleted cluster %s\n", o.cluster)
	} else {
		fmt.Fprintf(out, "cluster %s does not exist\n", o.cluster)
	}
	if o.keepSnapshots {
		return 0
	}
	if os.Getenv("BOSUN_REPOS_DIR") != "" {
		fmt.Fprintln(errout, "kept custom BOSUN_REPOS_DIR snapshots; remove them manually if desired")
		return 0
	}
	if e = cluster.RemoveSnapshots(); e != nil {
		fmt.Fprintln(errout, "remove snapshots:", e)
		return 1
	}
	hostRepos, e := cluster.HostRepos()
	if e != nil {
		fmt.Fprintln(errout, e)
		return 1
	}
	fmt.Fprintf(out, "removed %s\n", hostRepos)
	return 0
}
