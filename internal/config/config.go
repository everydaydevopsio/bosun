package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Namespace      string
	ReviewImage    string
	ReviewProvider string
	// ReviewEffort is how hard the reviewer works: EffortAuto (standard,
	// escalated to deep for a large change, #105), EffortStandard (the
	// provider's default), or EffortDeep (#95). See NormalizeEffort.
	ReviewEffort string
	// ReviewEscalateAboveLines is the reviewable diff size, in added plus
	// deleted lines, above which an EffortAuto review runs at EffortDeep.
	// Zero disables escalation; an explicit effort is never escalated.
	ReviewEscalateAboveLines int
	GitHubTokenSecret        string
	AISecret                 string
	JobTTLSeconds            int32
	ReviewTimeoutSeconds     int64
	MaxConcurrentReviews     int
	RunAsUser                int64
	ReviewCommand            string
	AllowedAssociations      map[string]bool
	WebhookSecret            string
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
	// MaxQueuedReviews bounds how many reviews may wait for a slot when
	// MaxConcurrentReviews are already running. A waiting review is a
	// suspended Job with no pod, so the bound protects the API server rather
	// than the nodes. Zero disables the queue: a delivery at capacity is
	// refused with 503 again, which GitHub does not redeliver.
	MaxQueuedReviews int
	// QueueIntervalSeconds paces the dispatcher that starts queued reviews.
	QueueIntervalSeconds int
	// QueueTimeoutSeconds drops a queued review that has waited this long.
	// Zero keeps it until a slot frees.
	QueueTimeoutSeconds int
	// SweepIntervalSeconds paces the pass that finds open pull requests whose
	// head commit Bosun never reviewed -- a webhook lost before it was stored --
	// and requests the review. Zero disables it. Needs a GitHub App.
	SweepIntervalSeconds int
	// SweepLookbackSeconds limits the sweep to pull requests updated this
	// recently, so enabling it does not review a backlog of old ones.
	SweepLookbackSeconds int
	// SweepGraceSeconds leaves a pull request updated this recently to its
	// webhook, which may still be in flight.
	SweepGraceSeconds int
	// SweepMaxPerPass bounds how many reviews one pass may request.
	SweepMaxPerPass int
}

// ReviewResources carries the reviewer Job's requests and limits as the
// quantity strings the chart renders, parsed at Job build time so a malformed
// value fails the submission it belongs to rather than controller startup.
type ReviewResources struct {
	CPURequest, MemoryRequest, CPULimit, MemoryLimit string
}

func Load() Config {
	return Config{
		Namespace: env("BOSUN_NAMESPACE", "bosun"), ReviewImage: env("BOSUN_REVIEW_IMAGE", "ghcr.io/everydaydevopsio/bosun:latest"),
		ReviewProvider: env("BOSUN_REVIEW_PROVIDER", "codex-bosun"), ReviewEffort: env("BOSUN_REVIEW_EFFORT", EffortAuto), ReviewEscalateAboveLines: envInt("BOSUN_REVIEW_ESCALATE_ABOVE_LINES", DefaultEscalateAboveLines), GitHubTokenSecret: env("BOSUN_GITHUB_TOKEN_SECRET", "bosun-github"), AISecret: env("BOSUN_AI_SECRET", "bosun-ai"),
		JobTTLSeconds: int32(envInt("BOSUN_JOB_TTL_SECONDS", 3600)), ReviewTimeoutSeconds: int64(envInt("BOSUN_REVIEW_TIMEOUT_SECONDS", 1800)),
		MaxConcurrentReviews: envInt("BOSUN_MAX_CONCURRENT_REVIEWS", 3), RunAsUser: int64(envInt("BOSUN_RUN_AS_USER", 1001)),
		ReviewBranchCreated: envBool("BOSUN_REVIEW_BRANCH_CREATED"),
		ReviewCommand:       strings.ToLower(env("BOSUN_REVIEW_COMMAND", "@bosun-review review")), AllowedAssociations: associations(env("BOSUN_ALLOWED_ASSOCIATIONS", "OWNER,MEMBER,COLLABORATOR")),
		WebhookSecret: os.Getenv("GITHUB_WEBHOOK_SECRET"),
		ReviewResources: ReviewResources{
			CPURequest:    os.Getenv("BOSUN_REVIEW_CPU_REQUEST"),
			MemoryRequest: os.Getenv("BOSUN_REVIEW_MEMORY_REQUEST"),
			CPULimit:      os.Getenv("BOSUN_REVIEW_CPU_LIMIT"),
			MemoryLimit:   os.Getenv("BOSUN_REVIEW_MEMORY_LIMIT"),
		},
		SentinelIntervalSeconds: envInt("BOSUN_SENTINEL_INTERVAL_SECONDS", 30),
		MaxQueuedReviews:        envInt("BOSUN_MAX_QUEUED_REVIEWS", 50),
		QueueIntervalSeconds:    envInt("BOSUN_QUEUE_INTERVAL_SECONDS", 10),
		QueueTimeoutSeconds:     envInt("BOSUN_QUEUE_TIMEOUT_SECONDS", 7200),
		SweepIntervalSeconds:    envInt("BOSUN_SWEEP_INTERVAL_SECONDS", 300),
		SweepLookbackSeconds:    envInt("BOSUN_SWEEP_LOOKBACK_SECONDS", 86400),
		SweepGraceSeconds:       envInt("BOSUN_SWEEP_GRACE_SECONDS", 300),
		SweepMaxPerPass:         envInt("BOSUN_SWEEP_MAX_PER_PASS", 10),
	}
}

// Review effort levels (#95). Each provider adapter maps them to its own
// control: standard passes nothing and keeps the provider's default; deep
// raises Codex's model_reasoning_effort and Claude's --effort to high. auto
// is not a provider level: the reviewer resolves it to standard or deep once
// it has sized the change (#105).
const (
	EffortAuto     = "auto"
	EffortStandard = "standard"
	EffortDeep     = "deep"
)

// DefaultEscalateAboveLines is provisional: the first 0.3.5 reviews were all
// under 500 reviewable lines, too few to calibrate from. #106 revisits it
// with the coverage numbers hosted reviews publish.
const DefaultEscalateAboveLines = 800

// NormalizeEffort returns the canonical effort level for value, treating an
// empty value as auto. An unknown level is an error rather than a silent
// fallback: a caller who asked for a deeper review must not get a shallow one
// because of a typo.
func NormalizeEffort(value string) (string, error) {
	switch level := strings.ToLower(strings.TrimSpace(value)); level {
	case "", EffortAuto:
		return EffortAuto, nil
	case EffortStandard, EffortDeep:
		return level, nil
	default:
		return "", fmt.Errorf("invalid review effort %q: use %s, %s or %s", value, EffortAuto, EffortStandard, EffortDeep)
	}
}

// ValidateReview checks the review settings every entry point must refuse
// before starting anything: the controller at start, a submission, and the
// local CLI before its snapshot.
func (c Config) ValidateReview() error {
	if _, err := NormalizeEffort(c.ReviewEffort); err != nil {
		return err
	}
	if c.ReviewEscalateAboveLines < 0 {
		return fmt.Errorf("invalid review escalate-above-lines %d: use 0 to disable escalation", c.ReviewEscalateAboveLines)
	}
	return nil
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
