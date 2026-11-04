package config

import (
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/everydaydevopsio/bosun/internal/version"
)

type Config struct {
	Namespace            string
	ReviewImage          string
	ReviewProvider       string
	GitHubTokenSecret    string
	AISecret             string
	JobTTLSeconds        int32
	ReviewTimeoutSeconds int64
	MaxConcurrentReviews int
	RunAsUser            int64
	ReviewCommand        string
	AllowedAssociations  map[string]bool
	WebhookSecret        string
	// ReviewResources is the resources block given to reviewer Jobs. An empty
	// quantity string omits that entry, so an operator can drop a limit
	// without having to drop the request with it.
	ReviewResources ReviewResources
	// ReviewBranchCreated enables the `create` trigger, which reviews a branch
	// push before any pull request exists. Off by default: it duplicates the
	// review the pull request itself triggers moments later, and both count
	// against the namespace-wide MaxConcurrentReviews. Turning it on is worth
	// it only when branches are pushed that will not become pull requests
	// soon, and the findings then publish as a check run on the commit.
	ReviewBranchCreated bool
	// SentinelIntervalSeconds paces the reconcile that reports reviewer
	// terminations the reviewer could not report itself. Zero disables it,
	// which returns an OOM-killed review to publishing nothing at all.
	SentinelIntervalSeconds int
}

// ReviewResources carries the reviewer Job's requests and limits as the
// quantity strings the chart renders, parsed at Job build time so a malformed
// value fails the submission it belongs to rather than controller startup.
type ReviewResources struct {
	CPURequest, MemoryRequest, CPULimit, MemoryLimit string
}

func Load() Config {
	return Config{
		Namespace: env("BOSUN_NAMESPACE", "bosun"), ReviewImage: env("BOSUN_REVIEW_IMAGE", defaultReviewImage()),
		ReviewProvider: env("BOSUN_REVIEW_PROVIDER", "codex-bosun"), GitHubTokenSecret: env("BOSUN_GITHUB_TOKEN_SECRET", "bosun-github"), AISecret: env("BOSUN_AI_SECRET", "bosun-ai"),
		JobTTLSeconds: int32(envInt("BOSUN_JOB_TTL_SECONDS", 3600)), ReviewTimeoutSeconds: int64(envInt("BOSUN_REVIEW_TIMEOUT_SECONDS", 1800)),
		MaxConcurrentReviews: envInt("BOSUN_MAX_CONCURRENT_REVIEWS", 3), RunAsUser: int64(envInt("BOSUN_RUN_AS_USER", 1001)),
		ReviewBranchCreated: envBool("BOSUN_REVIEW_BRANCH_CREATED"),
		ReviewCommand:       strings.ToLower(env("BOSUN_REVIEW_COMMAND", "@bridgectl review")), AllowedAssociations: associations(env("BOSUN_ALLOWED_ASSOCIATIONS", "OWNER,MEMBER,COLLABORATOR")),
		WebhookSecret: os.Getenv("GITHUB_WEBHOOK_SECRET"),
		ReviewResources: ReviewResources{
			CPURequest:    os.Getenv("BOSUN_REVIEW_CPU_REQUEST"),
			MemoryRequest: os.Getenv("BOSUN_REVIEW_MEMORY_REQUEST"),
			CPULimit:      os.Getenv("BOSUN_REVIEW_CPU_LIMIT"),
			MemoryLimit:   os.Getenv("BOSUN_REVIEW_MEMORY_LIMIT"),
		},
		SentinelIntervalSeconds: envInt("BOSUN_SENTINEL_INTERVAL_SECONDS", 30),
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func envInt(key string, fallback int) int {
	v, err := strconv.Atoi(env(key, strconv.Itoa(fallback)))
	if err != nil {
		return fallback
	}
	return v
}
func associations(value string) map[string]bool {
	out := map[string]bool{}
	for _, v := range strings.Split(value, ",") {
		if v = strings.TrimSpace(strings.ToUpper(v)); v != "" {
			out[v] = true
		}
	}
	return out
}

// envBool fails safe: anything unset or unparseable is false. The one caller
// enables a trigger that costs model credits, so an operator typo must not
// turn it on.
func envBool(key string) bool {
	value, err := strconv.ParseBool(os.Getenv(key))
	return err == nil && value
}

// devImage is the tag scripts/kind-up.sh builds and loads into the Kind node.
const devImage = "bosun:dev"

// publishedImage is the repository publish.yml pushes release images to.
const publishedImage = "ghcr.io/everydaydevopsio/bosun"

// releaseVersion matches a version that actually reaches the registry.
//
// It follows publish.yml's own "Tag is semver" gate as far as the prerelease
// suffix, so a published v0.3.0-rc.1 resolves to the image published alongside
// it rather than to the dev tag. Build metadata is deliberately excluded even
// though that gate allows it: the image is tagged `type=raw` from the version
// verbatim, and a Docker tag must match [a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}, so
// a "+" version never publishes an image at all. Naming one would produce a
// reference that cannot even be parsed.
var releaseVersion = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)

// gitDescribed matches what the Makefile stamps for a local build:
// `git describe --tags --always --dirty`, which emits "v0.2.8-2-gabc1234" and
// "v0.2.8-dirty".
//
// Those are valid semver prereleases, so widening releaseVersion to accept rc
// tags would otherwise start treating a developer's working tree as a release
// and name an image nobody ever pushed -- the same failure as the ":latest"
// default, one shape along. A commit-count-and-hash suffix, or a dirty marker,
// is never what a release tag looks like.
var gitDescribed = regexp.MustCompile(`-[0-9]+-g[0-9a-f]{7,}|-dirty$`)

// defaultReviewImage is the image reviewer Jobs run when nothing names one.
//
// This used to be a hardcoded latest tag, which publish.yml has never
// pushed, so
// an install that reached the default got ImagePullBackOff against something
// that does not exist (#78). A chart install never reached it -- the chart
// always sets BOSUN_REVIEW_IMAGE explicitly -- so it only bit someone running
// the controller outside the chart.
//
// A released controller therefore launches the image published alongside it,
// read from the same stamped version the CLI uses in
// internal/localreview.reviewImage. An unstamped or git-described build names
// no published image, so it falls back to the dev tag rather than inventing a
// registry reference that cannot be pulled.
func defaultReviewImage() string {
	if releaseVersion.MatchString(version.Version) && !gitDescribed.MatchString(version.Version) {
		return publishedImage + ":" + strings.TrimPrefix(version.Version, "v")
	}
	return devImage
}
