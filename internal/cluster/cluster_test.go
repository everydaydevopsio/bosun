package cluster

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

type call struct {
	name string
	args []string
}

func recorder(out string, err error) (*[]call, runner) {
	var calls []call
	return &calls, func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, call{name, args})
		return []byte(out), err
	}
}

// The embedded config is what a released CLI creates clusters from, and the
// repository copy is what scripts/kind-up.sh uses. They must not drift: a
// cluster created from one and inspected against the other would be missing the
// snapshot mount that every local review depends on.
func TestEmbeddedConfigMatchesRepositoryCopy(t *testing.T) {
	b, e := os.ReadFile("../../kind-config.yaml")
	if e != nil {
		t.Fatal(e)
	}
	if string(b) != configYAML {
		t.Error("internal/cluster/kind-config.yaml and kind-config.yaml differ; update both")
	}
}

// The snapshot directory is a host mount shared by the CLI, which writes
// snapshots into it, and the Job, which reads them from /repos. The constant and
// the cluster config are the two halves of that contract.
func TestEmbeddedConfigMountsTheSnapshotDirectory(t *testing.T) {
	var parsed struct {
		Nodes []struct {
			Role        string `json:"role"`
			ExtraMounts []struct {
				HostPath      string `json:"hostPath"`
				ContainerPath string `json:"containerPath"`
			} `json:"extraMounts"`
		} `json:"nodes"`
	}
	if e := yaml.Unmarshal([]byte(configYAML), &parsed); e != nil {
		t.Fatal(e)
	}
	if len(parsed.Nodes) != 1 || parsed.Nodes[0].Role != "control-plane" {
		t.Fatalf("want one control-plane node, got %+v", parsed.Nodes)
	}
	mounts := parsed.Nodes[0].ExtraMounts
	if len(mounts) != 1 || mounts[0].HostPath != HostRepos || mounts[0].ContainerPath != ContainerRepos {
		t.Fatalf("mount %+v does not map %s to %s", mounts, HostRepos, ContainerRepos)
	}
}

func TestExists(t *testing.T) {
	tests := []struct {
		name, output string
		err          error
		want         bool
		wantErr      bool
	}{
		{name: "named cluster is listed", output: "other\nbosun\n", want: true},
		{name: "only another cluster", output: "other\n", want: false},
		{name: "no clusters at all", output: "", want: false},
		// A near-miss must not count: "bosun-two" is a different cluster.
		{name: "prefix is not a match", output: "bosun-two\n", want: false},
		{name: "kind failed", err: errors.New("kind: not found"), wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls, run := recorder(tc.output, tc.err)
			got, e := (&Kind{Name: "bosun", run: run}).Exists(context.Background())
			if tc.wantErr {
				if e == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if got != tc.want {
				t.Errorf("Exists = %v, want %v", got, tc.want)
			}
			if len(*calls) != 1 || (*calls)[0].name != "kind" || strings.Join((*calls)[0].args, " ") != "get clusters" {
				t.Errorf("ran %+v, want kind get clusters", *calls)
			}
		})
	}
}

func TestCreateRunsKindWithTheEmbeddedConfig(t *testing.T) {
	calls, run := recorder("", nil)
	k := &Kind{Name: "bosun", run: run}
	if e := k.Create(context.Background()); e != nil {
		t.Fatal(e)
	}
	if len(*calls) != 1 {
		t.Fatalf("ran %d commands, want 1", len(*calls))
	}
	c := (*calls)[0]
	joined := strings.Join(c.args, " ")
	if c.name != "kind" || !strings.HasPrefix(joined, "create cluster --name bosun --config ") {
		t.Fatalf("ran %s %s, want kind create cluster --name bosun --config <file>", c.name, joined)
	}
	// The config is written to a temporary file for kind to read, and must not
	// outlive the call.
	path := c.args[len(c.args)-1]
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Errorf("temporary config %s was left behind", path)
	}
	// The host side of the mount must exist before kind creates the node, or
	// Docker creates it as root and snapshots cannot be written into it.
	if _, e := os.Stat(HostRepos); e != nil {
		t.Errorf("%s was not created: %v", HostRepos, e)
	}
}

func TestDelete(t *testing.T) {
	calls, run := recorder("", nil)
	if e := (&Kind{Name: "bosun", run: run}).Delete(context.Background()); e != nil {
		t.Fatal(e)
	}
	if got := strings.Join((*calls)[0].args, " "); got != "delete cluster --name bosun" {
		t.Errorf("ran kind %s, want delete cluster --name bosun", got)
	}
}

// A missing kind or docker binary is the most common reason bootstrap cannot
// work, and the message has to say which one to install.
func TestRequireTools(t *testing.T) {
	e := requireTools(func(string) (string, error) { return "", errors.New("not found") })
	if e == nil {
		t.Fatal("expected an error when no tools are present")
	}
	for _, want := range []string{"kind", "docker"} {
		if !strings.Contains(e.Error(), want) {
			t.Errorf("error %q does not mention %q", e, want)
		}
	}
	if e := requireTools(func(string) (string, error) { return "/usr/bin/x", nil }); e != nil {
		t.Errorf("unexpected error when both tools are present: %v", e)
	}
}
