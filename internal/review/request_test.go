package review

import "testing"

func TestBranchCreateLeavesSHAEmpty(t *testing.T) {
	r, ok := FromWebhook("create", []byte(`{"repository":{"full_name":"acme/widget"},"ref_type":"branch","ref":"feature/x"}`), "@bridgectl review", nil)
	if !ok || r.SHA != "" || r.Ref != "feature/x" {
		t.Fatalf("unexpected request: %#v, %v", r, ok)
	}
}
func TestAuthorizedComment(t *testing.T) {
	r, ok := FromWebhook("issue_comment", []byte(`{"action":"created","repository":{"full_name":"acme/widget"},"comment":{"body":"@bridgectl review","author_association":"MEMBER"},"issue":{"number":42,"pull_request":{}}}`), "@bridgectl review", map[string]bool{"MEMBER": true})
	if !ok || r.PRNumber != 42 || r.Trigger != "comment" {
		t.Fatalf("unexpected request: %#v, %v", r, ok)
	}
}
func TestUnauthorizedCommentIgnored(t *testing.T) {
	_, ok := FromWebhook("issue_comment", []byte(`{"action":"created","repository":{"full_name":"acme/widget"},"comment":{"body":"@bridgectl review","author_association":"NONE"},"issue":{"number":42,"pull_request":{}}}`), "@bridgectl review", map[string]bool{"MEMBER": true})
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
		if _, ok := FromWebhook("pull_request", payload, "@bridgectl review", nil); ok {
			t.Fatalf("draft pull request accepted a %s review", action)
		}
	}
}
func TestReadyPullRequestIsReviewed(t *testing.T) {
	for _, action := range []string{"opened", "reopened", "synchronize", "ready_for_review"} {
		payload := []byte(`{"action":"` + action + `","repository":{"full_name":"acme/widget"},"pull_request":{"number":7,"draft":false,"head":{"ref":"feature/x","sha":"abc123"}}}`)
		r, ok := FromWebhook("pull_request", payload, "@bridgectl review", nil)
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
	r, ok := FromWebhook("pull_request", payload, "@bridgectl review", nil)
	if !ok || r.Trigger != "pull-request-ready_for_review" {
		t.Fatalf("ready_for_review suppressed by draft flag: %#v, %v", r, ok)
	}
}

// An explicit command is a human asking for the review, so it still runs on a
// draft. Only the automatic triggers are gated.
func TestCommentReviewsDraftPullRequest(t *testing.T) {
	payload := []byte(`{"action":"created","repository":{"full_name":"acme/widget"},"comment":{"body":"@bridgectl review","author_association":"MEMBER"},"issue":{"number":42,"pull_request":{}},"pull_request":{"draft":true}}`)
	r, ok := FromWebhook("issue_comment", payload, "@bridgectl review", map[string]bool{"MEMBER": true})
	if !ok || r.PRNumber != 42 || r.Trigger != "comment" {
		t.Fatalf("comment on a draft was ignored: %#v, %v", r, ok)
	}
}
