package config

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/everydaydevopsio/bosun/internal/version"
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

// publish.yml pushes <version> and sha-<short> and nothing else, so the old
// ":latest" default named a tag that has never existed. A chart install never
// reached it -- the chart always sets BOSUN_REVIEW_IMAGE explicitly -- so it
// only ever bit someone running the controller outside the chart, who got
// ImagePullBackOff against a tag that is not there (#78).
func TestTheReviewerImageDefaultsToTheImagePublishedAlongsideTheBinary(t *testing.T) {
	t.Setenv("BOSUN_REVIEW_IMAGE", "")
	version.Version = "0.2.8"
	t.Cleanup(func() { version.Version = "dev" })
	if got := Load().ReviewImage; got != "ghcr.io/everydaydevopsio/bosun:0.2.8" {
		t.Fatalf("released controller defaults to %q", got)
	}
	version.Version = "v0.2.8"
	if got := Load().ReviewImage; got != "ghcr.io/everydaydevopsio/bosun:0.2.8" {
		t.Fatalf("a v-prefixed stamp defaults to %q", got)
	}
}

// `git describe` output for a local build names no published image, so it must
// not be turned into a registry reference that cannot be pulled. The dev tag
// is at least honest about being local.
func TestAnUnstampedControllerDefaultsToTheDevImage(t *testing.T) {
	t.Setenv("BOSUN_REVIEW_IMAGE", "")
	for _, v := range []string{"dev", "v0.2.8-2-gabc1234", "v0.2.8-dirty", ""} {
		version.Version = v
		t.Cleanup(func() { version.Version = "dev" })
		if got := Load().ReviewImage; got != "bosun:dev" {
			t.Errorf("version %q defaulted to %q, want bosun:dev", v, got)
		}
	}
}

func TestTheReviewerImageOverrideStillWins(t *testing.T) {
	t.Setenv("BOSUN_REVIEW_IMAGE", "registry.example.com/team/bosun:pinned")
	if got := Load().ReviewImage; got != "registry.example.com/team/bosun:pinned" {
		t.Fatalf("the explicit override was ignored: %q", got)
	}
}

// The tag that started this. No stamp, no override, and no code path may
// produce it, because nothing ever pushes it.
func TestNoCodePathProducesTheLatestTag(t *testing.T) {
	source, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatal(err)
	}
	// The literal that was the bug. Narrow rather than any ":latest"
	// substring, so the comment explaining the history does not trip it.
	if strings.Contains(string(source), publishedImage+":latest") {
		t.Error("config.go names a latest tag; publish.yml never pushes one")
	}
	for _, v := range []string{"dev", "0.2.8", "v0.2.8", "v0.2.8-dirty", ""} {
		version.Version = v
		t.Cleanup(func() { version.Version = "dev" })
		t.Setenv("BOSUN_REVIEW_IMAGE", "")
		if got := Load().ReviewImage; strings.HasSuffix(got, ":latest") {
			t.Errorf("version %q produced %q", v, got)
		}
	}
}

// publish.yml's own semver gate accepts a prerelease suffix:
//
//	^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$
//
// So v0.3.0-rc.1 is a tag that publishes an image. Rejecting it here sent a
// published prerelease controller to bosun:dev -- the same unpullable-tag
// failure #78 was about, one shape along. Found by Bosun reviewing #79.
func TestAPublishedPrereleaseUsesItsOwnPublishedImage(t *testing.T) {
	t.Setenv("BOSUN_REVIEW_IMAGE", "")
	t.Cleanup(func() { version.Version = "dev" })
	for _, v := range []string{"0.3.0-rc.1", "v0.3.0-rc.1", "v0.3.0-beta.2", "v0.3.0+build.5"} {
		version.Version = v
		want := publishedImage + ":" + strings.TrimPrefix(v, "v")
		if got := Load().ReviewImage; got != want {
			t.Errorf("version %q gave %q, want %q", v, got, want)
		}
	}
}

// The reason the pattern cannot simply copy publish.yml's: `git describe
// --tags --always --dirty`, which the Makefile stamps for local builds, emits
// valid semver prereleases of its own. No image is ever published under one,
// so widening for rc tags must not start treating a developer's working tree
// as a release.
func TestGitDescribeStampsAreStillNotReleases(t *testing.T) {
	t.Setenv("BOSUN_REVIEW_IMAGE", "")
	t.Cleanup(func() { version.Version = "dev" })
	for _, v := range []string{
		"v0.2.8-2-gabc1234",
		"v0.2.8-dirty",
		"v0.2.8-2-gabc1234-dirty",
		"0.2.8-14-g0123456789",
		"abc1234",
		"dev",
	} {
		version.Version = v
		if got := Load().ReviewImage; got != devImage {
			t.Errorf("local build stamp %q resolved to %q, want %q", v, got, devImage)
		}
	}
}
