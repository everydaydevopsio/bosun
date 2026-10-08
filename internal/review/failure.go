package review

import (
	"errors"
	"fmt"
)

// Stage names the part of a review that failed.
//
// A failed review used to render as one line of error text, so a user could not
// tell whether their repository was wrong, their credentials were missing,
// Bosun's plumbing broke, the AI provider died, or the reviewer read the change
// and declined to review it. Those call for four different responses, and only
// one of them is the user's to act on.
type Stage string

const (
	StageSetup    Stage = "setup"    // configuration, workspace, credentials
	StageClone    Stage = "clone"    // obtaining the repository
	StageProvider Stage = "provider" // the AI session
	StageReview   Stage = "review"   // the agent ran but did not review
	StagePublish  Stage = "publish"  // the review exists but did not land
)

// exitCodes carry the stage out through the process, so a pod's terminated
// exitCode still says what happened after events and logs are gone.
var exitCodes = map[Stage]int{
	StageSetup:    10,
	StageClone:    11,
	StageProvider: 12,
	StageReview:   13,
	StagePublish:  14,
}

// sentences turn a stage into something a user can act on. They name the actor,
// because "failed" without an actor sends people to the wrong place.
var sentences = map[Stage]string{
	StageSetup:    "Bosun could not prepare the review",
	StageClone:    "Bosun could not obtain the repository",
	StageProvider: "The review provider failed",
	StageReview:   "The reviewer did not review the change",
	StagePublish:  "The review completed but could not be published",
}

// Failure is an error that knows which stage produced it.
type Failure struct {
	Stage Stage
	Err   error
}

func (f *Failure) Error() string { return fmt.Sprintf("%s: %v", f.Stage, f.Err) }

// Unwrap keeps errors.Is working through the wrapper. Without it, a timeout
// wrapped at the provider stage would stop matching context.DeadlineExceeded
// and a timed-out review would report as a plain failure.
func (f *Failure) Unwrap() error { return f.Err }

func fail(stage Stage, err error) error { return &Failure{Stage: stage, Err: err} }

func failf(stage Stage, format string, a ...any) error {
	return &Failure{Stage: stage, Err: fmt.Errorf(format, a...)}
}

// Describe renders a failure for a human: what failed, then why.
func Describe(err error) string {
	if err == nil {
		return ""
	}
	var f *Failure
	if errors.As(err, &f) {
		if sentence, ok := sentences[f.Stage]; ok {
			return sentence + ": " + f.Err.Error()
		}
	}
	// Unclassified errors reach the user unchanged rather than being given a
	// category that may be wrong.
	return err.Error()
}

// ExitCode reports the process exit code for a failure.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var f *Failure
	if errors.As(err, &f) {
		if code, ok := exitCodes[f.Stage]; ok {
			return code
		}
	}
	return 1
}
