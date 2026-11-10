// Package sweep finds open pull requests whose head commit Bosun never
// reviewed, and requests the review.
//
// Every other path into a review starts with a webhook delivery, and a
// delivery can be lost before it is stored: refused while no controller is
// ready, cut off mid-rollout, or never sent. GitHub does not redeliver a failed
// App webhook, so nothing downstream can notice the gap -- the pull request
// simply has no review and no reason why (#82). The termination sentinel
// covers a review that started and died; this covers one that never started.
//
// It decides from GitHub, not from local state. "Has Bosun published for this
// head commit?" is answered by the publications themselves, which outlive any
// Job and any controller restart.
package sweep

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/jobs"
	"github.com/everydaydevopsio/bosun/internal/review"
	"k8s.io/client-go/kubernetes"
)

// GitHub is the read-only view of GitHub the sweep needs. review.Scanner
// implements it.
type GitHub interface {
	Installations(ctx context.Context) ([]review.Installation, error)
	Token(ctx context.Context, inst review.Installation) (string, error)
	Repositories(ctx context.Context, token string) ([]string, error)
	OpenPullRequests(ctx context.Context, token, repo string, since time.Time) ([]review.OpenPullRequest, error)
	Reviewed(ctx context.Context, token, repo string, number int, sha string) (bool, error)
}

// Trigger names sweep-started reviews in Jobs and logs.
const Trigger = "sweep"

// Sweeper requests reviews for pull requests whose head was never reviewed.
type Sweeper struct {
	Client kubernetes.Interface
	Config config.Config
	GitHub GitHub
	// Submit is the webhook's own submission path.
	Submit func(ctx context.Context, req review.Request, delivery string) (string, error)
	// Lock serializes a pass across controller replicas. Nil runs directly.
	Lock func(context.Context, func(context.Context) error) error
	Now  func() time.Time

	mu        sync.Mutex
	attempted map[string]bool
}

// Run sweeps until the context is cancelled.
func (s *Sweeper) Run(ctx context.Context) {
	interval := time.Duration(s.Config.SweepIntervalSeconds) * time.Second
	slog.Info("missed-review sweep started", "logger", "bosun.sweep", "interval", interval.String(),
		"lookback_seconds", s.Config.SweepLookbackSeconds, "grace_seconds", s.Config.SweepGraceSeconds)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.pass(ctx); err != nil && ctx.Err() == nil {
				slog.Error("missed-review sweep failed", "logger", "bosun.sweep", "error", err)
			}
		}
	}
}

func (s *Sweeper) pass(ctx context.Context) error {
	if s.Lock == nil {
		return s.Sweep(ctx)
	}
	return s.Lock(ctx, s.Sweep)
}

// Sweep makes one pass over every installation. It keeps going past a
// repository it cannot read, and reports the first failure at the end.
func (s *Sweeper) Sweep(ctx context.Context) error {
	installations, err := s.GitHub.Installations(ctx)
	if err != nil {
		return fmt.Errorf("list installations: %w", err)
	}
	var first error
	submitted := 0
	for _, inst := range installations {
		token, err := s.GitHub.Token(ctx, inst)
		if err != nil {
			first = keep(first, fmt.Errorf("token for installation %d: %w", inst.ID, err))
			continue
		}
		repos, err := s.GitHub.Repositories(ctx, token)
		if err != nil {
			first = keep(first, fmt.Errorf("repositories for installation %d: %w", inst.ID, err))
			continue
		}
		for _, repo := range repos {
			n, err := s.sweepRepository(ctx, token, repo, s.Config.SweepMaxPerPass-submitted)
			submitted += n
			if err != nil {
				first = keep(first, fmt.Errorf("%s: %w", repo, err))
			}
			if submitted >= s.Config.SweepMaxPerPass {
				slog.Warn("missed-review sweep hit its per-pass limit; the rest wait for the next pass", "logger", "bosun.sweep",
					"limit", s.Config.SweepMaxPerPass)
				return first
			}
		}
	}
	return first
}

func keep(first, next error) error {
	if first != nil {
		return first
	}
	return next
}

func (s *Sweeper) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Sweeper) sweepRepository(ctx context.Context, token, repo string, budget int) (int, error) {
	lookback := time.Duration(s.Config.SweepLookbackSeconds) * time.Second
	grace := time.Duration(s.Config.SweepGraceSeconds) * time.Second
	now := s.now()
	var failed error
	var since time.Time
	if lookback > 0 {
		since = now.Add(-lookback)
	}
	prs, err := s.GitHub.OpenPullRequests(ctx, token, repo, since)
	if err != nil {
		return 0, err
	}
	submitted := 0
	for _, pr := range prs {
		if submitted >= budget {
			break
		}
		// Sorted most recently updated first, so the first one outside the
		// window ends the repository.
		if lookback > 0 && now.Sub(pr.UpdatedAt) > lookback {
			break
		}
		// Drafts are not reviewed automatically; see review.FromWebhook.
		if pr.Draft || pr.SHA == "" {
			continue
		}
		// The webhook for a change this recent may still be arriving or
		// waiting for admission. Reviewing it here would race it.
		if now.Sub(pr.UpdatedAt) < grace {
			continue
		}
		// Per pull request, not per commit: one branch proposed against two
		// bases is two pull requests, each owed its own review.
		key := fmt.Sprintf("%s#%d@%s", repo, pr.Number, pr.SHA)
		if s.tried(key) {
			continue
		}
		pending, err := s.pending(ctx, repo, pr, now, grace)
		if err != nil {
			return submitted, err
		}
		if pending {
			continue
		}
		reviewed, err := s.GitHub.Reviewed(ctx, token, repo, pr.Number, pr.SHA)
		if err != nil {
			// Unknown is not "unreviewed": skip this one rather than risk a
			// duplicate, but keep going. Stopping here hid every later pull
			// request in the repository on every pass.
			failed = keep(failed, fmt.Errorf("pull request %d: %w", pr.Number, err))
			continue
		}
		if reviewed {
			continue
		}
		// Dated when the head was read, before listing: a webhook for a newer
		// head received while this pass was checking publications must still
		// order after it, or that newer review would be dropped as stale.
		req := review.Request{Repo: repo, Ref: pr.Ref, SHA: pr.SHA, PRNumber: pr.Number, Trigger: Trigger, ReceivedAt: now}
		// The delivery is derived from the pull request and commit, so the
		// Job name is too: a second pass, or a second replica, finds the Job
		// and stops.
		name, err := s.Submit(ctx, req, fmt.Sprintf("sweep-%d-%s", pr.Number, pr.SHA))
		if _, dup := err.(interface{ Duplicate() }); dup {
			s.mark(key)
			continue
		}
		if err != nil {
			return submitted, fmt.Errorf("pull request %d: %w", pr.Number, err)
		}
		// Once per commit per controller: a review whose publication keeps
		// failing must not be paid for again on every pass.
		s.mark(key)
		submitted++
		slog.Warn("requested a review the webhook never delivered", "logger", "bosun.sweep",
			"repo", repo, "pr", pr.Number, "sha", pr.SHA, "job", name)
	}
	return submitted, failed
}

// pending reports whether a Job already covers this head, or a Job for the pull
// request is new enough that its webhook path still owns the outcome.
func (s *Sweeper) pending(ctx context.Context, repo string, pr review.OpenPullRequest, now time.Time, grace time.Duration) (bool, error) {
	existing, err := jobs.ForPullRequest(ctx, s.Client, s.Config.Namespace, repo, pr.Number)
	if err != nil {
		return false, fmt.Errorf("list jobs for pull request %d: %w", pr.Number, err)
	}
	for i := range existing {
		j := &existing[i]
		if jobs.ReviewedSHA(j) == pr.SHA {
			return true, nil
		}
		if now.Sub(j.CreationTimestamp.Time) < grace {
			return true, nil
		}
	}
	return false, nil
}

func (s *Sweeper) tried(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attempted[key]
}

func (s *Sweeper) mark(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attempted == nil {
		s.attempted = map[string]bool{}
	}
	s.attempted[key] = true
}
