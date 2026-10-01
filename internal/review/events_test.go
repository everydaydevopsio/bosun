package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/progress"
)

func TestWorkerEventsKeepReviewSeparateAndPreserveTimeout(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{true: "timeout", false: "success"}[fail], func(t *testing.T) {
			repo := t.TempDir()
			os.Mkdir(filepath.Join(repo, ".git"), 0700)
			prompt := filepath.Join(t.TempDir(), "prompt")
			os.WriteFile(prompt, []byte("review"), 0600)
			t.Setenv("BOSUN_EVENTS", "1")
			t.Setenv("BOSUN_RUN_ID", "run")
			t.Setenv("BOSUN_LOCAL_PATH", repo)
			t.Setenv("BOSUN_REPO", "repo")
			t.Setenv("BOSUN_REF", "branch")
			t.Setenv("CODEX_AUTH", "")
			t.Setenv("CLAUDE_CREDENTIALS", "")
			var events, out bytes.Buffer
			w := Worker{PromptPath: prompt, Events: &events, Output: &out, Agent: func(context.Context, string, string, string, time.Duration) (string, error) {
				if fail {
					return "", context.DeadlineExceeded
				}
				return "findings", nil
			}}
			err := w.Run(context.Background(), config.Config{ReviewTimeoutSeconds: 1})
			if fail && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("timeout identity lost: %v", err)
			}
			if out.Len() != 0 {
				t.Fatal("raw output mixed with event protocol")
			}
			decoder := json.NewDecoder(&events)
			var got []progress.Event
			for decoder.More() {
				var e progress.Event
				if err := decoder.Decode(&e); err != nil {
					t.Fatal(err)
				}
				got = append(got, e)
			}
			final := got[len(got)-1]
			if fail {
				if final.Type != "timed_out" {
					t.Fatal(final)
				}
			} else {
				if final.Type != "completed" || got[len(got)-2].Message != "findings" {
					t.Fatal(got)
				}
			}
		})
	}
}
func TestDiagnosticsRedactMaterializedAuth(t *testing.T) {
	t.Setenv("CODEX_AUTH", `{"tokens":{"access_token":"private-access-token"}}`)
	sanitize := diagnosticRedactor()
	os.Unsetenv("CODEX_AUTH")
	if s := sanitize("failed using private-access-token"); strings.Contains(s, "private-access-token") {
		t.Fatal("auth token leaked")
	}
}
