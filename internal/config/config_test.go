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
