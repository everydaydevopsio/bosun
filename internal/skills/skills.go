// Package skills installs Bosun's review skill for the coding agents that can
// run it.
//
// The skill is embedded rather than fetched so a binary installed from a tap
// can place it with no repository checked out, and so the instructions always
// match the CLI that ships them -- a skill that tells an agent to pass a flag
// the installed binary does not have fails in a way that looks like the agent's
// mistake.
package skills

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

//go:embed assets
var assets embed.FS

const (
	skillName = "bosun-review"
	assetPath = "assets/" + skillName + "/SKILL.md"
	// Version is the revision of the embedded review instructions. It changes
	// independently of the CLI release version.
	Version = "2"
)

var versionPattern = regexp.MustCompile(`<!-- bosun-skill-version: ([^ ]+) -->`)

// Hash of the exact skill shipped in v0.1.5, before skill revisions were
// recorded in the file. Keep this fixed when the current skill text changes.
const legacyV015SHA256 = "36f25878ec357ff7c9c9809ada075ea4bb3593baac7a5d695bc5bca92995dbed"

// Status describes one target without changing an installed skill.
type Status struct {
	Target  Target `json:"-"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	State   string `json:"state"`
	Version string `json:"version,omitempty"`
}

// Inspect compares each configured agent's skill with this binary's embedded
// copy. A differing file is never treated as safe to overwrite automatically.
func Inspect(home string) ([]Status, error) {
	want, err := assets.ReadFile(assetPath)
	if err != nil {
		return nil, err
	}
	var result []Status
	for _, target := range Targets(home) {
		path := filepath.Join(target.Dir, "skills", skillName, "SKILL.md")
		status := Status{Target: target, Name: target.Name, Path: path, State: "unconfigured"}
		if info, err := os.Stat(target.Dir); err != nil || !info.IsDir() {
			result = append(result, status)
			continue
		}
		data, err := os.ReadFile(path)
		switch {
		case os.IsNotExist(err):
			status.State = "missing"
		case err != nil:
			status.State = "unreadable"
		case bytes.Equal(data, want):
			status.State = "current"
		case fmt.Sprintf("%x", sha256.Sum256(data)) == legacyV015SHA256:
			status.State = "outdated"
		default:
			status.State = "different"
		}
		if err == nil {
			if match := versionPattern.FindSubmatch(data); len(match) == 2 {
				status.Version = string(match[1])
			} else if status.State == "outdated" {
				status.Version = "pre-versioned"
			} else {
				status.Version = "unknown"
			}
		}
		result = append(result, status)
	}
	return result, nil
}

// Target is a coding agent that reads skills from a configuration directory.
type Target struct {
	Name string // claude, codex
	Dir  string // absolute path to the agent's configuration directory
}

// Targets reports every agent Bosun can install for, resolved to real paths.
//
// Codex reads CODEX_HOME, and internal/credentials already honours it when
// looking for a sign-in. Hardcoding ~/.codex here would make the same binary
// disagree with itself: it would find your credentials under a relocated
// CODEX_HOME and then install the skill somewhere that Codex never reads.
func Targets(home string) []Target {
	codex := filepath.Join(home, ".codex")
	if relocated := strings.TrimSpace(os.Getenv("CODEX_HOME")); relocated != "" {
		codex = relocated
	}
	return []Target{
		{Name: "claude", Dir: filepath.Join(home, ".claude")},
		{Name: "codex", Dir: codex},
	}
}

// Action records what Install did to one file.
type Action string

const (
	Installed Action = "installed"
	Unchanged Action = "unchanged"
	Replaced  Action = "replaced"
	Kept      Action = "kept"
)

// Result is one target's outcome.
type Result struct {
	Target Target
	Path   string
	Action Action
}

// Detect reports the agents configured for this user, in a stable order.
func Detect(home string) []Target {
	var found []Target
	for _, t := range Targets(home) {
		if info, err := os.Stat(t.Dir); err == nil && info.IsDir() {
			found = append(found, t)
		}
	}
	return found
}

// Install writes the skill for each target.
//
// A file that differs from the shipped one is left alone unless force is set.
// It may be a local edit or an older version, and the two are indistinguishable
// from here -- so the safe reading is the one that does not discard someone's
// work on an accidental re-run.
func Install(home string, targets []Target, force bool) ([]Result, error) {
	want, err := assets.ReadFile(assetPath)
	if err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(targets))
	for _, t := range targets {
		dir := filepath.Join(t.Dir, "skills", skillName)
		path := filepath.Join(dir, "SKILL.md")
		action := Installed
		switch existing, err := os.ReadFile(path); {
		case err == nil && bytes.Equal(existing, want):
			results = append(results, Result{Target: t, Path: path, Action: Unchanged})
			continue
		case err == nil && !force:
			results = append(results, Result{Target: t, Path: path, Action: Kept})
			continue
		case err == nil:
			action = Replaced
		case !os.IsNotExist(err):
			return results, fmt.Errorf("read %s: %w", path, err)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return results, fmt.Errorf("create %s: %w", dir, err)
		}
		if err := os.WriteFile(path, want, 0o644); err != nil {
			return results, fmt.Errorf("write %s: %w", path, err)
		}
		results = append(results, Result{Target: t, Path: path, Action: action})
	}
	return results, nil
}

// Print writes the skill without touching the filesystem.
func Print(w io.Writer) error {
	body, err := assets.ReadFile(assetPath)
	if err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}
