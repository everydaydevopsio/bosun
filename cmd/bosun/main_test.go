package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/everydaydevopsio/bosun/internal/config"

	"github.com/everydaydevopsio/bosun/internal/version"
)

// metaCommand backs `bosun version` and `bosun help`, the two commands a release
// artifact must answer without a cluster. The publishing rules require the
// installed binary to report the release version, so this pins the contract.
func TestMetaCommand(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		want     bool
		contains string
	}{
		{name: "no arguments", args: nil, want: false},
		{name: "version", args: []string{"version"}, want: true, contains: "bosun 9.9.9"},
		{name: "version flag", args: []string{"--version"}, want: true, contains: "bosun 9.9.9"},
		{name: "short version flag", args: []string{"-v"}, want: true, contains: "bosun 9.9.9"},
		{name: "help", args: []string{"help"}, want: true, contains: "Usage: bosun"},
		{name: "help flag", args: []string{"--help"}, want: true, contains: "bosun review-status"},
		{name: "short help flag", args: []string{"-h"}, want: true, contains: "bosun serve"},
		{name: "review is not a meta command", args: []string{"review"}, want: false},
		{name: "up is not a meta command", args: []string{"up"}, want: false},
		{name: "down is not a meta command", args: []string{"down"}, want: false},
		{name: "unknown command", args: []string{"nope"}, want: false},
	}

	original := version.Version
	version.Version = "9.9.9"
	t.Cleanup(func() { version.Version = original })

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			got := metaCommand(tc.args, &out)
			if got != tc.want {
				t.Fatalf("metaCommand(%v) = %v, want %v", tc.args, got, tc.want)
			}
			if !tc.want {
				if out.Len() != 0 {
					t.Fatalf("unhandled args wrote %q, want no output", out.String())
				}
				return
			}
			if !strings.Contains(out.String(), tc.contains) {
				t.Fatalf("output %q does not contain %q", out.String(), tc.contains)
			}
		})
	}
}

// The help text is the only place the CLI advertises its commands. Every command
// main dispatches must appear there or users cannot discover it.
func TestUsageListsEveryCommand(t *testing.T) {
	for _, command := range []string{"review", "review-status", "init", "up", "down", "serve", "reviewer", "version"} {
		if !strings.Contains(usage, command) {
			t.Errorf("usage text does not mention %q", command)
		}
	}
}

// The review skill tells an agent to confirm its bosun is new enough by reading
// `bosun help` for the flags it is about to pass. That check is only meaningful
// if the review line actually advertises them: --local-credentials appeared
// solely under `bosun up`, so the check passed on any binary that had `up`,
// including one whose `review` predates both flags.
func TestUsageAdvertisesTheReviewFlagsTheSkillChecksFor(t *testing.T) {
	// The entry wraps, so take it from its first line up to the next command.
	var reviewLine string
	for _, line := range strings.Split(usage, "\n") {
		if strings.Contains(line, "bosun review ") {
			reviewLine = line
			continue
		}
		if reviewLine == "" {
			continue
		}
		if strings.Contains(line, "bosun ") {
			break
		}
		reviewLine += " " + strings.TrimSpace(line)
	}
	if reviewLine == "" {
		t.Fatal("usage text has no bosun review line")
	}
	for _, flag := range []string{"--detach", "--local-credentials"} {
		if !strings.Contains(reviewLine, flag) {
			t.Errorf("review usage line does not advertise %s:\n%s", flag, reviewLine)
		}
	}
}

// #95, Bosun review F1: a bad review.effort must stop the controller at start,
// where a crash-looping Deployment is visible, rather than fail every review
// while the server reports healthy.
func TestServerRefusesAnUnknownReviewEffort(t *testing.T) {
	if err := validateServer(config.Config{ReviewEffort: "max"}); err == nil || !strings.Contains(err.Error(), "invalid review effort") {
		t.Fatalf("err = %v", err)
	}
	for _, ok := range []string{"", "auto", "standard", "deep"} {
		if err := validateServer(config.Config{ReviewEffort: ok}); err != nil {
			t.Fatalf("%q rejected: %v", ok, err)
		}
	}
	// #105: so is a negative escalation threshold.
	if err := validateServer(config.Config{ReviewEscalateAboveLines: -1}); err == nil {
		t.Fatal("a negative escalation threshold was accepted")
	}
}
