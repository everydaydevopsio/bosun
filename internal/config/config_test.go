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
