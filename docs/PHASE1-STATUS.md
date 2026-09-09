# Phase 1 implementation and verification ledger

Baseline: `02ef77cdc3b1c1cd27ae618eeeff93c761c062a9`.
Plan: Zeus five-phase implementation plan dated 2026-09-07.

## Current state: P1.07–P1.10 source landed, G1 OPEN, NOT production-verified

P1.01–P1.10 implementation is in this tree. It does not close the audit's
security findings merely by adding tests. Public shared-tenant release remains
blocked. V07 remains contained. Do not start Phase 2.

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
- Tenant-scoped `audit_events` for session login/logout, enrollment, client create/revoke, agent revoke/rotate, and CLI admin-token issue. `GET /api/admin/audit` and `GET /api/admin/agents/expected` are admin-only; expected-agent completeness is always `not_assessed`.
- Bounded metric/process/CSV reads accept `limit` and `cursor=unix:id` and return `complete` / `next_cursor` (CSV via headers). Query limits are capped at 100000.
- `GET /api/servers` accepts `limit` (1..1000) and `cursor=<server_id>` and returns `complete` / `next_cursor`.

### Validation status

Local continuation on 2026-09-08 used `go version go1.26.6 windows/amd64`. Git is not installed, so no implementation commit hash can be recorded.

Executed here:
- `gofmt -l .` produced no files (clean).
- `go test ./storage ./server ./dashboard ./export ./agent ./retention ./collector ./alerting . -count=1` exit 0.
- `go test ./server` required **3** focused retries (JSON audit tags; metrics cursor fixture used future timestamps vs `Until=now`). Do not treat this as a suppressed gate.
- Loopback dashboard `http://127.0.0.1/` loaded in a real browser: auth modal visible, empty query string, empty `localStorage`, empty `document.cookie`. Submitting a read token through browser automation was **not** performed, so cookie Set-Cookie/CSRF/revocation in a real browser remain **NOT RUN**.
- `TestSessionCookieJarOverLoopbackTCP` exit 0: real TCP `httptest` server, cookie jar login, same-origin metrics, cross-site logout 403, same-origin logout then 401. This is not a browser engine.
- `node --check dashboard/static/session.js` and `node scripts/check-session.cjs` exit 0.
- `go vet ./...` exit 0.
- `go build ./...` exit 0.
- `go mod verify` reported `all modules verified`.
- `go test -race ./storage -run TestAuditRequiresTenantAndIsolatesRows`: **FAIL at compile**. `CGO_ENABLED=1` and `CC=gcc`, but `gcc` is not an executable on PATH (`cgo: C compiler "gcc" not found`). This is an environment blocker, not a suppressed test.
- `govulncheck ./...` (binary installed to a temp `GOBIN`, because `GOPATH/bin` was empty): **No vulnerabilities found.** This is a source scan of this tree, not a signed selected-module/binary scan of a release artifact.

NOT RUN here: live PostgreSQL (no `psql`/`docker` on PATH), native service/ACL, real browser cookie/CSRF, power-loss/disk-full spool injection, selected-module/binary scans of signed release artifacts. A configured CI job is not evidence of a passing CI run.

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
| P1.01 test isolation, patched toolchain and truthful baseline | Implemented in source; `go 1.26.6` used locally. Git/CI URLs **NOT RECORDED** (git not installed). |
| P1.02 retention containment, safe HTML/CSV and documentation | Implemented earlier; V07 still contained, not closed |
| P1.03 identity, authorization and legacy/bootstrap migration | Implemented in source; G1 not signed off |
| P1.04 atomic enrollment/rotation and cache lifecycle | Implemented in source; replica warmed-cache **NOT RUN** |
| P1.05 tenant-scoped storage/compute/reports | Implemented in source; PostgreSQL RLS **NOT RUN** |
| P1.06 TLS/origin/secrets/Unix ownership/Windows DACL | Implemented in source; second-user ACL **NOT RUN** |
| P1.07 secure sessions and browser boundaries | Implemented; Node VM + loopback TCP cookie-jar tests pass. Real browser loaded the login modal only. Cookie submit/CSRF in a browser engine **NOT RUN** |
| P1.08 service lifecycle/configuration | Implemented in source; native install/reboot/stop/uninstall **NOT RUN** |
| P1.09 spool correctness/idempotency/async transport | Implemented; SQLite ingest tests pass; `TestSpoolFullAppliesBackpressureWithoutEviction` pass. OS disk-full and power-loss injection **NOT RUN** |
| P1.10 budgets/health/audit/foundation review | Audit ledger + expected-agent route + keyset `complete`/`cursor` in source and unit tests. Live PG fixture skipped (no local PostgreSQL/docker). `go test -race` **NOT RUN** (no gcc). `govulncheck ./...` reported no vulnerabilities in this source tree (not a signed release binary scan) |

**G1 remains OPEN.** Do not start Phase 2.

## Next session

Paste this prompt:

```text
Continue P1.07–P1.10 / G1 from zeus-five-phase-agent-handoff/zeus-five-phase-handoff/P1.07-P1.10-IMPLEMENTATION-HANDOFF.md.

Workspace: c:\Users\markmv\Desktop\Zeus. Authoritative ledger: docs/PHASE1-STATUS.md. Query graphify first. Do not mark Phase 1 or G1 complete unless every explicit requirement is proven against current evidence. V07 stays contained.

Source for P1.07–P1.10 is already in the tree. Prior local run used go1.26.6 windows/amd64; git is not installed. Max 5 retries per verification command; do not suppress failing gates.

Next work, in order:
1. Install a C compiler so `go test -race ./... -count=1` can compile (gcc is not on PATH; CGO_ENABLED=1 CC=gcc).
2. Run the disposable PostgreSQL fixture: `WMONITOR_PHASE1_PG_FIXTURE=1` against postgres://phase1:fixture-only@127.0.0.1:5432/phase1_test?sslmode=disable (storage/p110_postgres_fixture_test.go). Never use a production DSN.
3. Real-browser cookie/CSRF/revocation: start `WMONITOR_PHASE1_BROWSER=1` TestP107LoopbackHubForBrowser, then submit a disposable read token in a browser engine. Page-load-only and Go cookie-jar tests are not sufficient.
4. Native Windows (and Linux if available) service install/reboot/stop/uninstall and second-user ACL.
5. OS disk-full / power-loss spool injection (unit spool tests are not this).
6. Signed selected-module/binary scans of a release artifact. Source `govulncheck ./...` already reported no vulns and is not that gate.
7. Record one exact commit hash once git exists. Qualified security/operator sign-off is still required.

Do not start Phase 2. Do not re-enable downsampleMetrics/purge.
```

## Compatibility and rollback

No database migration or credential encoding change. Existing callers of
LoadCredentials/SaveCredentials/ClearCredentials/New retain their entry points.
New constructors require absolute paths. Production symlink/ownership/atomic
write and Windows DACL policies are NOT fixed here. Never advertise them as fixed.

A reviewed code revert is technically possible without a database downgrade,
but reverting the tests would restore the production-path test hazard. Preserve
isolation if reverting unrelated changes. Do not bypass failing release checks;
fix and record their actual causes before issuing a release.
