package localreview

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/everydaydevopsio/bosun/internal/cluster"
	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/credentials"
	"k8s.io/client-go/kubernetes"
)

// loadCredentials copies credentials discovered on this machine into the AI
// Secret the reviewer Job reads.
//
// Only the source of each credential is reported, never its value: the whole
// point of the flag is to move secrets without a human handling them, and a
// value echoed to a terminal ends up in scrollback and shell history.
func loadCredentials(ctx context.Context, client kubernetes.Interface, namespace, secret, provider string, found []credentials.Credential, notice func(string)) error {
	if err := credentials.Missing(provider, found); err != nil {
		return err
	}
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
	notice(fmt.Sprintf("Creating Kind cluster %q with %s mounted at %s; this takes a minute on a cold machine", name, cluster.HostRepos, cluster.ContainerRepos))
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
	defer cancel()
	if e = cluster.EnsureNamespace(nsCtx, c, o.namespace); e != nil {
		fmt.Fprintln(errout, "create namespace:", e)
		return 1
	}
	if o.localCredentials {
		if e = loadCredentials(nsCtx, c, o.namespace, cfg.AISecret, o.provider, credentials.Discover(nsCtx), notice); e != nil {
			fmt.Fprintln(errout, e)
			return 2
		}
	}
	fmt.Fprintf(out, "%s\n", contextName)
	notice(fmt.Sprintf("Cluster %q is ready for reviews in namespace %q", o.cluster, o.namespace))
	if !o.localCredentials {
		notice("No credentials were loaded; pass --local-credentials to copy this machine's provider sign-in into the cluster")
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
	if e = cluster.RemoveSnapshots(); e != nil {
		fmt.Fprintln(errout, "remove snapshots:", e)
		return 1
	}
	fmt.Fprintf(out, "removed %s\n", cluster.HostRepos)
	return 0
}
