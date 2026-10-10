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
	"embed"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

//go:embed assets
var assets embed.FS

const (
	skillName = "bosun-review"
	assetPath = "assets/" + skillName + "/SKILL.md"
)

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
