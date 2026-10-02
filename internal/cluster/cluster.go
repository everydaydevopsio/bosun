// Package cluster owns the local Kind cluster a review runs in, so the CLI can
// create and delete it without the repository's shell scripts.
package cluster

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// HostRepos is the directory snapshots are written to on the host, and
// ContainerRepos is where the node sees it. The pair is a contract between the
// CLI, the cluster config, and the Job's volume mount.
const (
	HostRepos      = "/tmp/bosun-repos"
	ContainerRepos = "/repos"
)

// configYAML is the cluster definition, embedded so a released binary can
// create a cluster with no repository checked out. Kept identical to the
// repository's kind-config.yaml by TestEmbeddedConfigMatchesRepositoryCopy.
//
//go:embed kind-config.yaml
var configYAML string

// runner executes an external command. Tests substitute their own.
type runner func(ctx context.Context, name string, args ...string) ([]byte, error)

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, detail)
		}
		return out, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return out, nil
}

// Kind is one local Kind cluster, addressed by name.
type Kind struct {
	Name string
	run  runner
}

func New(name string) *Kind { return &Kind{Name: name, run: execRunner} }

// Context reports the kubeconfig context kind writes for this cluster.
func (k *Kind) Context() string { return "kind-" + k.Name }

// RequireTools reports whether the binaries the lifecycle needs are installed.
func RequireTools() error { return requireTools(exec.LookPath) }

func requireTools(lookup func(string) (string, error)) error {
	var missing []string
	for _, tool := range []string{"kind", "docker"} {
		if _, err := lookup(tool); err != nil {
			missing = append(missing, tool)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("bosun manages its own local cluster and needs %s on PATH; install %s and run again", strings.Join(missing, " and "), strings.Join(missing, " and "))
}

// Exists reports whether the cluster is already present.
func (k *Kind) Exists(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := k.run(ctx, "kind", "get", "clusters")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == k.Name {
			return true, nil
		}
	}
	return false, nil
}

// Create brings up the cluster with the snapshot directory mounted.
//
// The host directory is created first and deliberately: left to Docker, the
// bind mount's source is created as root, and the unprivileged CLI then cannot
// write the snapshots the Job is meant to read.
func (k *Kind) Create(ctx context.Context) error {
	if err := os.MkdirAll(HostRepos, 0755); err != nil {
		return fmt.Errorf("create %s: %w", HostRepos, err)
	}
	f, err := os.CreateTemp("", "bosun-kind-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.WriteString(configYAML); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	// Creating a node, pulling its image, and waiting for the control plane to
	// become ready is minutes of work on a cold machine.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	_, err = k.run(ctx, "kind", "create", "cluster", "--name", k.Name, "--config", f.Name())
	return err
}

// Delete removes the cluster. Removing the node removes everything loaded into
// it, so no separate image cleanup is needed.
func (k *Kind) Delete(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	_, err := k.run(ctx, "kind", "delete", "cluster", "--name", k.Name)
	return err
}

// RemoveSnapshots deletes the host snapshot directory. It is separate from
// Delete because a cluster can be rebuilt while snapshots are still wanted.
func RemoveSnapshots() error {
	if filepath.Clean(HostRepos) != HostRepos || !strings.HasPrefix(HostRepos, "/tmp/") {
		return fmt.Errorf("refusing to remove %s", HostRepos)
	}
	return os.RemoveAll(HostRepos)
}
