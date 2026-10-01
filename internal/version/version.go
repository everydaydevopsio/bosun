// Package version reports the release a binary was built from.
//
// It exists so the CLI and the code that resolves the reviewer image read the
// same stamped value: a released binary must be able to name the image that was
// published alongside it.
package version

// Version is stamped at build time with
//
//	-ldflags "-X github.com/everydaydevopsio/bosun/internal/version.Version=<version>"
//
// An unstamped build reports "dev".
var Version = "dev"
