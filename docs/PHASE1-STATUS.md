# Phase 1 implementation and verification ledger

Baseline: `02ef77cdc3b1c1cd27ae618eeeff93c761c062a9`.
Plan: Zeus five-phase implementation plan dated 2026-09-07.

## Current state: started, NOT complete or production-verified

This commit implements the filesystem-isolation foundation of P1.01. It does
not implement all ten Phase 1 tickets and does not close the audit's security
findings merely by adding tests. Public shared-tenant release remains blocked.

### Changes implemented

- Explicit instance-scoped `CredentialStore`, with no environment/global test
  override, no implicit path fallback, and no constructor/read/clear mkdir.
- Existing production credential entry points retain their normal paths and
  encoding. Unix missing-home resolution now fails closed instead of using /tmp.
- Credential tests use isolated temporary roots. They cover round trips,
  independent stores, outside canaries, invalid/zero stores, side-effect-free
  construction and platform-specific file encoding/permissions.
- `NewWithDataDir` supplies an explicit spool root and returns initialization
  errors instead of falling back to a production directory or disabling spool.
- Agent transport tests use that constructor, close handles and read complete
  request bodies. New tests cover isolated backlog persistence and canaries.
- AST-based test-source guard rejects known production credential/data path
  entry points. CI runs this guard before executing the broad test suite.
- CI and release use the module's Go version instead of a conflicting 1.22 pin.
  Formatting is a separate required job so baseline format errors do not prevent
  independent test jobs from reporting their results.
- Tag releases require the reusable CI workflow to succeed on that same commit.

### Validation status

Static source review performed. Go build, gofmt, vet, race tests, native Windows
DPAPI tests, module verification and vulnerability scans were NOT RUN by the
implementation environment: it has no Go toolchain or Windows/PostgreSQL runner,
and its sandbox has no internet access to install tools. A configured CI job is
not evidence of a passing CI run. Record actual CI run URLs/results before
changing this status. GitHub code search returned incomplete results; the
repository-wide AST check must establish the final known-entry-point inventory.

The baseline module remains `go 1.26.5`; aligning CI to it does not establish that
it is patched or vulnerability-free. Selected-module/toolchain upgrades, scanner
version pinning, complete history/release scans and native acceptance evidence
remain pending. Existing scanner checks are retained, not bypassed. Baseline
formatting or dependency findings may correctly fail the release gate.

### Required disposable-runner commands

```sh
go version
go env GOTOOLCHAIN GOOS GOARCH
go mod verify
go test ./agent -run '^TestNoProductionPathCallsInTests$' -count=1
go test -race ./agent -run 'TestCredential|TestAgentExplicitDirectory' -count=1
go build ./...
go vet ./...
go test -race ./... -count=1
govulncheck ./...
```

Run on disposable Linux and Windows runners, never customer/enrolled machines.
The source guard detects known entry points, not arbitrary transitive filesystem
side effects. Canary tests protect disposable files outside each operation's
fixture; they do not claim a whole-suite production-machine safety proof.

## Remaining Phase 1 tickets

| Ticket | State |
|---|---|
| P1.01 test isolation, patched toolchain and truthful baseline | Implemented in part; runtime verification, patched toolchain and scanner pinning pending |
| P1.02 retention containment, safe HTML/CSV and documentation | Not implemented by this commit |
| P1.03 identity, authorization and legacy/bootstrap migration | Not implemented by this commit |
| P1.04 atomic enrollment/rotation and cache lifecycle | Not implemented by this commit |
| P1.05 tenant-scoped storage/compute/reports | Not implemented by this commit |
| P1.06 TLS/origin/secrets/Unix ownership/Windows DACL | Not implemented by this commit |
| P1.07 secure sessions and browser boundaries | Not implemented by this commit |
| P1.08 service lifecycle/configuration | Not implemented by this commit |
| P1.09 spool correctness/idempotency/async transport | Not implemented by this commit |
| P1.10 budgets/health/audit/foundation review | Not implemented; same-commit release dependency added only |

## Compatibility and rollback

No database migration or credential encoding change. Existing callers of
LoadCredentials/SaveCredentials/ClearCredentials/New retain their entry points.
New constructors require absolute paths. Production symlink/ownership/atomic
write and Windows DACL policies are NOT fixed here. Never advertise them as fixed.

A reviewed code revert is technically possible without a database downgrade,
but reverting the tests would restore the production-path test hazard. Preserve
isolation if reverting unrelated changes. Do not bypass failing release checks;
fix and record their actual causes before issuing a release.
