package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/everydaydevopsio/bosun/internal/skills"
	"github.com/spf13/pflag"
)

// runInit installs the review skill for the coding agents on this machine.
//
// It is separate from `bosun up` on purpose: writing a file into a home
// directory and creating a Kubernetes cluster are different weights of action,
// and bundling them makes init a command people hesitate to run.
func runInit(args []string, out, errout io.Writer) int {
	f := pflag.NewFlagSet("init", pflag.ContinueOnError)
	f.SetOutput(errout)
	var (
		targets = f.StringSlice("target", nil, "Agents to install for (claude, codex); default is whichever are configured")
		force   = f.Bool("force", false, "Replace a skill that differs from the one this binary ships")
		print   = f.Bool("print", false, "Write the skill to stdout without installing it")
	)
	if err := f.Parse(args); err != nil {
		if err == pflag.ErrHelp {
			return 0
		}
		fmt.Fprintln(errout, err)
		return 2
	}
	if f.NArg() > 0 {
		fmt.Fprintln(errout, "init takes no positional arguments")
		return 2
	}
	if *print {
		if err := skills.Print(out); err != nil {
			fmt.Fprintln(errout, err)
			return 1
		}
		return 0
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(errout, "cannot locate your home directory:", err)
		return 1
	}
	chosen, err := selectTargets(home, *targets)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 2
	}
	if len(chosen) == 0 {
		fmt.Fprintln(errout, "No coding agent found: expected ~/.claude or ~/.codex. Install Claude Code or Codex first, or pass --target to choose anyway.")
		return 2
	}

	results, err := skills.Install(home, chosen, *force)
	for _, r := range results {
		switch r.Action {
		case skills.Kept:
			fmt.Fprintf(errout, "%s: %s differs from the skill this bosun ships; left alone. Use --force to replace it.\n", r.Target.Name, r.Path)
		default:
			fmt.Fprintf(out, "%s: %s %s\n", r.Target.Name, r.Action, r.Path)
		}
	}
	if err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	fmt.Fprintln(errout, "Ask your agent to review your changes; it will pick the skill up on its next session.")
	return 0
}

// selectTargets honours an explicit choice and otherwise detects. An explicit
// target is installed even when its directory is absent: the user may be
// setting up a machine before installing the agent.
func selectTargets(home string, names []string) ([]skills.Target, error) {
	if len(names) == 0 {
		return skills.Detect(home), nil
	}
	known := map[string]skills.Target{
		"claude": {Name: "claude", Dir: ".claude"},
		"codex":  {Name: "codex", Dir: ".codex"},
	}
	var chosen []skills.Target
	for _, raw := range names {
		for _, name := range strings.Split(raw, ",") {
			name = strings.ToLower(strings.TrimSpace(name))
			if name == "" {
				continue
			}
			target, ok := known[name]
			if !ok {
				return nil, fmt.Errorf("unknown target %q: expected claude or codex", name)
			}
			chosen = append(chosen, target)
		}
	}
	return chosen, nil
}
