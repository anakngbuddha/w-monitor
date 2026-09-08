# Phase 1 of 5: secure and stabilize the foundation

**Outcome:** safe development and bounded authenticated ingestion, not yet a customer-ready assessment. **Runtime layers:** prerequisite for all five. **Read first:** `00-SHARED-CONTRACT.md` and the referenced original V/L findings. **Entry:** pinned baseline inspected; no production execution. **Exit:** G1 below. Execute tickets in order; a later phase cannot waive a failed security gate.

## P1.01: isolate tests and establish a truthful baseline
- Depends on: none. Existing targets: `agent/credstore*.go`, `agent/agent_test.go`, spool fixtures, CI workflows.
- Inject credential/data/spool roots; use `t.TempDir()` everywhere. Add a guard rejecting production paths in test fixtures. Capture current build, targeted tests, module graph and selected toolchain on disposable runners. Align module/CI versions; review V21 with current selected-module and binary scans, not just copied advisory claims.
- Accept: a canary outside the fixture is byte-for-byte unchanged by the suite; Linux/Windows fixture paths isolated; CI failures remain visible. Attach commands/results. Resolves V22; begins V21/L17.

## P1.02: containment controls and safe export rendering
- Depends on: P1.01. Targets: `retention/retention.go`, `export/report.go`, `export/export.go`, documentation.
- Disable destructive downsampling until P2.03 replaces it; apply disk-pressure backpressure and visible retention-disabled status instead of silent unbounded growth. Use `html/template` for all untrusted names; implement documented spreadsheet-safe CSV separately from lossless machine export. Correct dangerous closure claims.
- Accept: original tenant/server rows survive a retention cycle; multi-host XSS fixture renders literal text; formula cases and flush errors tested; disk budget exhaustion is explicit. Operator runbook requires backup verification and rotation of any actually live exposed secret before rollout. No credentials are rotated automatically. V07/V08/V09/V13/L18.

## P1.03: separate identities and fail-closed authorization
- Depends on: P1.02. Targets: `server/auth.go`, `server/enroll.go`, `storage/apikeys.go`, focused new identity schema/migrations.
- Typed principal carries tenant/agent/credential/kind/permissions/expiry; legacy/all cannot grant platform admin. Enrollment codes are handshake-only. Read cannot revoke. Enforce bound asset identity at ingestion. Stop automatic CSV/config-key resurrection; exact opaque-token hashing and explicit one-time opaque-tenant migration with reviewed backup/rollback procedure.
- Accept: full route/method/kind/scope matrix; A cannot write B; revoked keys stay revoked after restart; duplicate client display names never resolve identity. Migration fails atomically, never restores a revoked credential. V01-V05, V02, L11.

## P1.04: atomic enrollment, rotation and cache lifecycle
- Depends on: P1.03. Targets: credential store, enrollment handlers, auth cache.
- Separate new enrollment from replacement; replacement needs old-token proof or authorized operator approval. Transactionally consume/issue with identity constraints and idempotent handshake recovery. Bound cache lifetime by expiry; invalidate locally and propagate revocation/version to replicas with a documented maximum delay.
- Accept: concurrent same-identity enrollments, failed insert, lost reply and warmed caches across replicas cannot create uncontrolled active tokens or accept expired/revoked credentials beyond policy. V18/V19.

## P1.05: tenant scope across storage, compute and reports
- Depends on: P1.04. Targets: `storage/store.go`, SQLite/PG implementations, alert evaluator/history, server/CLI exports.
- Add typed required scope; separate privileged administrative operations. SQL-side tenant/server/time bounds and pagination; propagate context/cancellation. Composite alert keys include tenant/asset/rule. Require project/run scope for future exports; temporarily require explicit tenant scope for legacy ones. Never equate empty tenant with global read.
- Accept: same-ID two-tenant fixtures remain isolated in storage, alerts, CSV/HTML and background jobs; canceled queries stop; no global customer export. Introduce PostgreSQL least-privilege/RLS defense where tested, not merely configured. V06/V15, L09/L10/L13.

## P1.06: transport, destination and secret-file boundaries
- Depends on: P1.05. Targets: `agent/agent.go`, `agent/enroll.go`, credential stores, proxy/auth middleware, PG config.
- Require verified HTTPS in production before transmitting codes/tokens; reject insecure/spoofed proxy headers and authenticated cross-origin redirects. Bind token/spool to canonical origin and agent/tenant. Require remote DB TLS verification. Implement atomic ownership-checked files, Unix modes/symlink defenses, Windows service-identity DACLs; redact all credential/log/error paths.
- Accept: changed destination sends no prior token/backlog; bad certificates and redirects fail; standard Windows user cannot read/replace credentials; canary secrets absent from logs/service arguments. V10/V11/V13/V14/V20.

## P1.07: actual secure sessions and browser boundaries
- Depends on: P1.06. Targets: `server/session.go`, dashboard HTML/JS and headers.
- Opaque server-side sessions with Secure/HttpOnly/SameSite, expiry/revocation and CSRF/origin checks. Remove URL/localStorage credentials; never re-import query secrets. Self-host verified assets, CSP/anti-framing/no-store; throttle login before DB work. Fix inherited-key maps, stale overlapping requests and incomplete logout.
- Accept: real browser checks for login/logout/revocation, no URL/storage bearer, no external asset requests, cross-site mutation rejected, hostile map keys harmless; charts expose stale/gapped data. V12/V15, L15.

## P1.08: service lifecycle and strict configuration
- Depends on: P1.07. Targets: `main.go`, `server_id_flag.go`, installers, `alerts_setup.go`.
- Process service-control commands before workload dispatch; share service lifecycle across Hub/agent. Explicit machine-owned config paths and documented precedence; strict modes/URLs/ranges, no fallback to unauthenticated all-interface standalone. Join workers on shutdown. Remove embedded API/enrollment secrets from packaging.
- Accept: fresh install/start/reboot/stop/uninstall on disposable Windows/Linux under service identity, config failure closes safely, shutdown waits for collection and workers. V13/V20, L01/L07/L16/L19.

## P1.09: immutable spool segments and idempotent bounded transport
- Depends on: P1.08. Targets: `agent/spool.go`, `agent/agent.go`, ingest DTOs/handlers, both databases.
- Seal segments under lock before drain; process ownership lock, atomic replacement and durable checkpoints. Decouple collection from bounded async batches; context-aware delivery/backoff. Add boot/sequence/event IDs, receive times, strict semantic validation and durable deduplication. Persist overflow/loss reasons and caps; never acknowledge an event before its promised durable state.
- Accept: append-during-drain, disk-full, torn write, eviction, restart, two processes, duplicate event with changed content, uint overflow, future data and lost ACK. No second accepted event after replay; no unnoticed dropped data. V16, L06/L07.

## P1.10: fair budgets, health and foundation review
- Depends on: P1.09. Targets: quota/rate middleware, health, storage indexes, CI, docs.
- Separate pre-auth abuse limits from accepted-data entitlements; shared atomic accepted-row/byte accounting, per-agent fairness, bounded queries/exports, cardinality caps and periodic cleanup. Explicit config initialization, not package-init quota capture. Constant-time liveness; dependency readiness; expected-agent health separately authorized. Protect `/metrics` through the real proxy route. Add security/business audit events and release gates.
- Accept: malformed/rejected requests do not consume accepted-data allowance; restart/replicas do not reset budgets; noisy tenant and cold health requests meet measured resource bounds. All G1 fixtures pass; publish remaining findings honestly. V15/V17, L08/L12/L14/L17.

## G1 exit gate and handoff

Require actual SQLite/PG security integration tests, route matrix, warmed-cache tests, native OS service/ACL tests, browser tests, spool fault tests, cancellation/abuse tests and selected-module/binary scans. Any critical identity, data-loss, executable-export or insecure-default path blocks progression. Qualified reviewer signs off changes; production rollout separately requires verified backups/rollback and authorized secret handling. Unsafe retention remains disabled until P2.03; this is explicit temporary containment, not V07 closure. No public shared-tenant launch yet.

Rollback: code rollback may only run against a documented compatible schema. For identity/schema changes use a tested forward repair or verified maintenance-window restore; never blindly downgrade into legacy credentials or destructive retention. Record compatibility per PR.

### Copy-paste start prompt

> Read 00-SHARED-CONTRACT.md and 01-PHASE-1.md. Implement only P1.01 against the current checked-out commit. Inspect the named files and V22 before running any broad tests. Isolate production credential paths first. Show real focused-test results and the handoff record, then stop. Do not start P1.02, touch customer machines, or claim findings closed without evidence.
