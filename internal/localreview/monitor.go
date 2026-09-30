package localreview

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/everydaydevopsio/bosun/internal/progress"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func outcomeCode(outcome string) int {
	switch outcome {
	case "completed":
		return 0
	case "timed_out":
		return 124
	case "cancelled":
		return 130
	default:
		return 1
	}
}
func render(e progress.Event, jsonMode bool, out, errout io.Writer) {
	if jsonMode {
		_ = json.NewEncoder(out).Encode(e)
		return
	}
	if e.Type == "result" {
		fmt.Fprintln(out, e.Message)
		return
	}
	elapsed := time.Duration(e.Elapsed) * time.Second
	if e.Type == "heartbeat" {
		activity := "No provider activity received yet"
		if e.HasActivity {
			activity = "last provider activity " + (time.Duration(e.Quiet) * time.Second).String() + " ago"
		}
		quietWarning := 2 * time.Minute
		if d, err := time.ParseDuration(os.Getenv("BOSUN_QUIET_WARNING_AFTER")); err == nil && d > 0 {
			quietWarning = d
		}
		if time.Duration(e.Quiet)*time.Second >= quietWarning {
			activity += "; quiet session, progress unconfirmed"
		}
		fmt.Fprintf(errout, "[%s] %s | stage %s | %s\n", elapsed, e.Stage, time.Duration(e.StageElapsed)*time.Second, activity)
		return
	}
	fmt.Fprintf(errout, "[%s] %s: %s\n", elapsed, e.Type, e.Message)
}
func cleanSnapshot(r *record) error {
	if filepath.Dir(r.Snapshot) != "/tmp/bosun-repos" || !strings.HasPrefix(filepath.Base(r.Snapshot), "review.") {
		return fmt.Errorf("refusing invalid snapshot cleanup path")
	}
	if err := os.RemoveAll(r.Snapshot); err != nil {
		return err
	}
	if err := os.Remove(r.Snapshot + ".events"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
func monitor(ctx context.Context, c kubernetes.Interface, r *record, o options, out, errout io.Writer) int {
	if r.Outcome != "" && !r.Finished.IsZero() {
		if r.Result != "" {
			render(progress.Event{Version: 1, Run: r.Job + "/stored", Seq: 1, Time: r.Finished, Type: "result", Message: r.Result}, o.json, out, errout)
		}
		render(progress.Event{Version: 1, Run: r.Job + "/stored", Seq: 2, Time: r.Finished, Type: r.Outcome, Message: "Stored review result", Elapsed: r.Finished.Sub(r.Started).Seconds()}, o.json, out, errout)
		return outcomeCode(r.Outcome)
	}
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	var localSeq uint64
	message := func(kind, s string) {
		localSeq++
		render(progress.Event{Version: 1, Run: r.Job + "/monitor", Seq: localSeq, Time: time.Now(), Type: kind, Elapsed: time.Since(r.Started).Seconds(), Message: s}, o.json, out, errout)
	}
	message("status", "Following "+r.Job+"; reconnect with bosun review-status "+r.Job+" --follow")
	// Resume may replay the complete output for a new terminal. Persistent cursor
	// still prevents rewriting the stored result more than once per event.
	seq := uint64(0)
	lastStatus := ""
	lastHeartbeat := time.Time{}
	var since *metav1.Time
	warnings := map[string]string{}
	diagnostics := map[string]bool{}
	accept := func(event progress.Event) {
		if event.Version != 1 || event.Run != r.Job || event.Seq <= seq {
			return
		}
		seq = event.Seq
		event.Elapsed = event.Time.Sub(r.Started).Seconds()
		if event.Elapsed < 0 {
			event.Elapsed = 0
		}
		render(event, o.json, out, errout)
		if event.Type == "result" {
			r.Result = event.Message
		}
		if event.Type == "completed" || event.Type == "failed" || event.Type == "timed_out" || event.Type == "cancelled" {
			r.Outcome = event.Type
			r.WorkerFinished = event.Time
		}
		r.LastSeq = seq
	}
	for {
		// Durable worker events survive detached runs and Kubernetes log expiry.
		if f, e := os.Open(r.Snapshot + ".events"); e == nil {
			scanner := bufio.NewScanner(f)
			scanner.Buffer(make([]byte, 4096), 16*1024*1024)
			for scanner.Scan() {
				var event progress.Event
				if json.Unmarshal(scanner.Bytes(), &event) == nil {
					accept(event)
				}
			}
			f.Close()
		}
		checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		job, err := c.BatchV1().Jobs(r.Namespace).Get(checkCtx, r.Job, metav1.GetOptions{})
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				if o.owner {
					return cancelReview(c, r, message)
				}
				message("status", "Stopped following; review remains running")
				return 130
			}
			if apierrors.IsNotFound(err) {
				if r.Outcome != "" && (r.Outcome != "completed" || r.Result != "") {
					r.Finished = r.WorkerFinished
					if r.Finished.IsZero() {
						r.Finished = time.Now()
					}
					if e := save(r); e != nil {
						message("warning", e.Error())
					}
					message(r.Outcome, "Recovered durable worker result after job removal")
					return outcomeCode(r.Outcome)
				}
				message("failed", "Job no longer exists; no terminal result was saved. Snapshot retained: "+r.Snapshot)
				return 1
			}
			message("warning", "Kubernetes connection unavailable: "+err.Error())
			if !o.follow {
				return 1
			}
		} else {
			checkCtx, cancel = context.WithTimeout(ctx, 10*time.Second)
			pods, e := c.CoreV1().Pods(r.Namespace).List(checkCtx, metav1.ListOptions{LabelSelector: "job-name=" + r.Job})
			cancel()
			if e != nil {
				message("warning", "Cannot inspect reviewer pod: "+e.Error())
			} else {
				for _, pod := range pods.Items {
					state := string(pod.Status.Phase)
					for _, condition := range pod.Status.Conditions {
						if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse {
							state += " / " + condition.Reason + ": " + condition.Message
						}
					}
					if pod.Status.Phase == corev1.PodPending {
						eventCtx, stop := context.WithTimeout(ctx, 3*time.Second)
						events, eventErr := c.CoreV1().Events(r.Namespace).List(eventCtx, metav1.ListOptions{FieldSelector: "involvedObject.uid=" + string(pod.UID) + ",type=Warning"})
						stop()
						if eventErr == nil {
							for _, event := range events.Items {
								key := event.Reason + "/" + string(pod.UID)
								if warnings[key] != event.Message {
									message("warning", event.Reason+": "+event.Message)
									warnings[key] = event.Message
								}
							}
						}
					}
					for _, s := range pod.Status.ContainerStatuses {
						if s.State.Waiting != nil {
							state += " / " + s.State.Waiting.Reason + ": " + s.State.Waiting.Message
						}
						if s.State.Terminated != nil {
							state += fmt.Sprintf(" / %s (exit %d): %s", s.State.Terminated.Reason, s.State.Terminated.ExitCode, s.State.Terminated.Message)
						}
					}
					if state != lastStatus {
						message("pod", state)
						lastStatus = state
					}
					if pod.Status.Phase != corev1.PodPending {
						logCtx, stop := context.WithTimeout(ctx, 5*time.Second)
						stream, e := c.CoreV1().Pods(r.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{Container: "reviewer", SinceTime: since, Timestamps: true}).Stream(logCtx)
						if e == nil {
							scanner := bufio.NewScanner(stream)
							scanner.Buffer(make([]byte, 4096), 16*1024*1024)
							var latest time.Time
							for scanner.Scan() {
								line := scanner.Text()
								ts, body, ok := strings.Cut(line, " ")
								if !ok {
									body = line
								} else if t, e := time.Parse(time.RFC3339Nano, ts); e == nil {
									latest = t
								} else {
									body = line
								}
								var event progress.Event
								if json.Unmarshal([]byte(body), &event) != nil || event.Version != 1 || event.Run != r.Job {
									if !diagnostics[body] && len(diagnostics) < 100 {
										diagnostics[body] = true
										if len(body) > 2048 {
											body = body[:2048] + "…"
										}
										message("diagnostic", body)
									}
									continue
								}
								accept(event)
							}
							scanErr := scanner.Err()
							stream.Close()
							if scanErr != nil {
								message("warning", "Log connection interrupted; retrying: "+scanErr.Error())
							} else if !latest.IsZero() {
								t := metav1.NewTime(latest.Add(-time.Second))
								since = &t
							}
						} else {
							message("warning", "Reviewer logs unavailable: "+e.Error())
						}
						stop()
					}
				}
				terminal := false
				failed := false
				reason := ""
				for _, condition := range job.Status.Conditions {
					if condition.Status == corev1.ConditionTrue && (condition.Type == batchv1.JobComplete || condition.Type == batchv1.JobFailed) {
						terminal = true
						failed = condition.Type == batchv1.JobFailed
						reason = condition.Reason + ": " + condition.Message
					}
				}
				if terminal {
					if failed {
						if r.Outcome != "timed_out" && r.Outcome != "cancelled" {
							r.Outcome = "failed"
						}
						if strings.Contains(reason, "DeadlineExceeded") {
							r.Outcome = "timed_out"
						}
						message(r.Outcome, reason+"; inspect: kubectl --context "+r.Context+" -n "+r.Namespace+" logs job/"+r.Job)
					} else if r.Outcome != "completed" || r.Result == "" {
						r.Outcome = "failed"
						message("failed", "Job exited without a complete structured review result")
					}
					r.Finished = time.Now()
					if job.Status.CompletionTime != nil {
						r.Finished = job.Status.CompletionTime.Time
					}
					saved := true
					if e := save(r); e != nil {
						message("warning", "Cannot save result; event journal retained: "+e.Error())
						saved = false
					}
					stopped := saved
					for _, p := range pods.Items {
						if p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed {
							stopped = false
						}
					}
					if stopped {
						if e := cleanSnapshot(r); e != nil {
							message("warning", e.Error())
						}
					}
					return outcomeCode(r.Outcome)
				}
			}
			if time.Since(lastHeartbeat) >= 10*time.Second {
				remaining := ""
				if job.Spec.ActiveDeadlineSeconds != nil && job.Status.StartTime != nil {
					left := time.Until(job.Status.StartTime.Add(time.Duration(*job.Spec.ActiveDeadlineSeconds) * time.Second))
					if left < 0 {
						left = 0
					}
					remaining = "; job deadline in " + left.Round(time.Second).String()
				}
				if r.TypicalHigh > 0 {
					elapsed := time.Since(r.Started)
					if elapsed > r.TypicalHigh {
						remaining += "; longer than the typical duration"
					} else {
						low := r.TypicalLow - elapsed
						if low < 0 {
							low = 0
						}
						remaining += "; estimated remaining " + low.Round(time.Second).String() + "–" + (r.TypicalHigh - elapsed).Round(time.Second).String()
					}
				}
				message("status", "Review pending/running"+remaining)
				lastHeartbeat = time.Now()
			}
			if e := save(r); e != nil {
				message("warning", "Cannot persist status: "+e.Error())
			}
			if !o.follow {
				return 0
			}
		}
		select {
		case <-ctx.Done():
			if o.owner {
				return cancelReview(c, r, message)
			}
			message("status", "Stopped following; review remains running")
			return 130
		case <-ticker.C:
		}
	}
}
func cancelReview(c kubernetes.Interface, r *record, message func(string, string)) int {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	policy := metav1.DeletePropagationForeground
	err := c.BatchV1().Jobs(r.Namespace).Delete(ctx, r.Job, metav1.DeleteOptions{PropagationPolicy: &policy})
	if err != nil && !apierrors.IsNotFound(err) {
		message("warning", "Cancellation unconfirmed; retain snapshot and inspect job: "+err.Error())
		return 130
	}
	for {
		pods, e := c.CoreV1().Pods(r.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "job-name=" + r.Job})
		if e == nil && len(pods.Items) == 0 {
			_ = cleanSnapshot(r)
			r.Outcome = "cancelled"
			r.Finished = time.Now()
			_ = save(r)
			message("cancelled", "Reviewer stopped; snapshot removed")
			return 130
		}
		select {
		case <-ctx.Done():
			message("warning", "Shutdown unconfirmed; snapshot retained at "+r.Snapshot)
			return 130
		case <-time.After(time.Second):
		}
	}
}
