package config

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The chart renders environment variables that this package reads by name.
// Nothing connects the two but the spelling, so a rename on either side fails
// silently: the controller simply sees an unset value and falls back to a
// default, with no error anywhere. That is the same shape as the Role that
// granted "update" after the code had moved to Patch -- two files agreeing in
// review and disagreeing at runtime.
func TestChartAndConfigAgreeOnReviewEnvironmentNames(t *testing.T) {
	chart, err := os.ReadFile("../../charts/bosun/templates/deployment.yaml")
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatal(err)
	}

	names := regexp.MustCompile(`BOSUN_REVIEW_[A-Z_]+`)
	rendered := unique(names.FindAllString(string(chart), -1))
	read := unique(names.FindAllString(string(source), -1))

	for _, name := range rendered {
		if !contains(read, name) {
			t.Errorf("the chart renders %s but config.Load never reads it; the value is silently discarded", name)
		}
	}
	// The reverse direction is not symmetrical: jobs.go sets some reviewer
	// variables directly on the Job, so config may legitimately read a name
	// the controller's own Deployment does not carry. Only the resource
	// variables are chart-supplied, so those must appear in both.
	for _, name := range []string{
		"BOSUN_REVIEW_CPU_REQUEST", "BOSUN_REVIEW_MEMORY_REQUEST",
		"BOSUN_REVIEW_CPU_LIMIT", "BOSUN_REVIEW_MEMORY_LIMIT",
	} {
		if !contains(rendered, name) {
			t.Errorf("%s is read by config.Load but the chart never renders it; the value can only ever be empty", name)
		}
		if !contains(read, name) {
			t.Errorf("%s is missing from config.Load", name)
		}
	}
}

func unique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func contains(haystack []string, needle string) bool {
	for _, v := range haystack {
		if strings.EqualFold(v, needle) {
			return true
		}
	}
	return false
}

// The sentinel's interval is the switch between "an OOM-killed review tells
// the pull request nothing" and "it does". It does not share the BOSUN_REVIEW_
// prefix the test above sweeps, so it needs its own check against the same
// silent-rename failure.
func TestChartAndConfigAgreeOnTheSentinelInterval(t *testing.T) {
	const name = "BOSUN_SENTINEL_INTERVAL_SECONDS"
	chart, err := os.ReadFile("../../charts/bosun/templates/deployment.yaml")
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(chart), name) {
		t.Errorf("the chart never renders %s, so the sentinel can only ever run at its default", name)
	}
	if !strings.Contains(string(source), name) {
		t.Errorf("config.Load never reads %s, so the chart value is silently discarded", name)
	}
}

// A default of zero would disable the sentinel for every install that does not
// set it, which is the silence this exists to remove.
func TestTheSentinelIsOnByDefault(t *testing.T) {
	t.Setenv("BOSUN_SENTINEL_INTERVAL_SECONDS", "")
	if got := Load().SentinelIntervalSeconds; got <= 0 {
		t.Fatalf("sentinel disabled by default (interval %d)", got)
	}
	t.Setenv("BOSUN_SENTINEL_INTERVAL_SECONDS", "0")
	if got := Load().SentinelIntervalSeconds; got != 0 {
		t.Fatalf("the documented opt-out does not disable the sentinel: got %d", got)
	}
}

// Branch review duplicates the review the pull request itself triggers, and
// both count against the namespace-wide maxConcurrent. Defaulting it on would
// make an ordinary `git push -u` then `gh pr create` cost two reviews and risk
// losing the pull-request one to a 503 GitHub never redelivers, so the default
// is load-bearing rather than cosmetic.
func TestBranchReviewIsOffUnlessAskedFor(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{{"", false}, {"false", false}, {"true", true}, {"1", true}, {"TRUE", true}, {"yes", false}, {"", false}} {
		t.Setenv("BOSUN_REVIEW_BRANCH_CREATED", tc.value)
		if got := Load().ReviewBranchCreated; got != tc.want {
			t.Errorf("BOSUN_REVIEW_BRANCH_CREATED=%q gave %v, want %v", tc.value, got, tc.want)
		}
	}
}

// The chart is the only thing that sets this variable on the controller, and
// the default it renders is the behaviour every install gets.
func TestTheChartDefaultsBranchReviewOff(t *testing.T) {
	values, err := os.ReadFile("../../charts/bosun/values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(values), "branchCreated: false") {
		t.Error("charts/bosun/values.yaml no longer defaults review.triggers.branchCreated to false")
	}
}

// The default trigger names an account the project owns. "@bridgectl" was not
// a GitHub user, so it never autocompleted, and anyone registering it would be
// notified by every review request. "@bosun-review" is reserved by the project.
func TestTheDefaultReviewCommandMentionsTheBosunReviewAccount(t *testing.T) {
	t.Setenv("BOSUN_REVIEW_COMMAND", "")
	if got := Load().ReviewCommand; got != "@bosun-review review" {
		t.Fatalf("default review command %q, want %q", got, "@bosun-review review")
	}
	t.Setenv("BOSUN_REVIEW_COMMAND", "@BridgeCtl Review")
	if got := Load().ReviewCommand; got != "@bridgectl review" {
		t.Fatalf("override not honoured case-insensitively: got %q", got)
	}
}

// Installs that taught their team the old phrase keep it by setting
// review.command; that only works if the chart renders it.
func TestTheChartRendersTheReviewCommand(t *testing.T) {
	chart, err := os.ReadFile("../../charts/bosun/templates/deployment.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(chart), "BOSUN_REVIEW_COMMAND") {
		t.Error("charts/bosun/templates/deployment.yaml does not render BOSUN_REVIEW_COMMAND")
	}
	values, err := os.ReadFile("../../charts/bosun/values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(values), `command: "@bosun-review review"`) {
		t.Error(`charts/bosun/values.yaml does not default review.command to "@bosun-review review"`)
	}
}
