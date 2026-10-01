package localreview

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeNested is write plus parent-directory creation, for the nested
// ignored paths this test needs.
func writeNested(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, path, body)
}

// A dirty-checkout snapshot is handed to a remote AI agent. Git-ignored files
// are, by convention, exactly where local secrets live: .env.local, .npmrc,
// nested .env files, credential caches. They are neither tracked nor part of
// the changes under review, so they must not be copied into the snapshot.
func TestDirtySnapshotExcludesGitIgnoredFiles(t *testing.T) {
	repo := t.TempDir()
	gitTest(t, repo, "init", "--initial-branch=main")
	write(t, filepath.Join(repo, ".gitignore"), ".env*\n.npmrc\nsecrets/\nnode_modules/\n")
	write(t, filepath.Join(repo, "main.go"), "package main\n")
	gitTest(t, repo, "add", ".")
	gitTest(t, repo, "commit", "-m", "initial")

	// Ignored, and all plausible secret carriers.
	secrets := map[string]string{
		".env":                      "ROOT_SECRET=1",
		".env.local":                "LOCAL_SECRET=sk-ignored-local",
		"service/.env":              "NESTED_SECRET=sk-ignored-nested",
		".npmrc":                    "//registry:_authToken=sk-ignored-npm",
		"secrets/token.txt":         "sk-ignored-dir",
		"node_modules/pkg/index.js": "module.exports = 1",
	}
	for rel, body := range secrets {
		writeNested(t, filepath.Join(repo, rel), body)
	}

	// Untracked but NOT ignored: a genuine working-tree change under review.
	write(t, filepath.Join(repo, "newfile.go"), "package main // new\n")
	// Tracked and modified: also under review.
	write(t, filepath.Join(repo, "main.go"), "package main // edited\n")

	snap, err := snapshot(context.Background(), repo, "", "")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	defer os.RemoveAll(snap.Path)

	for rel := range secrets {
		if _, err := os.Stat(filepath.Join(snap.Path, rel)); err == nil {
			body, _ := os.ReadFile(filepath.Join(snap.Path, rel))
			t.Errorf("git-ignored %s was copied into the snapshot: %q", rel, strings.TrimSpace(string(body)))
		}
	}

	for _, rel := range []string{"newfile.go", "main.go", ".gitignore"} {
		if _, err := os.Stat(filepath.Join(snap.Path, rel)); err != nil {
			t.Errorf("%s should be in the snapshot but is missing: %v", rel, err)
		}
	}

	body, err := os.ReadFile(filepath.Join(snap.Path, "main.go"))
	if err != nil || !strings.Contains(string(body), "edited") {
		t.Errorf("uncommitted edit to main.go was not carried into the snapshot: %q %v", body, err)
	}
}
