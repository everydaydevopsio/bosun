package localreview

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/everydaydevopsio/bosun/internal/cluster"
	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/credentials"
	"github.com/everydaydevopsio/bosun/internal/version"
	batchv1 "k8s.io/api/batch/v1"
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
	// A headless request can refresh a file or macOS Keychain token. Discovery
	// captured its old bytes before the probe, so reload the selected provider.
	found, err := credentials.Refresh(ctx, provider, found)
	if err != nil {
		return err
	}
	if err := credentials.Missing(provider, found); err != nil {
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
		return fmt.Errorf("provider credential Secret %s/%s is missing; run bosun up --local-credentials --provider %s: %w", namespace, secret, provider, err)
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
	if e := checkSnapshotMount(ctx, o.cluster); e != nil {
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

type clusterLifecycle interface {
	Exists(context.Context) (bool, error)
	Delete(context.Context) error
}

// resetCluster keeps the deletion and recreation sequence testable without a
// Docker daemon. A failed delete must never be followed by an attempted create.
func resetCluster(ctx context.Context, name string, k clusterLifecycle, purge, force bool, active func(context.Context) ([]string, error), removeSnapshots func() error, up func() int, out io.Writer) (int, error) {
	exists, err := k.Exists(ctx)
	if err != nil {
		return 1, err
	}
	if exists {
		jobs, checkErr := active(ctx)
		if checkErr != nil && !force {
			return 2, fmt.Errorf("cannot check active reviews before deleting cluster %s: %w; retry with --force only if interrupting reviews is acceptable", name, checkErr)
		}
		if len(jobs) > 0 && !force {
			return 2, fmt.Errorf("cluster %s has active review Jobs (%s); wait for them or pass --force to interrupt them", name, strings.Join(jobs, ", "))
		}
		if checkErr != nil {
			fmt.Fprintf(out, "warning: could not check active review Jobs: %v\n", checkErr)
		}
		if len(jobs) > 0 {
			fmt.Fprintf(out, "warning: interrupting active review Jobs: %s\n", strings.Join(jobs, ", "))
		}
		if err := k.Delete(ctx); err != nil {
			return 1, err
		}
		fmt.Fprintf(out, "deleted cluster %s\n", name)
	}
	if purge {
		if err := removeSnapshots(); err != nil {
			return 1, fmt.Errorf("purge snapshots: %w", err)
		}
	}
	return up(), nil
}

func unfinishedReviewJobs(jobs []batchv1.Job) []string {
	var names []string
	for _, job := range jobs {
		finished := false
		for _, condition := range job.Status.Conditions {
			if (condition.Type == batchv1.JobComplete || condition.Type == batchv1.JobFailed) && condition.Status == "True" {
				finished = true
			}
		}
		if !finished && job.DeletionTimestamp == nil {
			names = append(names, job.Namespace+"/"+job.Name)
		}
	}
	sort.Strings(names)
	return names
}

func activeReviewJobs(ctx context.Context, o options) ([]string, error) {
	c, err := client("kind-" + o.cluster)
	if err != nil {
		return nil, err
	}
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	jobs, err := c.BatchV1().Jobs(metav1.NamespaceAll).List(checkCtx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=bosun"})
	if err != nil {
		return nil, err
	}
	return unfinishedReviewJobs(jobs.Items), nil
}

func preflightReset(ctx context.Context, o options, discover func(context.Context) []credentials.Credential, probe func(context.Context, string) error) error {
	if !o.localCredentials {
		return fmt.Errorf("reset cannot preserve the cluster's provider Secret with --local-credentials=false; use a local provider sign-in")
	}
	if o.purgeSnapshots && os.Getenv("BOSUN_REPOS_DIR") != "" {
		return fmt.Errorf("refusing to purge a custom BOSUN_REPOS_DIR; remove it manually if desired")
	}
	if ref, _ := reviewImage(version.Version); !pullable(ref) && !o.force {
		return fmt.Errorf("reviewer image %s exists only on the current Kind node and will be lost during reset; pass --force and then build and load it with ./scripts/kind-up.sh", ref)
	}
	found := discover(ctx)
	if err := credentials.Missing(o.provider, found); err != nil {
		return fmt.Errorf("cannot reset without local provider credentials: %w", err)
	}
	if err := probeSourceMismatch(o.provider, found); err != nil {
		return err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if err := probe(probeCtx, o.provider); err != nil {
		return fmt.Errorf("cannot reset without a working provider sign-in: %w", err)
	}
	_, err := cluster.HostRepos()
	return err
}

// Kind clusters share one host snapshot root, so purging it while another
// cluster exists could delete a checkout that another review is using.
func guardSharedSnapshots(ctx context.Context, o options) error {
	if !o.purgeSnapshots {
		return nil
	}
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(checkCtx, "kind", "get", "clusters").Output()
	if err != nil {
		return fmt.Errorf("cannot verify other Kind clusters before purging shared snapshots: %w", err)
	}
	var others []string
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.TrimSpace(line)
		if name != "" && name != o.cluster && name != "No kind clusters found." {
			others = append(others, name)
		}
	}
	if len(others) > 0 {
		return fmt.Errorf("cannot purge shared snapshots while other Kind clusters exist (%s); remove them separately or reset without --purge-snapshots", strings.Join(others, ", "))
	}
	return nil
}

func runReset(ctx context.Context, o options, out, errout io.Writer) int {
	if err := cluster.RequireTools(); err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	if err := preflightReset(ctx, o, credentials.Discover, probeLocalProvider); err != nil {
		fmt.Fprintln(errout, err)
		return 2
	}
	if err := guardSharedSnapshots(ctx, o); err != nil {
		fmt.Fprintln(errout, err)
		return 2
	}
	code, err := resetCluster(ctx, o.cluster, cluster.New(o.cluster), o.purgeSnapshots, o.force,
		func(ctx context.Context) ([]string, error) { return activeReviewJobs(ctx, o) }, cluster.RemoveSnapshots,
		func() int {
			if code := runUp(ctx, o, out, errout); code != 0 {
				return code
			}
			ref, _ := reviewImage(version.Version)
			probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			warning, imageErr := checkImage(probeCtx, ref, version.Version, kindInspector(o.cluster))
			if imageErr != nil {
				fmt.Fprintln(errout, imageErr)
				return 1
			}
			if warning != "" {
				fmt.Fprintln(errout, warning)
			}
			return 0
		}, out)
	if err != nil {
		fmt.Fprintln(errout, err)
	}
	return code
}
