// Package localreview runs local snapshots through a configured Kind cluster.
package localreview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/jobs"
	"github.com/everydaydevopsio/bosun/internal/progress"
	"github.com/everydaydevopsio/bosun/internal/review"
	"github.com/spf13/pflag"
	"golang.org/x/sys/unix"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type options struct {
	branch, base, provider, context, namespace string
	timeout                                    time.Duration
	json, detach, follow                       bool
	owner                                      bool
}
type record struct {
	Job, Context, Namespace, Snapshot, Repo, Provider, Image string
	Started                                                  time.Time
	Finished                                                 time.Time
	WorkerFinished                                           time.Time
	TypicalLow, TypicalHigh                                  time.Duration
	Changes                                                  int
	Result, Outcome                                          string
	LastSeq                                                  uint64
}

func stateDir() (string, error) {
	d := os.Getenv("BOSUN_STATE_DIR")
	if d == "" {
		d = os.Getenv("XDG_STATE_HOME")
		if d == "" {
			h, e := os.UserHomeDir()
			if e != nil {
				return "", e
			}
			d = filepath.Join(h, ".local", "state")
		}
		d = filepath.Join(d, "bosun")
	}
	return d, os.MkdirAll(d, 0700)
}
func save(r *record) error {
	d, e := stateDir()
	if e != nil {
		return e
	}
	lock, e := os.OpenFile(filepath.Join(d, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = unix.Flock(int(lock.Fd()), unix.LOCK_EX); e != nil {
		return e
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	// Concurrent viewers must not replace a newer or terminal summary.
	if b, err := os.ReadFile(filepath.Join(d, r.Job+".json")); err == nil {
		var previous record
		if json.Unmarshal(b, &previous) == nil {
			if !previous.Finished.IsZero() || (previous.LastSeq > r.LastSeq && r.Finished.IsZero()) {
				return nil
			}
		}
	}
	b, e := json.Marshal(r)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(d, ".run-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(f.Name(), filepath.Join(d, r.Job+".json"))
}
func load(job string) (record, error) {
	var r record
	if filepath.Base(job) != job {
		return r, fmt.Errorf("invalid job name")
	}
	d, e := stateDir()
	if e != nil {
		return r, e
	}
	b, e := os.ReadFile(filepath.Join(d, job+".json"))
	if e != nil {
		return r, e
	}
	e = json.Unmarshal(b, &r)
	return r, e
}
func parse(command string, args []string, errout io.Writer) (options, []string, error) {
	cfg := config.Load()
	o := options{}
	f := pflag.NewFlagSet(command, pflag.ContinueOnError)
	f.SetOutput(errout)

	f.BoolVar(&o.json, "json", false, "Emit versioned JSON events")
	if command == "review" {
		f.StringVar(&o.context, "context", "kind-"+env("BOSUN_KIND_CLUSTER", "bosun"), "Kubernetes context for the local Kind cluster")
		f.StringVar(&o.namespace, "namespace", cfg.Namespace, "Kubernetes namespace")
		f.StringVar(&o.branch, "branch", "", "Committed branch/ref to review (default: current checkout including dirty files)")
		f.StringVar(&o.base, "base", "", "Comparison base (default: repository default branch)")
		f.StringVar(&o.provider, "provider", cfg.ReviewProvider, "Headless bridgectl provider")
		f.DurationVar(&o.timeout, "timeout", time.Duration(cfg.ReviewTimeoutSeconds)*time.Second, "Review execution timeout")
		f.BoolVar(&o.detach, "detach", false, "Submit without waiting; retain snapshot")
	} else {
		f.BoolVar(&o.follow, "follow", false, "Follow until completion")
	}
	e := f.Parse(args)
	if e != nil {
		return o, nil, e
	}
	if f.NArg() > 1 {
		return o, nil, fmt.Errorf("expected at most one path or job name")
	}
	if command == "review" && o.timeout < time.Second {
		return o, nil, fmt.Errorf("--timeout must be at least 1s")
	}
	return o, f.Args(), nil
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func client(ctxName string) (kubernetes.Interface, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	c, e := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: ctxName}).ClientConfig()
	if e != nil {
		return nil, e
	}
	c.Timeout = 15 * time.Second
	return kubernetes.NewForConfig(c)
}
func Run(ctx context.Context, command string, args []string, out, errout io.Writer) int {
	o, pos, e := parse(command, args, errout)
	if errors.Is(e, pflag.ErrHelp) {
		return 0
	}
	if e != nil {
		fmt.Fprintln(errout, e)
		return 2
	}
	if command == "review-status" {
		if len(pos) != 1 {
			fmt.Fprintln(errout, "usage: bosun review-status JOB [--follow]")
			return 2
		}
		r, e := load(pos[0])
		if e != nil {
			fmt.Fprintln(errout, "Load review metadata:", e)
			return 1
		}
		if r.Outcome != "" && !r.Finished.IsZero() {
			return monitor(ctx, nil, &r, o, out, errout)
		}
		c, e := client(r.Context)
		if e != nil {
			fmt.Fprintln(errout, e)
			return 1
		}
		return monitor(ctx, c, &r, o, out, errout)
	}
	if o.provider == "codex" {
		fmt.Fprintln(errout, "provider codex is interactive; use --provider codex-bosun")
		return 2
	}
	path := "."
	if len(pos) == 1 {
		path = pos[0]
	}
	reporter := progress.New(errout, "launcher")
	if o.json {
		reporter = progress.New(out, "launcher")
	}
	notice := func(message string) {
		if o.json {
			reporter.Emit("status", message)
		} else {
			fmt.Fprintln(errout, message)
		}
	}
	c, e := client(o.context)
	if e != nil {
		fmt.Fprintln(errout, e)
		return 2
	}
	// The snapshot is a host mount: verify this context refers to the expected Kind node.
	if !strings.HasPrefix(o.context, "kind-") {
		fmt.Fprintln(errout, "local snapshots require a configured Kind context; remote clusters are unsupported")
		return 2
	}
	cluster := strings.TrimPrefix(o.context, "kind-")
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	nodes, e := c.CoreV1().Nodes().List(checkCtx, metav1.ListOptions{})
	cancel()
	if e != nil {
		fmt.Fprintln(errout, "Cannot reach Kind cluster; run make kind-up:", e)
		return 1
	}
	found := false
	for _, n := range nodes.Items {
		if n.Name == cluster+"-control-plane" {
			found = true
		}
	}
	if !found {
		fmt.Fprintln(errout, "context does not point to the expected Kind cluster")
		return 2
	}
	// Inspect only mount metadata, never container credentials.
	b, e := exec.CommandContext(ctx, "docker", "inspect", "--format", `{{range .Mounts}}{{if eq .Destination "/repos"}}{{.Source}}{{end}}{{end}}`, cluster+"-control-plane").Output()
	if e != nil || strings.TrimSpace(string(b)) != "/tmp/bosun-repos" {
		fmt.Fprintln(errout, "Kind /repos mount is missing or inaccessible; run make kind-up on this host")
		return 2
	}
	started := time.Now()
	notice("Preparing repository snapshot")
	s, e := snapshot(ctx, path, o.branch, o.base)
	if e != nil {
		fmt.Fprintln(errout, e)
		return 2
	}
	eventFile, e := os.OpenFile(s.Path+".events", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0660)
	if e != nil {
		os.RemoveAll(s.Path)
		fmt.Fprintln(errout, e)
		return 1
	}
	e = eventFile.Chmod(0660)
	eventFile.Close()
	if e != nil {
		os.RemoveAll(s.Path)
		os.Remove(s.Path + ".events")
		fmt.Fprintln(errout, e)
		return 1
	}
	submitted := false
	defer func() {
		if !submitted {
			os.RemoveAll(s.Path)
			os.Remove(s.Path + ".events")
		}
	}()
	cfg := config.Load()
	cfg.Namespace = o.namespace
	cfg.ReviewProvider = o.provider
	cfg.ReviewTimeoutSeconds = int64(o.timeout.Seconds())
	cfg.ReviewImage = env("BOSUN_DEV_IMAGE", "bosun:dev")
	expectedJob := jobs.Name(filepath.Base(s.Repo), s.Branch, filepath.Base(s.Path))
	r := record{Job: expectedJob, Context: o.context, Namespace: o.namespace, Snapshot: s.Path, Repo: s.Repo, Provider: o.provider, Image: cfg.ReviewImage, Started: started, Changes: s.Changes}
	if e = save(&r); e != nil {
		fmt.Fprintln(errout, "Cannot save reconnect metadata:", e)
		return 1
	}
	job, e := jobs.SubmitLocal(ctx, c, cfg, review.Request{Repo: filepath.Base(s.Repo), Ref: s.Branch, SHA: s.Head, Trigger: "local-kind"}, filepath.Base(s.Path), s.Base, s.Committed, int64(os.Getgid()))
	if e != nil {
		// A create RPC can succeed at the server even if the response is lost.
		// Retain the snapshot whenever a create was attempted.
		if job != "" {
			submitted = true
			fmt.Fprintln(errout, "Submission outcome uncertain; snapshot retained. Inspect with bosun review-status", expectedJob)
		}
		fmt.Fprintln(errout, "Submit review:", e)
		return 1
	}
	submitted = true

	scope := "including working-tree changes"
	if s.Committed {
		scope = "committed content only"
	}
	notice(fmt.Sprintf("Review %s | %s at %.12s | %s | provider %s | timeout %s + 2m startup allowance", job, s.Branch, s.Head, scope, o.provider, o.timeout))
	notice(estimate(&r))
	if e = save(&r); e != nil {
		notice("Could not persist estimate: " + e.Error())
	}
	if o.detach {
		if o.json {
			reporter.Emit("submitted", job)
		} else {
			fmt.Fprintln(out, job)
		}
		return 0
	}
	o.follow = true
	o.owner = true
	return monitor(ctx, c, &r, o, out, errout)
}
func estimate(current *record) string {
	d, e := stateDir()
	if e != nil {
		return "Estimate unavailable"
	}
	files, _ := filepath.Glob(filepath.Join(d, "*.json"))
	var durations []float64
	for _, f := range files {
		b, e := os.ReadFile(f)
		if e != nil {
			continue
		}
		var r record
		if json.Unmarshal(b, &r) != nil {
			continue
		}
		if r.Outcome == "completed" && r.Repo == current.Repo && r.Provider == current.Provider && r.Image == current.Image && r.Changes <= 2*(current.Changes+1) && current.Changes <= 2*(r.Changes+1) {
			durations = append(durations, r.Finished.Sub(r.Started).Seconds())
		}
	}
	if len(durations) < 5 {
		return fmt.Sprintf("Estimate unavailable: %d comparable successful runs (need 5); timeout is not an estimate", len(durations))
	}
	sort.Float64s(durations)
	current.TypicalLow = time.Duration(durations[len(durations)/10]) * time.Second
	current.TypicalHigh = time.Duration(durations[(len(durations)-1)*9/10]) * time.Second
	return fmt.Sprintf("Typical duration: %s–%s from %d comparable runs (approximate; model/load may differ)", time.Duration(durations[len(durations)/10])*time.Second, time.Duration(durations[(len(durations)-1)*9/10])*time.Second, len(durations))
}
