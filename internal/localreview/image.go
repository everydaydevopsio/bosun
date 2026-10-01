package localreview

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// devImage is the tag scripts/kind-up.sh builds and loads into the Kind node.
const devImage = "bosun:dev"

// publishedImage is the repository publish.yml pushes release images to.
const publishedImage = "ghcr.io/everydaydevopsio/bosun"

// titleLabel identifies the project an image was built from. The reviewer image
// is built FROM bridgectl, so an image that does not set this to "bosun"
// carries the base image's identity and has no bosun binary in it.
const titleLabel = "org.opencontainers.image.title"
const versionLabel = "org.opencontainers.image.version"

// releaseVersion matches an exact release, with or without the "v" prefix.
// `git describe` output for a local build ("v0.1.1-2-gabc1234", "v0.1.1-dirty")
// deliberately does not match: no image was ever published under it.
var releaseVersion = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+$`)

// reviewImage resolves the image a local review runs in, reporting whether the
// caller chose it explicitly.
//
// A released binary must not default to bosun:dev. That tag exists only on a
// machine that built it, so a Homebrew install would silently reuse whatever
// stale image happens to carry the name -- or fail to find one at all. Released
// builds therefore default to the image published alongside them, and only an
// unstamped or locally described build falls back to the dev tag.
func reviewImage(v string) (string, bool) {
	// BOSUN_DEV_IMAGE is the dev-loop override scripts/kind-up.sh already
	// honours; BOSUN_REVIEW_IMAGE is the same knob the controller reads.
	for _, key := range []string{"BOSUN_DEV_IMAGE", "BOSUN_REVIEW_IMAGE"} {
		if image := os.Getenv(key); image != "" {
			return image, true
		}
	}
	if releaseVersion.MatchString(v) {
		return publishedImage + ":" + strings.TrimPrefix(v, "v"), false
	}
	return devImage, false
}

// inspector reports the `crictl inspecti -o json` document for ref on the Kind
// node. found is false when the node holds no such image; a non-nil error means
// the probe itself could not run, which is not evidence either way.
type inspector func(ctx context.Context, ref string) (data []byte, found bool, err error)

// kindInspector inspects images through the Kind node's container runtime.
// Reading image metadata is all it does; it never touches container state.
func kindInspector(cluster string) inspector {
	return func(ctx context.Context, ref string) ([]byte, bool, error) {
		cmd := exec.CommandContext(ctx, "docker", "exec", cluster+"-control-plane", "crictl", "inspecti", "-o", "json", ref)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err == nil {
			return out, true, nil
		}
		if strings.Contains(stderr.String(), "no such image") {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
}

// pullable reports whether a reference names a registry the kubelet can pull
// from. A bare name:tag resolves to docker.io/library, where a locally built tag
// such as bosun:dev exists nowhere, so its absence from the node is terminal.
func pullable(ref string) bool {
	host, _, ok := strings.Cut(ref, "/")
	return ok && (strings.ContainsAny(host, ".:") || host == "localhost")
}

// checkImage verifies, before any work is done, that the reviewer image the Job
// will run is a Bosun image.
//
// The Job runs /usr/local/bin/bosun, which exists only in this project's image.
// Pointed at anything else -- most likely a stale tag left in the Kind node by
// an earlier version of this project -- the kubelet reports an opaque OCI
// "no such file or directory" after the snapshot has already been taken. This
// turns that into a preflight failure that names the fix.
//
// It returns either a warning or an error, never both: a warning is something
// the run can proceed through, an error is not.
func checkImage(ctx context.Context, ref, cliVersion string, inspect inspector) (string, error) {
	data, found, err := inspect(ctx, ref)
	if err != nil {
		return fmt.Sprintf("Could not verify the reviewer image %s on the Kind node: %v", ref, err), nil
	}
	if !found {
		if pullable(ref) {
			return "", nil
		}
		return "", fmt.Errorf("image %s is not loaded into the Kind cluster; build and load it with ./scripts/kind-up.sh", ref)
	}
	var doc struct {
		Info struct {
			ImageSpec struct {
				Config struct {
					Labels map[string]string `json:"Labels"`
				} `json:"config"`
			} `json:"imageSpec"`
		} `json:"info"`
	}
	if e := json.Unmarshal(data, &doc); e != nil {
		return fmt.Sprintf("Could not verify the reviewer image %s on the Kind node: %v", ref, e), nil
	}
	labels := doc.Info.ImageSpec.Config.Labels
	if title := labels[titleLabel]; title != "bosun" {
		detail := "carries no Bosun image label"
		if title != "" {
			detail = fmt.Sprintf("is a %q image", title)
		}
		return "", fmt.Errorf("image %s on the Kind node %s, so it has no bosun binary to run; rebuild and reload it with ./scripts/kind-up.sh", ref, detail)
	}
	// A dev image carries no release identity, so comparing it to the CLI
	// version would warn on every contributor's normal workflow.
	imageVersion, cli := strings.TrimPrefix(labels[versionLabel], "v"), strings.TrimPrefix(cliVersion, "v")
	if imageVersion == "" || imageVersion == "dev" || cli == "" || cli == "dev" || imageVersion == cli {
		return "", nil
	}
	return fmt.Sprintf("Reviewer image %s was built from version %s; this CLI is %s", ref, imageVersion, cli), nil
}
