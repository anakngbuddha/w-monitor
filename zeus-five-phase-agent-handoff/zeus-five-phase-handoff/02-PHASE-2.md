# Phase 2 of 5: trustworthy infrastructure and application evidence

**Outcome:** finite, consented campaigns with complete-or-explicitly-incomplete Layer 1/2 data. **Entry:** G1 approved. **Exit:** G2. No inference calls in this phase. New paths below are proposed.

## P2.01: versioned migrations, capability and event contracts
- Depends on: G1. Targets: `storage/db.go`, `storage/postgres.go`, new `storage/migrations/`, `schemas/`, focused repository interfaces.
- Version/checksum migrations, fail-fast errors, cross-instance lock and backup/restore compatibility. Use appropriate wide IDs and composite scoped indexes/foreign keys. Assert effective SQLite journal/timeout/foreign-key settings. Implement shared event, capability-status and inventory-page schemas; publish OpenAPI and mock fixtures.
- Accept: upgrade a copy of the baseline schema, repeat safely, inject migration failure, concurrently start replicas; no partial schema accepted. Same contract tests pass in real SQLite/PG. L10.

## P2.02: assessment campaigns and frozen membership
- Depends on: P2.01. New targets: `assessment/run.go`, `server/assessment_runs.go`, run/asset/assignment tables.
- Explicit tenant/project/owner/consent/asset scope/start/end/timezone/expected cadence; restart-safe finite schedule, pause/resume and missed-run status. UTC half-open time windows, DST-safe display/business schedules. Controlled baseline/follow-up asset links, not hostname matching. Agents ingest only authorized assignments.
- Accept: timezone/DST boundaries, reboot/pause/end, absent expected agents and overlapping runs cannot leak or duplicate events. Frozen memberships are immutable; corrections create linked versions. F01/F07.

## P2.03: safe retention and measurement semantics
- Depends on: P2.02. Targets: collector/delta/retention and both backends.
- Preserve raw evidence for the assessment window or explicitly approved equivalent statistical representation; typed separate rollups preserve ownership, duration/count/extrema/capacity/reset metadata. Late-arrival policy and retention pinning protect frozen inputs. Unknown measurement stays null/status, not zero. Never reconstruct exact percentiles from hourly averages or average PIDs/counters.
- Accept: identical raw fixtures yield backend-equivalent results; repeated retention is idempotent; late data, counter resets, missing hours and capacity changes preserve invariants. Back up before enabling corrected retention. V07/L02 closure requires these runtime results, not merely the Phase 1 kill switch.

## P2.04: per-volume infrastructure and capability quality
- Depends on: P2.03. Targets: `collector/collector.go`, `collector/delta.go`, storage DTOs, quality presentation.
- Collect supported volumes/mounts/filesystems/topology and scoped IOPS/throughput/latency only where available. Persist errors, observation duration and permission/unsupported status. Separate device/network scopes and label remote-IP/default-route-interface observations honestly. Add bounded overhead instrumentation.
- Accept: multi-volume Windows/Linux, changing devices, permission loss/recovery and mixed physical/virtual interfaces; absent metrics cannot become zeros or fabricated billable egress. L04/L05, F02.

## P2.05: inventory core and Linux collectors
- Depends on: P2.04. New `collector/inventory/` with OS-specific implementations, fake OS adapters, inventory DTOs.
- Separate full bounded inventory from existing top-20 processes; process start identity, services/listeners, approved package/runtime metadata with source and version. No execution of discovered binaries. Startup/six-hour inventory and five-minute process/listener cadence are tunables; full baselines plus deltas/tombstones and paged completeness.
- Accept: process/PID reuse, stopped services, no package manager, denied metadata, huge inventories, lost/reordered pages and cadence under slow Hub. Partial discovery displays scope/gaps, never total installed-software claims. Layer 2.

## P2.06: Windows services/software/runtime/IIS inventory
- Depends on: P2.05 contracts. New OS adapters and native Windows fixtures.
- Services and 32/64-bit installed-software registry, runtime metadata, optional read-only IIS sites/app pools/bindings. Publish per-user/container/IIS visibility and permissions. No `Win32_Product`, arbitrary WMI commands or secret-bearing application-pool configuration uploads.
- Accept: IIS absent/present/denied, runtime side-by-side versions, registry views, service account vs interactive user and non-ASCII names. Native Windows tests required; cross-compilation is insufficient. Layer 2.

## P2.07: privacy-controlled config references and lifecycle catalog
- Depends on: P2.06. New `collector/configscan/`, `assessment/lifecycle/`, redaction fixtures and policy settings.
- Implement disabled-by-default allowlisted scans with bounded matching, symlink containment and pre-spool redaction. Emit finding categories and approved endpoint aliases, never raw file content/DSNs. Add versioned sourced OS/runtime lifecycle catalog with edition/support-channel/extended-support applicability and unknown results.
- Accept: planted passwords, query tokens, encodings, multiline DSNs, huge files and symlink escapes do not leak through any output; unsupported lifecycle entries remain unknown. Consent withdrawal stops future scans and follows retention/deletion policy for prior evidence. F10, Layer 2.

## P2.08: inventory view and campaign completeness gate
- Depends on: P2.07. Targets: dashboard, run summary API and no-AI result/export skeleton.
- Show expected/observed assets, inventory freshness, capabilities/permissions, gaps, collection overhead, consent and run status. Deterministic quality rules specify required business-cycle duration, minimum per-metric coverage and maximum gaps; all thresholds versioned/configured. Initial example policy: at least 14 calendar days and 95% coverage for sizing-required metrics, plus declared critical business events. This is a proposed policy, not a universal sufficient sample.
- Accept: a month-end workload with no month-end evidence stays insufficient despite 14 days; a reporting Hub cannot hide missing agents; excluded collection appears in every export. Finalize pins the accepted evidence cutoff and manifest. F01/F02/F07, L12.

## G2 exit gate and handoff

A consented Windows/Linux fixture fleet completes a finite campaign with durable paged inventory and explicit unknowns; lifecycle/scan privacy tests and backend retention parity pass; signed-off manifests survive subsequent retention. This phase proves collection/evidence sufficiency, not supported Huawei target sizing. Replaying network backlog cannot extend the run window silently. Unproven OS capabilities remain labeled unsupported.

Rollback: additive inventory schemas retained; disable new capabilities at policy level and stop scans without deleting prior evidence. Do not restore unsafe legacy retention. Destructive storage cleanup needs operator approval and tested restore.

### Copy-paste start prompt

> Read 00-SHARED-CONTRACT.md, this phase file, and the G1 handoff. Implement only P2.01. Treat schemas and paths as proposed, inspect current code first, and run real SQLite/PostgreSQL migration fixtures. If G1 evidence is missing, stop and report the blocker. Produce the ticket handoff and do not continue automatically.
