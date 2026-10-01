package localreview

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// inspectJSON builds the subset of `crictl inspecti -o json` that checkImage
// reads, so the tests exercise the real document shape.
func inspectJSON(t *testing.T, labels map[string]string) []byte {
	t.Helper()
	b, e := json.Marshal(map[string]any{"info": map[string]any{"imageSpec": map[string]any{"config": map[string]any{"Labels": labels}}}})
	if e != nil {
		t.Fatal(e)
	}
	return b
}

// A released CLI must never default to bosun:dev: that tag only exists on a
// machine that built it, so a Homebrew install would reuse whatever stale image
// happens to carry it. Equally, a `git describe` version names no published
// image, so local builds must stay on the dev tag.
func TestReviewImage(t *testing.T) {
	tests := []struct {
		name, version, dev, review, want string
		wantExplicit                     bool
	}{
		{name: "unstamped build uses the dev tag", version: "dev", want: "bosun:dev"},
		{name: "empty version uses the dev tag", version: "", want: "bosun:dev"},
		{name: "release uses the published image", version: "0.1.1", want: "ghcr.io/everydaydevopsio/bosun:0.1.1"},
		{name: "v prefix is trimmed", version: "v0.1.1", want: "ghcr.io/everydaydevopsio/bosun:0.1.1"},
		{name: "git describe is not a release", version: "v0.1.1-2-gabc1234", want: "bosun:dev"},
		{name: "dirty describe is not a release", version: "v0.1.1-dirty", want: "bosun:dev"},
		{name: "BOSUN_DEV_IMAGE overrides", version: "0.1.1", dev: "local/bosun:wip", want: "local/bosun:wip", wantExplicit: true},
		{name: "BOSUN_REVIEW_IMAGE overrides", version: "0.1.1", review: "ghcr.io/fork/bosun:1", want: "ghcr.io/fork/bosun:1", wantExplicit: true},
		{name: "BOSUN_DEV_IMAGE wins", version: "dev", dev: "a:1", review: "b:2", want: "a:1", wantExplicit: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BOSUN_DEV_IMAGE", tc.dev)
			t.Setenv("BOSUN_REVIEW_IMAGE", tc.review)
			got, explicit := reviewImage(tc.version)
			if got != tc.want {
				t.Errorf("reviewImage(%q) = %q, want %q", tc.version, got, tc.want)
			}
			if explicit != tc.wantExplicit {
				t.Errorf("reviewImage(%q) explicit = %v, want %v", tc.version, explicit, tc.wantExplicit)
			}
		})
	}
}

// The reviewer Job runs `/usr/local/bin/bosun reviewer`, so an image that is not
// a Bosun image fails as an opaque OCI "no such file or directory" 14 seconds
// after the snapshot is taken. These cases are the ones that failure splits into.
func TestCheckImage(t *testing.T) {
	const bosunTitle = "org.opencontainers.image.title"
	const bosunVersion = "org.opencontainers.image.version"
	tests := []struct {
		name, ref, cli    string
		labels            map[string]string
		found             bool
		probeErr          error
		data              []byte
		wantErr, wantWarn string
	}{
		{
			name: "stale image from another project under the dev tag", ref: "bosun:dev", cli: "0.1.1", found: true,
			labels:  map[string]string{bosunTitle: "bridgectl", bosunVersion: "v1.3.0"},
			wantErr: "kind-up.sh",
		},
		{
			name: "image carrying no title label", ref: "bosun:dev", cli: "0.1.1", found: true,
			labels: map[string]string{}, wantErr: "kind-up.sh",
		},
		{
			name: "matching dev image", ref: "bosun:dev", cli: "dev", found: true,
			labels: map[string]string{bosunTitle: "bosun", bosunVersion: "dev"},
		},
		{
			name: "released CLI against a locally built image does not warn", ref: "bosun:dev", cli: "0.1.1", found: true,
			labels: map[string]string{bosunTitle: "bosun", bosunVersion: "dev"},
		},
		{
			name: "older image than the CLI warns", ref: "ghcr.io/everydaydevopsio/bosun:0.1.0", cli: "0.1.1", found: true,
			labels: map[string]string{bosunTitle: "bosun", bosunVersion: "0.1.0"}, wantWarn: "0.1.0",
		},
		{
			name: "v prefix is not a mismatch", ref: "bosun:dev", cli: "v0.1.1", found: true,
			labels: map[string]string{bosunTitle: "bosun", bosunVersion: "0.1.1"},
		},
		{
			name: "locally built tag missing from the node", ref: "bosun:dev", cli: "dev",
			wantErr: "not loaded",
		},
		{
			name: "published image missing from the node is pulled", ref: "ghcr.io/everydaydevopsio/bosun:0.1.1", cli: "0.1.1",
		},
		{
			name: "probe that cannot run only warns", ref: "bosun:dev", cli: "dev",
			probeErr: errors.New("docker: command not found"), wantWarn: "docker",
		},
		{
			name: "unreadable probe output only warns", ref: "bosun:dev", cli: "dev", found: true,
			data: []byte("not json"), wantWarn: "verify",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inspect := func(context.Context, string) ([]byte, bool, error) {
				if tc.data != nil {
					return tc.data, tc.found, tc.probeErr
				}
				return inspectJSON(t, tc.labels), tc.found, tc.probeErr
			}
			warning, e := checkImage(context.Background(), tc.ref, tc.cli, inspect)
			if tc.wantErr == "" && e != nil {
				t.Fatalf("checkImage returned unexpected error %v", e)
			}
			if tc.wantErr != "" {
				if e == nil {
					t.Fatalf("checkImage returned no error, want one mentioning %q", tc.wantErr)
				}
				if !strings.Contains(e.Error(), tc.wantErr) {
					t.Fatalf("error %q does not mention %q", e, tc.wantErr)
				}
				if !strings.Contains(e.Error(), tc.ref) {
					t.Errorf("error %q does not name the image %q", e, tc.ref)
				}
				return
			}
			if tc.wantWarn == "" && warning != "" {
				t.Fatalf("checkImage returned unexpected warning %q", warning)
			}
			if tc.wantWarn != "" && !strings.Contains(warning, tc.wantWarn) {
				t.Fatalf("warning %q does not mention %q", warning, tc.wantWarn)
			}
		})
	}
}
