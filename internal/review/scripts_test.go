package review

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalCommandsSelectKindContext(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	mocks := map[string]string{
		"kind":   "printf '%s\\n' selected\n",
		"docker": "exit 0\n",
		"helm":   "[ \"$1\" = --kube-context ] && [ \"$2\" = kind-selected ] || exit 40\nprintf 'helm\\n' >> \"$TEST_CALLS\"\n",
		"kubectl": `[ "$1" = --context ] && [ "$2" = kind-selected ] || exit 41
printf 'kubectl\n' >> "$TEST_CALLS"
shift 2
if [ "${1:-}" = -n ]; then shift 2; fi
case "$*" in
  'create -o name -f -') cat >/dev/null; printf 'job.batch/test\n' ;;
  'apply -f -') cat >/dev/null ;;
  *) exit 0 ;;
esac
`,
	}
	for name, body := range mocks {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/usr/bin/env bash\nset -eu\n"+body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	testGit(t, repo, "init", "-b", "main")
	writeTestFile(t, filepath.Join(repo, "file"), "base")
	testGit(t, repo, "add", ".")
	testGit(t, repo, "commit", "-m", "base")
	calls := filepath.Join(root, "calls")
	for _, script := range []string{"kind-up.sh"} {
		path, err := filepath.Abs("../../scripts/" + script)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("bash", path, repo)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "TEST_CALLS="+calls, "BOSUN_KIND_CLUSTER=selected", "BOSUN_SKIP_BUILD=1", "BOSUN_REVIEW_PROVIDER=codex-bosun", "OPENAI_API_KEY=test-only", "KUBECONFIG=/nonexistent-production-context")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v: %s", script, err, out)
		}
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "helm") || strings.Count(string(data), "kubectl") < 5 {
		t.Fatalf("commands not exercised: %s", data)
	}
}

func TestReviewWrapperForwardsArguments(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bosun")
	out := filepath.Join(dir, "args")
	body := "#!/usr/bin/env bash\nprintf '%s\\n' \"$@\" > \"$TEST_ARGS\"\n"
	if err := os.WriteFile(bin, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "../../scripts/review-local.sh", "/repo with spaces", "--provider", "claude-bosun")
	cmd.Env = append(os.Environ(), "BOSUN_BIN="+bin, "TEST_ARGS="+out)
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("%v: %s", e, b)
	}
	b, e := os.ReadFile(out)
	if e != nil {
		t.Fatal(e)
	}
	if string(b) != "review\n/repo with spaces\n--provider\nclaude-bosun\n" {
		t.Fatalf("arguments: %q", b)
	}
}
