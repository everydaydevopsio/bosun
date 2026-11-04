package review

import (
	"encoding/json"
	"strings"
)

type Request struct {
	Repo, Ref, SHA, Trigger string
	PRNumber                int
	// TriggerRef is the ref the trigger carried, kept when Ref is replaced by
	// a resolved one. A Job's name is the deduplication key for a webhook
	// delivery, and it is derived from the ref: if resolution fails on one
	// delivery and succeeds on its redelivery, a changing ref turns one
	// delivery into two Jobs, two reviews, and two publications. Empty when
	// nothing was resolved.
	TriggerRef string
}

// NameRef is the ref a Job is named after: stable across resolution, so one
// webhook delivery always maps to one Job.
func (r Request) NameRef() string {
	if r.TriggerRef != "" {
		return r.TriggerRef
	}
	return r.Ref
}

// FromWebhook turns a delivery into a review request, or reports that the
// event does not ask for one.
//
// branchCreated enables the `create` trigger. It is off by default: a branch
// push and the pull request opened from it review the same commit, so the
// ordinary flow paid for two identical reviews. maxConcurrent is counted
// across the namespace rather than per repository, so the duplicate could take
// the last slot and leave the pull request opened moments later rejected with
// 503 -- which GitHub does not redeliver, losing the review that was actually
// wanted. The pull-request triggers below already cover that flow.
func FromWebhook(event string, payload json.RawMessage, command string, allowed map[string]bool, branchCreated bool) (Request, bool) {
	var p struct {
		Action     string `json:"action"`
		RefType    string `json:"ref_type"`
		Ref        string `json:"ref"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		PullRequest struct {
			Number int  `json:"number"`
			Draft  bool `json:"draft"`
			Head   struct {
				Ref string `json:"ref"`
				SHA string `json:"sha"`
			} `json:"head"`
		} `json:"pull_request"`
		Issue struct {
			Number      int             `json:"number"`
			PullRequest json.RawMessage `json:"pull_request"`
		} `json:"issue"`
		Comment struct {
			Body              string `json:"body"`
			AuthorAssociation string `json:"author_association"`
		} `json:"comment"`
		Sender struct {
			Type string `json:"type"`
		} `json:"sender"`
	}
	if json.Unmarshal(payload, &p) != nil || p.Repository.FullName == "" {
		return Request{}, false
	}
	if event == "create" && p.RefType == "branch" && branchCreated {
		return Request{Repo: p.Repository.FullName, Ref: p.Ref, Trigger: "branch-created"}, true
	}
	if event == "pull_request" && (p.Action == "opened" || p.Action == "reopened" || p.Action == "synchronize" || p.Action == "ready_for_review") {
		// A draft is work the author has not offered up yet, so reviewing it
		// burns a run on every push and then burns another once they mark it
		// ready. ready_for_review is the moment the PR stops being a draft, so
		// it reviews regardless of what the payload's flag still says. An
		// explicit review comment is handled below and still works on drafts:
		// only the automatic triggers are gated.
		if p.PullRequest.Draft && p.Action != "ready_for_review" {
			return Request{}, false
		}
		return Request{Repo: p.Repository.FullName, Ref: p.PullRequest.Head.Ref, SHA: p.PullRequest.Head.SHA, PRNumber: p.PullRequest.Number, Trigger: "pull-request-" + p.Action}, true
	}
	if (event == "issue_comment" || event == "pull_request_review_comment") && p.Action == "created" {
		if p.Sender.Type == "Bot" || !strings.Contains(strings.ToLower(p.Comment.Body), command) || !allowed[strings.ToUpper(p.Comment.AuthorAssociation)] {
			return Request{}, false
		}
		pr := p.Issue.Number
		if event == "issue_comment" && len(p.Issue.PullRequest) == 0 {
			return Request{}, false
		}
		if pr == 0 {
			pr = p.PullRequest.Number
		}
		if pr == 0 {
			return Request{}, false
		}
		return Request{Repo: p.Repository.FullName, Ref: "pr-" + itoa(pr), PRNumber: pr, Trigger: "comment"}, true
	}
	return Request{}, false
}
func itoa(n int) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = digits[n%10]
		n /= 10
	}
	return string(b[i:])
}
