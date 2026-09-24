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
