package review

import (
	"encoding/json"
	"strings"
)

type Request struct {
	Repo, Ref, SHA, Trigger string
	PRNumber                int
}

func FromWebhook(event string, payload json.RawMessage, command string, allowed map[string]bool) (Request, bool) {
	var p struct {
		Action     string `json:"action"`
		RefType    string `json:"ref_type"`
		Ref        string `json:"ref"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		PullRequest struct {
			Number int `json:"number"`
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
	if event == "create" && p.RefType == "branch" {
		return Request{Repo: p.Repository.FullName, Ref: p.Ref, Trigger: "branch-created"}, true
	}
	if event == "pull_request" && (p.Action == "opened" || p.Action == "reopened" || p.Action == "synchronize" || p.Action == "ready_for_review") {
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
