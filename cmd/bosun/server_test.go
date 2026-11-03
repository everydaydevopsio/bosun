package main

import (
	"testing"

	"github.com/everydaydevopsio/bosun/internal/jobs"
)

// The webhook server is internet-facing and verifies signatures only after the
// request is read. With no timeouts a client can hold connections open with
// slow headers or a slow body and exhaust the pod's file descriptors before any
// authentication runs.
func TestWebhookServerHasBoundedTimeouts(t *testing.T) {
	srv := newWebhookServer(nil)

	if srv.ReadHeaderTimeout <= 0 {
		t.Error("ReadHeaderTimeout must be set; slow-header clients hold connections open indefinitely")
	}
	if srv.ReadTimeout <= 0 {
		t.Error("ReadTimeout must be set; slow-body clients hold connections open indefinitely")
	}
	if srv.IdleTimeout <= 0 {
		t.Error("IdleTimeout must be set; kept-alive connections are never reclaimed")
	}

	// Admission waits for the lease and then runs the transaction it guards.
	// A WriteTimeout at or below that total cuts the response off while the
	// handler is still legitimately working, turning a delivery that queued
	// behind another replica into a GitHub-side failure.
	if srv.WriteTimeout <= jobs.AdmissionBudget {
		t.Errorf("WriteTimeout %v must exceed the %v admission budget", srv.WriteTimeout, jobs.AdmissionBudget)
	}
	if srv.ReadHeaderTimeout > srv.ReadTimeout {
		t.Errorf("ReadHeaderTimeout %v must not exceed ReadTimeout %v", srv.ReadHeaderTimeout, srv.ReadTimeout)
	}
}

func TestWebhookServerListensOnTheDocumentedPort(t *testing.T) {
	if got := newWebhookServer(nil).Addr; got != ":8080" {
		t.Errorf("Addr = %q, want %q (the chart's targetPort)", got, ":8080")
	}
}
