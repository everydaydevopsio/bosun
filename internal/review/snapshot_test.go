package review

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func testGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Bosun Test", "-c", "user.email=test@example.invalid"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestLinkedWorktreeSnapshotIsSelfContained(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "main")
	worktree := filepath.Join(root, "linked")
	snapshot := filepath.Join(root, "snapshot")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	testGit(t, source, "init", "-b", "main")
	writeTestFile(t, filepath.Join(source, "tracked"), "base\n")
	writeTestFile(t, filepath.Join(source, "deleted"), "delete me\n")
	testGit(t, source, "add", ".")
	testGit(t, source, "commit", "-m", "base")
	base := testGit(t, source, "rev-parse", "HEAD")
	testGit(t, source, "update-ref", "refs/remotes/origin/main", base)
	testGit(t, source, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	testGit(t, source, "worktree", "add", "-b", "feature", worktree)
	writeTestFile(t, filepath.Join(worktree, "tracked"), "staged\n")
	writeTestFile(t, filepath.Join(worktree, "new"), "staged new\n")
	testGit(t, worktree, "add", "tracked", "new")
	writeTestFile(t, filepath.Join(worktree, "tracked"), "unstaged\n")
	writeTestFile(t, filepath.Join(worktree, "untracked"), "local\n")
	writeTestFile(t, filepath.Join(worktree, ".env"), "PRIVATE=not-for-review\n")
	if err := os.Remove(filepath.Join(worktree, "deleted")); err != nil {
		t.Fatal(err)
	}
	staged := testGit(t, worktree, "diff", "--cached", "--binary")
	unstaged := testGit(t, worktree, "diff", "--binary")
	script, err := filepath.Abs("../../scripts/snapshot.sh")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("bash", script, worktree, snapshot).CombinedOutput()
	if err != nil {
		t.Fatalf("snapshot: %v: %s", err, out)
	}
	if err := os.Rename(source, source+"-offline"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(worktree, worktree+"-offline"); err != nil {
		t.Fatal(err)
	}
	if got := testGit(t, snapshot, "diff", "--cached", "--binary"); got != staged {
		t.Fatalf("staged changes lost: %s", got)
	}
	if got := testGit(t, snapshot, "diff", "--binary"); got != unstaged {
		t.Fatalf("unstaged changes lost: %s", got)
	}
	if got := testGit(t, snapshot, "rev-parse", "origin/main"); got != base {
		t.Fatal("default branch ref lost")
	}
	if got := testGit(t, snapshot, "symbolic-ref", "refs/remotes/origin/HEAD"); got != "refs/remotes/origin/main" {
		t.Fatal("default branch symbolic ref lost")
	}
	if got := testGit(t, snapshot, "branch", "--show-current"); got != "feature" {
		t.Fatal("worktree branch identity lost")
	}
	testGit(t, snapshot, "fsck", "--full")
	if _, err := os.Stat(filepath.Join(snapshot, ".env")); !os.IsNotExist(err) {
		t.Fatal(".env copied")
	}
	if data, err := os.ReadFile(filepath.Join(snapshot, "untracked")); err != nil || string(data) != "local\n" {
		t.Fatal("untracked file lost")
	}
}
