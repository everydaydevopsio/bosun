# Releasing

Bosun releases one version across three artifacts, all built from the same tag:

| Artifact | Where it lands |
| --- | --- |
| Container image | `ghcr.io/everydaydevopsio/bosun:<version>` (linux/amd64, linux/arm64) |
| Helm chart | `oci://ghcr.io/everydaydevopsio/charts/bosun:<version>` |
| CLI archives | GitHub Release assets, plus the `everydaydevopsio/homebrew-bosun` cask |

> **Before the first release:** GHCR packages are created **private**. The image
> and the chart each become a separate package on their first push, and both
> start private regardless of the repository being public. Until their
> visibility is changed, `helm install oci://ghcr.io/...` and the chart's
> default image pull fail for anyone without GHCR credentials — including the
> install commands in the README.
>
> After the first successful release, set both to public at
> <https://github.com/orgs/everydaydevopsio/packages>:
>
> - `bosun` (container)
> - `charts/bosun` (chart)
>
> Package settings → **Danger Zone** → *Change visibility* → **Public**. This is
> a one-time step; later releases inherit the setting.

## Cutting a release

1. Open **Actions → Publish → Run workflow** on `main`.
2. Choose `patch`, `minor`, or `major`.
3. Run it.

The workflow then:

1. **validate** — runs `.github/workflows/validate.yml`: gofmt, `go vet`, tests with
   coverage, CLI build and `bosun version`, Helm lint and render, shellcheck, image
   build and smoke test. Nothing is tagged until this passes.
2. **bump_and_tag** — computes the next semver from the previous tag, rewrites
   `charts/bosun/Chart.yaml` `version` and `appVersion`, generates release notes and
   a `CHANGELOG.md` entry with [castoff](https://github.com/everydaydevopsio/castoff),
   commits, and pushes the `v<version>` tag. **This is the point of no return.**
3. **publish_image** — builds the multi-arch image from the tag, scans it with Trivy,
   and pushes to GHCR only if no fixable HIGH/CRITICAL finding exists.
4. **publish_chart** — packages and pushes the chart at the same version.
5. **publish_cli** — on `macos-latest`, GoReleaser builds darwin and linux archives,
   signs and notarizes the darwin binaries as a post-build hook, publishes the
   GitHub Release, and updates the Homebrew tap.

Pushing a `v*` tag by hand runs the same publish jobs without the bump. The tag
`bump_and_tag` pushes is excluded by an actor guard, so a dispatch run never
publishes twice.

## Required repository configuration

### Secrets

| Secret | Used by | Purpose |
| --- | --- | --- |
| `OPENAI_API_KEY` | `bump_and_tag` | castoff release notes and changelog. The job fails loudly if it is missing. |
| `APPLE_CERTIFICATE` | `publish_cli` | Base64 `.p12` Developer ID Application certificate |
| `APPLE_CERTIFICATE_PASSWORD` | `publish_cli` | Password for that `.p12` |
| `APPLE_SIGNING_IDENTITY` | `publish_cli` | e.g. `Developer ID Application: … (TEAMID)` |
| `APPLE_API_KEY_ID` | `publish_cli` | App Store Connect API key ID |
| `APPLE_API_KEY_ISSUER_ID` | `publish_cli` | App Store Connect issuer ID |
| `APPLE_API_KEY_CONTENT` | `publish_cli` | Base64 `.p8` App Store Connect key |
| `HOMEBREW_TAP_GITHUB_TOKEN` | `publish_cli` | Write access to `everydaydevopsio/homebrew-bosun` |
| `CODECOV_TOKEN` | CI | Coverage upload (non-fatal when absent) |

`GITHUB_TOKEN` is provided automatically.

### Variables

| Variable | Default | Purpose |
| --- | --- | --- |
| `BRIDGECTL_VERSION` | `v1.4.1` | bridgectl base image tag built into the release image |
| `OPENAI_MODEL` | castoff default | Model used for release notes |

### Repositories

`everydaydevopsio/homebrew-bosun` must exist and be public before the first
release. GoReleaser writes `Casks/bosun.rb` into it.

## Versioning rules

- Tags are `v`-prefixed semver (`v1.2.3`); artifact versions drop the `v`.
- The chart `version` and `appVersion`, the image tag, and `bosun version` all equal
  the release version. The chart's `image.tag` defaults to the chart `appVersion`,
  so an installed chart always pulls the image it was released with.
- No `latest` tag is published. Pin a version, or pin the published digest.

## macOS signing

Signing and notarization run inside GoReleaser's build phase via
`scripts/sign-and-notarize-darwin.sh`, before any artifact is uploaded. A
notarization failure therefore aborts the release rather than leaving
signed-but-unnotarized archives on a published Release and advertised by the tap.

`notarytool submit --wait` exits 0 even when Apple returns `Invalid`, so the script
reads the status out of the JSON and asserts it is `Accepted`.

Local snapshot builds skip signing: `make release-snapshot` passes GoReleaser's
`IsSnapshot` flag through to the hook, and snapshots are never published.

## Recovering a failed release

`bump_and_tag` is the boundary. Classify the failure before retrying:

| Failure | Recovery |
| --- | --- |
| Before any outward write (validation, signing, notarization, scan) | Re-run the failed job. |
| After a partial outward write (some assets uploaded, then a step failed) | Roll forward with a new patch release. |
| After a complete write (registry rejects a duplicate) | Nothing to do. |

Re-running a job that already published fails on its own artifacts. GoReleaser is
configured with `release.mode: replace` so a re-run can overwrite its own assets,
but workflows read config from the checked-out tag, so that only helps releases
tagged after the setting landed. Default to rolling forward: a burned version
number is cheaper than a mutated one.

## Local verification

```bash
make coverage          # tests with a coverage profile
make lint              # gofmt, shellcheck, helm lint and render
make release-check     # validate .goreleaser.yaml
make release-snapshot  # build unsigned archives into dist/
```
