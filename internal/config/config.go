package config

import (
	"os"
	"strconv"
	"strings"
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
		ReviewProvider: env("BOSUN_REVIEW_PROVIDER", "codex-bosun"), GitHubTokenSecret: env("BOSUN_GITHUB_TOKEN_SECRET", "bosun-github"), AISecret: env("BOSUN_AI_SECRET", "bosun-ai"),
		JobTTLSeconds: int32(envInt("BOSUN_JOB_TTL_SECONDS", 3600)), ReviewTimeoutSeconds: int64(envInt("BOSUN_REVIEW_TIMEOUT_SECONDS", 1800)),
		MaxConcurrentReviews: envInt("BOSUN_MAX_CONCURRENT_REVIEWS", 3), RunAsUser: int64(envInt("BOSUN_RUN_AS_USER", 1001)),
		ReviewCommand: strings.ToLower(env("BOSUN_REVIEW_COMMAND", "@bridgectl review")), AllowedAssociations: associations(env("BOSUN_ALLOWED_ASSOCIATIONS", "OWNER,MEMBER,COLLABORATOR")),
		WebhookSecret: os.Getenv("GITHUB_WEBHOOK_SECRET"),
		ReviewResources: ReviewResources{
			CPURequest:    os.Getenv("BOSUN_REVIEW_CPU_REQUEST"),
			MemoryRequest: os.Getenv("BOSUN_REVIEW_MEMORY_REQUEST"),
			CPULimit:      os.Getenv("BOSUN_REVIEW_CPU_LIMIT"),
			MemoryLimit:   os.Getenv("BOSUN_REVIEW_MEMORY_LIMIT"),
		},
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
