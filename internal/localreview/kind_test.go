package localreview

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestCheckImage fakes the probe, so the probe itself is never exercised: a
// change to the crictl invocation or to the "no such image" handling would pass
// every unit test and still fail every real review. This runs the real thing
// against the Kind node scripts/kind-up.sh prepares.
func TestKindInspector(t *testing.T) {
	if os.Getenv("BOSUN_KIND_TEST") != "1" {
		t.Skip("set BOSUN_KIND_TEST=1 to test against kind-bosun")
	}
	inspect := kindInspector(env("BOSUN_KIND_CLUSTER", "bosun"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// An absent image is a reported absence, not a probe failure: the two carry
	// different consequences for a pullable reference.
	if _, found, e := inspect(ctx, "bosun-no-such-image:absent"); e != nil {
		t.Fatalf("absent image reported a probe failure: %v", e)
	} else if found {
		t.Fatal("absent image reported as present")
	}

	data, found, e := inspect(ctx, devImage)
	if e != nil {
		t.Fatalf("inspect %s: %v", devImage, e)
	}
	if !found {
		t.Fatalf("%s is not loaded into the cluster; run ./scripts/kind-up.sh", devImage)
	}
	if !strings.Contains(string(data), "imageSpec") {
		t.Fatalf("probe output is not a crictl document: %.120s", data)
	}
	warning, e := checkImage(ctx, devImage, "dev", inspect)
	if e != nil {
		t.Fatalf("the loaded %s failed the preflight: %v", devImage, e)
	}
	if warning != "" {
		t.Errorf("the loaded %s warned: %s", devImage, warning)
	}
}
