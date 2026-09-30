// Package progress defines the worker-to-CLI event protocol.
package progress

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"
)

type Event struct {
	Version      int       `json:"bosun_event"`
	Run          string    `json:"run"`
	Seq          uint64    `json:"seq"`
	Time         time.Time `json:"time"`
	Type         string    `json:"type"`
	Stage        string    `json:"stage,omitempty"`
	Elapsed      float64   `json:"elapsed_seconds"`
	StageElapsed float64   `json:"stage_seconds"`
	Quiet        float64   `json:"quiet_seconds"`
	HasActivity  bool      `json:"has_activity"`
	Message      string    `json:"message,omitempty"`
}
type Reporter struct {
	mu                          sync.Mutex
	out                         io.Writer
	run                         string
	seq                         uint64
	start, stageStart, activity time.Time
	stage                       string
	hasActivity, terminal       bool
}
type key struct{}

func With(ctx context.Context, r *Reporter) context.Context { return context.WithValue(ctx, key{}, r) }
func From(ctx context.Context) *Reporter                    { r, _ := ctx.Value(key{}).(*Reporter); return r }
func New(w io.Writer, run string) *Reporter {
	now := time.Now()
	return &Reporter{out: w, run: run, start: now, stageStart: now, activity: now}
}
func (r *Reporter) Emit(kind, message string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.terminal {
		return
	}
	now := time.Now()
	if kind == "stage" {
		r.stage = message
		r.stageStart = now
	}
	if kind == "activity" {
		r.activity = now
		r.hasActivity = true
	}
	if kind == "completed" || kind == "failed" || kind == "timed_out" || kind == "cancelled" {
		r.terminal = true
	}
	r.seq++
	_ = json.NewEncoder(r.out).Encode(Event{Version: 1, Run: r.run, Seq: r.seq, Time: now, Type: kind, Stage: r.stage, Elapsed: now.Sub(r.start).Seconds(), StageElapsed: now.Sub(r.stageStart).Seconds(), Quiet: now.Sub(r.activity).Seconds(), HasActivity: r.hasActivity, Message: message})
}
func (r *Reporter) Heartbeat(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.Emit("heartbeat", "Worker active; provider activity is reported separately")
		}
	}
}
