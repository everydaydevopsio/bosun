package skills

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// InstallPermissions installs opt-in Codex command rules. Claude's grants live
// in the skill frontmatter. Preserve user edits unless force is requested.
func InstallPermissions(targets []Target, force bool) ([]Result, error) {
	want, err := assets.ReadFile("assets/bosun-review.rules")
	if err != nil {
		return nil, err
	}
	var results []Result
	for _, target := range targets {
		if target.Name != "codex" {
			continue
		}
		path := filepath.Join(target.Dir, "rules", "bosun-review.rules")
		action := Installed
		existing, err := os.ReadFile(path)
		switch {
		case err == nil && bytes.Equal(existing, want):
			action = Unchanged
		case err == nil && !force:
			action = Kept
		case err == nil:
			action = Replaced
		case !os.IsNotExist(err):
			return results, fmt.Errorf("read %s: %w", path, err)
		}
		if action == Installed || action == Replaced {
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				return results, err
			}
			if err := os.WriteFile(path, want, 0644); err != nil {
				return results, err
			}
		}
		results = append(results, Result{Target: target, Path: path, Action: action})
	}
	return results, nil
}
