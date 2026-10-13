# Review snapshot path contract

- Status: Implemented locally; CI and release verification pending
- Branch: `issue-41-review-snapshot-path`
- Created: 2026-10-13
- Issue: [#41](https://github.com/everydaydevopsio/bosun/issues/41)
- Related ADRs: none

## Problem

The pinned bridgectl image rejects local snapshots at `/repos/review.*` because
the shipped config does not allow `/repos`. A submitted review stops before an
agent reads code and reports a generic provider failure.

## Approach

Allow the single Kind snapshot root in Bosun's bridgectl config. Exercise both
an accepted snapshot and a rejected outside path with the pinned image in CI.
Classify this exact StartSession rejection as a setup incompatibility, retaining
the original error and image reference. Align the installed skill with the
CLI's committed-only `--branch` snapshot behavior.

## Files Affected

- `config/bridge-bosun.yaml`: path allowance.
- `internal/review/`, `internal/jobs/`: diagnosis and worker image provenance.
- `scripts/smoke-bridge-path.sh`, `.github/workflows/ci.yml`: image contract.
- `internal/skills/assets/bosun-review/SKILL.md`, `docs/`: operating guidance.

## Phases

- [x] Reproduce config and image rejection.
- [x] Add failing regression tests and implement the config and diagnostic.
- [x] Add image contract check and update guidance.
- [ ] Verify a freshly built image in CI and a complete credentialed review.

## Verification

- `go test ./internal/review ./internal/jobs -count=1`
- `scripts/smoke-bridge-path.sh ghcr.io/everydaydevopsio/bosun:0.1.3` fails as expected.
- Same command with `config/bridge-bosun.yaml` as the second argument passes.
- CI builds `bosun:test` and runs the script without an override.

## Alternatives Rejected

- Disable bridgectl path validation: grants access beyond the snapshot mount.
- Allow `/tmp`: the worker's repository mount is `/repos`, and broader access
  would not express the actual contract.

## Open Questions

- A full local review needs a fresh image and usable provider credentials;
  Docker storage is currently full on the local host.

## Change Log

| Date | Change |
| --- | --- |
| 2026-10-13 | Reproduced the failure, implemented the narrow path contract and diagnostic. |
