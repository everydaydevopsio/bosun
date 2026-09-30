package review

import (
	"strings"
	"testing"
)

func TestPublishedOutputRedactsProviderCredentials(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "model-secret-do-not-publish")
	t.Setenv("CODEX_AUTH", `{"tokens":{"access_token":"nested-secret-do-not-publish"}}`)
	result := redact("model-secret-do-not-publish nested-secret-do-not-publish repository-token", "repository-token")
	for _, secret := range []string{"model-secret-do-not-publish", "nested-secret-do-not-publish", "repository-token"} {
		if strings.Contains(result, secret) {
			t.Fatal("secret survived result redaction")
		}
	}
}
