package localreview

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/everydaydevopsio/bosun/internal/cluster"
	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/skills"
	"github.com/everydaydevopsio/bosun/internal/version"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type doctorCheck struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Version  string `json:"version,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Remedy   string `json:"remedy,omitempty"`
	Required bool   `json:"required"`
}

type doctorReport struct {
	Schema        int           `json:"schema"`
	Cluster       string        `json:"cluster"`
	CLIVersion    string        `json:"cli_version"`
	SkillVersion  string        `json:"skill_version"`
	ReviewerImage string        `json:"reviewer_image"`
	Healthy       bool          `json:"healthy"`
	Checks        []doctorCheck `json:"checks"`
}

func (r *doctorReport) add(c doctorCheck) {
	r.Checks = append(r.Checks, c)
	if c.Required && c.Status != "ok" {
		r.Healthy = false
	}
}

func (r doctorReport) print(out io.Writer, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	fmt.Fprintf(out, "reviewer image: %s\n", r.ReviewerImage)
	for _, c := range r.Checks {
		fmt.Fprintf(out, "%-15s %-12s", c.Name, c.Status)
		if c.Version != "" {
			fmt.Fprintf(out, " version=%s", c.Version)
		}
		if c.Detail != "" {
			fmt.Fprintf(out, " %s", c.Detail)
		}
		if c.Remedy != "" {
			fmt.Fprintf(out, " (fix: %s)", c.Remedy)
		}
		fmt.Fprintln(out)
	}
	return nil
}

func runDoctor(ctx context.Context, o options, out, errout io.Writer) int {
	image, _ := reviewImage(version.Version)
	r := doctorReport{Schema: 1, Cluster: o.cluster, CLIVersion: version.Version, SkillVersion: skills.Version, ReviewerImage: image, Healthy: true}
	r.add(doctorCheck{Name: "bosun CLI", Status: "ok", Version: version.Version, Required: true})
	doctorSkills(&r, o.provider)
	doctorCluster(ctx, o, &r)
	if err := r.print(out, o.json); err != nil {
		fmt.Fprintln(errout, "write doctor report:", err)
		return 1
	}
	if !r.Healthy {
		return 1
	}
	return 0
}

func doctorSkills(r *doctorReport, provider string) {
	selected := ""
	if strings.HasPrefix(provider, "claude") {
		selected = "claude"
	} else if strings.HasPrefix(provider, "codex") {
		selected = "codex"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		r.add(doctorCheck{Name: "skills", Status: "error", Detail: err.Error(), Required: true})
		return
	}
	statuses, err := skills.Inspect(home)
	if err != nil {
		r.add(doctorCheck{Name: "skills", Status: "error", Detail: err.Error(), Required: true})
		return
	}
	for _, s := range statuses {
		c := doctorCheck{Name: s.Name + " skill", Version: s.Version, Detail: s.Path,
			Required: s.Name == selected && s.State != "unconfigured" && s.State != "different"}
		switch s.State {
		case "current":
			c.Status = "ok"
		case "unconfigured":
			c.Status = "optional"
			c.Detail = "agent is not configured"
		case "missing":
			c.Status = "missing"
			c.Remedy = "bosun init --target " + s.Name
		case "outdated":
			c.Status = "outdated"
			c.Remedy = "bosun init --force --target " + s.Name
		default:
			c.Status = s.State
			c.Remedy = "inspect local edits before bosun init --force --target " + s.Name
		}
		r.add(c)
	}
}

func doctorCluster(ctx context.Context, o options, r *doctorReport) {
	reset := fmt.Sprintf("bosun reset --cluster %s --namespace %s --provider %s", o.cluster, o.namespace, o.provider)
	up := fmt.Sprintf("bosun up --cluster %s --namespace %s --local-credentials --provider %s", o.cluster, o.namespace, o.provider)
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	versionOutput, err := exec.CommandContext(probeCtx, "kind", "version").Output()
	if err != nil {
		r.add(doctorCheck{Name: "Kind CLI", Status: "error", Detail: "kind version unavailable", Remedy: "install kind and Docker", Required: true})
		return
	}
	r.add(doctorCheck{Name: "Kind CLI", Status: "ok", Version: strings.TrimSpace(string(versionOutput)), Required: true})
	exists, err := cluster.New(o.cluster).Exists(ctx)
	if err != nil {
		r.add(doctorCheck{Name: "Kind cluster", Status: "error", Detail: err.Error(), Remedy: "check Docker and kind", Required: true})
		return
	}
	if !exists {
		r.add(doctorCheck{Name: "Kind cluster", Status: "missing", Detail: o.cluster, Remedy: up, Required: true})
		return
	}
	r.add(doctorCheck{Name: "Kind cluster", Status: "ok", Detail: o.cluster, Required: true})
	doctorMount(ctx, o.cluster, r)
	c, err := client("kind-" + o.cluster)
	if err != nil {
		r.add(doctorCheck{Name: "Kubernetes", Status: "error", Detail: err.Error(), Remedy: reset, Required: true})
		return
	}
	kubeCtx, kubeCancel := context.WithTimeout(ctx, 15*time.Second)
	defer kubeCancel()
	nodes, err := c.CoreV1().Nodes().List(kubeCtx, metav1.ListOptions{})
	if err != nil {
		r.add(doctorCheck{Name: "Kubernetes", Status: "error", Detail: err.Error(), Remedy: reset, Required: true})
		return
	}
	node := findReadyNode(nodes.Items, o.cluster+"-control-plane")
	if node == nil {
		r.add(doctorCheck{Name: "Kind node", Status: "unready", Detail: o.cluster + "-control-plane", Remedy: reset, Required: true})
	} else {
		r.add(doctorCheck{Name: "Kind node", Status: "ok", Version: node.Status.NodeInfo.KubeletVersion, Detail: node.Name, Required: true})
	}
	serverVersion, err := c.Discovery().ServerVersion()
	if err != nil {
		r.add(doctorCheck{Name: "Kubernetes API", Status: "error", Detail: err.Error(), Required: true})
	} else {
		r.add(doctorCheck{Name: "Kubernetes API", Status: "ok", Version: serverVersion.GitVersion, Required: true})
	}
	doctorImage(ctx, o.cluster, r)
	if _, err := c.CoreV1().Namespaces().Get(kubeCtx, o.namespace, metav1.GetOptions{}); err != nil {
		r.add(doctorCheck{Name: "namespace", Status: "missing", Detail: o.namespace, Remedy: up, Required: true})
		return
	}
	r.add(doctorCheck{Name: "namespace", Status: "ok", Detail: o.namespace, Required: true})
	if err := checkClusterCredentials(kubeCtx, c, o.namespace, config.Load().AISecret, o.provider); err != nil {
		r.add(credentialDoctorCheck(err, up))
	} else {
		r.add(doctorCheck{Name: "credentials", Status: "ok", Detail: o.provider + " present (not live-tested)", Required: true})
	}
	doctorController(kubeCtx, o.namespace, c, r)
}

func credentialDoctorCheck(err error, up string) doctorCheck {
	check := doctorCheck{Name: "credentials", Status: "error", Detail: err.Error(), Remedy: "check Kubernetes access and provider configuration", Required: true}
	if apierrors.IsNotFound(err) || strings.Contains(err.Error(), "no usable credentials found") {
		check.Status, check.Remedy = "missing", up
	}
	return check
}

func findReadyNode(nodes []corev1.Node, name string) *corev1.Node {
	for i := range nodes {
		if nodes[i].Name != name {
			continue
		}
		for _, condition := range nodes[i].Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				return &nodes[i]
			}
		}
	}
	return nil
}

func doctorMount(ctx context.Context, clusterName string, r *doctorReport) {
	host, err := cluster.HostRepos()
	if err != nil {
		r.add(doctorCheck{Name: "snapshot mount", Status: "error", Detail: err.Error(), Required: true})
		return
	}
	info, err := os.Stat(host)
	if err != nil || !info.IsDir() {
		r.add(doctorCheck{Name: "snapshot mount", Status: "missing", Detail: host, Remedy: "bosun reset --cluster " + clusterName, Required: true})
		return
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	node := clusterName + "-control-plane"
	got, err := exec.CommandContext(probeCtx, "docker", "inspect", "--format", `{{range .Mounts}}{{if eq .Destination "`+cluster.ContainerRepos+`"}}{{.Source}}{{end}}{{end}}`, node).Output()
	if err != nil || strings.TrimSpace(string(got)) != host || exec.CommandContext(probeCtx, "docker", "exec", node, "test", "-d", cluster.ContainerRepos).Run() != nil {
		r.add(doctorCheck{Name: "snapshot mount", Status: "error", Detail: host + " is not visible at " + cluster.ContainerRepos, Remedy: "bosun reset --cluster " + clusterName, Required: true})
		return
	}
	r.add(doctorCheck{Name: "snapshot mount", Status: "ok", Detail: filepath.Base(host) + " -> " + cluster.ContainerRepos, Required: true})
}

func doctorImage(ctx context.Context, clusterName string, r *doctorReport) {
	ref, _ := reviewImage(version.Version)
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	r.add(inspectDoctorImage(probeCtx, ref, version.Version, kindInspector(clusterName)))
}

func inspectDoctorImage(ctx context.Context, ref, cliVersion string, inspect inspector) doctorCheck {
	data, found, err := inspect(ctx, ref)
	if err != nil {
		return doctorCheck{Name: "review image", Status: "error", Detail: "could not inspect " + ref, Remedy: "check Docker and the Kind node", Required: true}
	}
	if !found {
		status := "not cached"
		required := false
		if !pullable(ref) {
			status, required = "missing", true
		}
		remedy := "image will be pulled by the first review"
		if required {
			remedy = "build and load the development image"
		}
		return doctorCheck{Name: "review image", Status: status, Detail: ref, Remedy: remedy, Required: required}
	}
	labels, err := imageLabels(data)
	if err != nil {
		return doctorCheck{Name: "review image", Status: "error", Detail: "invalid image metadata for " + ref, Required: true}
	}
	c := doctorCheck{Name: "review image", Status: "ok", Version: labels[versionLabel], Detail: ref, Required: true}
	if labels[titleLabel] != "bosun" {
		c.Status, c.Remedy = "invalid", "load a Bosun reviewer image"
	} else if v := strings.TrimPrefix(labels[versionLabel], "v"); v == "" {
		c.Status, c.Remedy = "unknown", "use a Bosun image with a version label"
	} else if v != "dev" && v != strings.TrimPrefix(cliVersion, "v") {
		c.Status, c.Remedy = "mismatch", "bosun reset or update the reviewer image"
	}
	return c
}

func doctorController(ctx context.Context, namespace string, c kubernetes.Interface, r *doctorReport) {
	deployments, err := c.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=bosun"})
	if err != nil {
		r.add(doctorCheck{Name: "controller", Status: "error", Detail: "could not inspect Bosun deployment", Required: true})
		return
	}
	if len(deployments.Items) == 0 {
		r.add(doctorCheck{Name: "controller", Status: "optional", Detail: "not installed; local reviews do not need it"})
		return
	}
	for _, deployment := range deployments.Items {
		for _, container := range deployment.Spec.Template.Spec.Containers {
			if container.Name != "bosun" {
				continue
			}
			ref := container.Image
			imageVersion := ref
			if colon := strings.LastIndex(ref, ":"); colon > strings.LastIndex(ref, "/") {
				imageVersion = ref[colon+1:]
			}
			check := doctorCheck{Name: "controller", Status: "ok", Version: imageVersion, Detail: ref, Required: true}
			if deployment.Status.ReadyReplicas == 0 {
				check.Status = "unready"
				check.Remedy = "inspect the Bosun deployment in namespace " + namespace
			}
			if releaseVersion.MatchString(imageVersion) && strings.TrimPrefix(imageVersion, "v") != strings.TrimPrefix(version.Version, "v") {
				check.Status = "mismatch"
				check.Remedy = "upgrade the Bosun Helm release"
			}
			r.add(check)
			return
		}
	}
	r.add(doctorCheck{Name: "controller", Status: "invalid", Detail: "Bosun deployment has no bosun container", Required: true})
}
