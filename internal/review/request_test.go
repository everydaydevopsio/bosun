package review

import "testing"

func TestBranchCreateLeavesSHAEmpty(t *testing.T) {
	r, ok := FromWebhook("create", []byte(`{"repository":{"full_name":"acme/widget"},"ref_type":"branch","ref":"feature/x"}`), "@bosun-review review", nil, true)
	if !ok || r.SHA != "" || r.Ref != "feature/x" {
		t.Fatalf("unexpected request: %#v, %v", r, ok)
	}
}
func TestAuthorizedComment(t *testing.T) {
	r, ok := FromWebhook("issue_comment", []byte(`{"action":"created","repository":{"full_name":"acme/widget"},"comment":{"body":"@bosun-review review","author_association":"MEMBER"},"issue":{"number":42,"pull_request":{}}}`), "@bosun-review review", map[string]bool{"MEMBER": true}, false)
	if !ok || r.PRNumber != 42 || r.Trigger != "comment" {
		t.Fatalf("unexpected request: %#v, %v", r, ok)
	}
}
func TestUnauthorizedCommentIgnored(t *testing.T) {
	_, ok := FromWebhook("issue_comment", []byte(`{"action":"created","repository":{"full_name":"acme/widget"},"comment":{"body":"@bosun-review review","author_association":"NONE"},"issue":{"number":42,"pull_request":{}}}`), "@bosun-review review", map[string]bool{"MEMBER": true}, false)
	if ok {
		t.Fatal("unauthorized command was accepted")
	}
}

// Issue #66: a draft pull request is work in progress. Automatic triggers must
// skip it, so the review happens once the author marks it ready instead of on
// every push while they are still writing.
func TestDraftPullRequestIsNotReviewed(t *testing.T) {
	for _, action := range []string{"opened", "reopened", "synchronize"} {
		payload := []byte(`{"action":"` + action + `","repository":{"full_name":"acme/widget"},"pull_request":{"number":7,"draft":true,"head":{"ref":"feature/x","sha":"abc123"}}}`)
		if _, ok := FromWebhook("pull_request", payload, "@bosun-review review", nil, false); ok {
			t.Fatalf("draft pull request accepted a %s review", action)
		}
	}
}
func TestReadyPullRequestIsReviewed(t *testing.T) {
	for _, action := range []string{"opened", "reopened", "synchronize", "ready_for_review"} {
		payload := []byte(`{"action":"` + action + `","repository":{"full_name":"acme/widget"},"pull_request":{"number":7,"draft":false,"head":{"ref":"feature/x","sha":"abc123"}}}`)
		r, ok := FromWebhook("pull_request", payload, "@bosun-review review", nil, false)
		if !ok || r.PRNumber != 7 || r.Trigger != "pull-request-"+action {
			t.Fatalf("ready pull request rejected a %s review: %#v, %v", action, r, ok)
		}
	}
}

// ready_for_review always carries draft:false from GitHub, but the gate must
// not depend on that: the action itself means the PR just stopped being a
// draft, so a stale flag in the payload cannot suppress the review.
func TestReadyForReviewSurvivesStaleDraftFlag(t *testing.T) {
	payload := []byte(`{"action":"ready_for_review","repository":{"full_name":"acme/widget"},"pull_request":{"number":7,"draft":true,"head":{"ref":"feature/x","sha":"abc123"}}}`)
	r, ok := FromWebhook("pull_request", payload, "@bosun-review review", nil, false)
	if !ok || r.Trigger != "pull-request-ready_for_review" {
		t.Fatalf("ready_for_review suppressed by draft flag: %#v, %v", r, ok)
	}
}

// An explicit command is a human asking for the review, so it still runs on a
// draft. Only the automatic triggers are gated.
func TestCommentReviewsDraftPullRequest(t *testing.T) {
	payload := []byte(`{"action":"created","repository":{"full_name":"acme/widget"},"comment":{"body":"@bosun-review review","author_association":"MEMBER"},"issue":{"number":42,"pull_request":{}},"pull_request":{"draft":true}}`)
	r, ok := FromWebhook("issue_comment", payload, "@bosun-review review", map[string]bool{"MEMBER": true}, false)
	if !ok || r.PRNumber != 42 || r.Trigger != "comment" {
		t.Fatalf("comment on a draft was ignored: %#v, %v", r, ok)
	}
}

// A branch push and the pull request opened from it review the same commit.
// Both ran, so the ordinary `git push -u` then `gh pr create` flow paid for two
// identical reviews -- and because maxConcurrent is counted across the whole
// namespace, the duplicate could take the last slot and leave the pull request
// opened moments later rejected with 503, which GitHub never redelivers. The
// pull-request triggers already cover that flow, so branch review is off until
// an operator asks for it.
func TestBranchCreationIsNotReviewedUnlessEnabled(t *testing.T) {
	const payload = `{"repository":{"full_name":"acme/widget"},"ref_type":"branch","ref":"feature/x"}`
	if _, ok := FromWebhook("create", []byte(payload), "@bosun-review review", nil, false); ok {
		t.Fatal("a branch push started a review while branch review was disabled")
	}
	r, ok := FromWebhook("create", []byte(payload), "@bosun-review review", nil, true)
	if !ok {
		t.Fatal("branch review is enabled but the branch push did not request one")
	}
	if r.Trigger != "branch-created" || r.Ref != "feature/x" {
		t.Fatalf("unexpected request: %+v", r)
	}
}

// Disabling branch review must not touch the triggers that carry the ordinary
// workflow: a pull request opened, and every push to it afterwards.
func TestPullRequestTriggersAreUnaffectedByTheBranchSetting(t *testing.T) {
	for _, action := range []string{"opened", "reopened", "synchronize", "ready_for_review"} {
		payload := []byte(`{"action":"` + action + `","repository":{"full_name":"acme/widget"},"pull_request":{"number":7,"head":{"ref":"feature","sha":"0123456789abcdef0123456789abcdef01234567"}}}`)
		r, ok := FromWebhook("pull_request", payload, "@bosun-review review", nil, false)
		if !ok {
			t.Fatalf("%s did not request a review with branch review disabled", action)
		}
		if r.PRNumber != 7 || r.Trigger != "pull-request-"+action {
			t.Fatalf("%s produced %+v", action, r)
		}
	}
}

// #96: a pull request names its own base. Without it the reviewer compares
// against the default branch, which is the wrong diff for a PR into a release
// branch.
func TestPullRequestCarriesItsBase(t *testing.T) {
	const base = "1111111111111111111111111111111111111111"
	payload := []byte(`{"action":"synchronize","repository":{"full_name":"acme/widget"},"pull_request":{"number":7,"head":{"ref":"feature/x","sha":"abc123"},"base":{"ref":"release/1.x","sha":"` + base + `"}}}`)
	r, ok := FromWebhook("pull_request", payload, "@bosun-review review", nil, false)
	if !ok || r.BaseSHA != base {
		t.Fatalf("base not carried: %#v, %v", r, ok)
	}
	bad := []byte(`{"action":"synchronize","repository":{"full_name":"acme/widget"},"pull_request":{"number":7,"head":{"ref":"feature/x","sha":"abc123"},"base":{"sha":"--upload-pack=x"}}}`)
	if r, _ := FromWebhook("pull_request", bad, "@bosun-review review", nil, false); r.BaseSHA != "" {
		t.Fatalf("accepted an invalid base: %q", r.BaseSHA)
	}
}
