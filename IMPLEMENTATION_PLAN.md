# W-Monitor Hardening & Accuracy Plan (Phase 13)

Status: proposed
Created: 2026-08-21
Scope: correctness of collected metrics, hub auth, agent durability, server robustness, alerting, query performance.

This plan is ordered by risk. Work top to bottom. Every item lists the target file,
the defect, the fix, and how to prove it is fixed.

---

## Priority Legend

| Tag | Meaning |
|-----|---------|
| P0 | Data is wrong or the hub is open to abuse. Fix now. |
| P1 | Silent data loss or operational blind spots. |
| P2 | Scale, polish, and features. |

---

# Track A: Hub Auth & Tenancy (P0)

The repo is going private, so key rotation is out of scope. The auth *logic* is still
broken independently of repo visibility and must be fixed.

### A1. Hub accepts any non-empty API key

**File:** `server/server.go` (`EnableHubMode`, `handleIngest`, `handleMetrics`, `handleProcesses`, `handleServers`, `handleExportCSV`)

**Defect:** `EnableHubMode(_ string)` discards the key argument entirely. Every handler
treats whatever arrives in `X-API-Key` as a valid tenant ID. Any caller can invent a key,
write metrics into a brand-new tenant, and read them back. There is no authentication,
only tenant partitioning by an attacker-chosen string.

**Fix:**
1. Add an `api_keys` table to both backends:
   `id, tenant_id, key_hash (sha256 hex), client_name, created_at, revoked_at, last_seen_at`.
2. Add `storage.Store` methods: `ResolveAPIKey(hash string) (tenant string, ok bool, err error)`
   and `TouchAPIKey(hash string) error`.
3. Replace the copy-pasted key-reading block in all five handlers with one
   `func (s *Server) authTenant(r *http.Request) (string, bool)` helper that hashes the
   presented key, looks it up, and returns 401 on miss. Use `subtle.ConstantTimeCompare`
   semantics via hash equality.
4. Cache resolved keys in memory with a 60s TTL so ingest does not hit the DB per row.
5. Migrate `clients_registry.csv` into the table via a one-time
   `wmonitor -import-clients <csv>` command, then delete the CSV from the working tree.
   Store only hashes going forward: the plaintext key is shown once at generation time.
6. Drop `api_key` as a query parameter. Header only, so keys stop landing in access logs
   and browser history.

**Proof:** new `server/auth_test.go`: unknown key returns 401 on all five endpoints;
revoked key returns 401; valid key returns 200 and sees only its own rows; key in query
param is rejected.

### A2. No rate limiting or body size cap on ingest

**File:** `server/server.go` (`handleIngest`)

**Defect:** unbounded `json.NewDecoder(r.Body)` and unlimited request rate. One client can
fill the hub's Postgres.

**Fix:** wrap body in `http.MaxBytesReader` (256 KB is generous for a batch), add a
per-tenant token bucket (`golang.org/x/time/rate`, e.g. 20 req/s burst 60) keyed by
tenant ID, return 429 with `Retry-After` when exceeded. Add a per-tenant daily row quota
checked on ingest.

**Proof:** `server/ratelimit_test.go` drives 200 rapid posts and asserts 429s appear and
that a second tenant is unaffected.

### A3. Wide-open CORS

**File:** `server/server.go` (every `Access-Control-Allow-Origin: *`)

**Defect:** `*` on authenticated, tenant-scoped JSON endpoints.

**Fix:** allowlist via `WMONITOR_ALLOWED_ORIGINS` (comma-separated). Echo the origin only
when it matches, omit the header otherwise. Default to no CORS header when the var is unset.

### A4. Committed binaries and secrets in the tree

**Files:** `wmonitor_WSI.exe` (16 MB), `wmonitor_WSI_linux` (15 MB), `clients_registry.csv`

**Defect:** 31 MB of build artifacts in git, plus a plaintext key registry. Keys are baked
into those binaries via `-ldflags`, so the artifacts are themselves secrets.

**Fix:** add `*.exe`, `wmonitor_*_linux`, and `clients_registry.csv` to `.gitignore`.
`git rm --cached` all three. Ship client binaries through GitHub Releases instead, built
by CI (Track E). History rewrite is optional now that the repo is private, but the files
should leave `main`.

---

# Track B: Metric Accuracy (P0)

These are the bugs that make the dashboard lie.

### B1. Per-process CPU% is measured since process start, not since last poll

**File:** `collector/collector.go` (`collectTopProcesses`)

**Defect:** `p.CPUPercent()` returns cumulative CPU time divided by process lifetime. A
process that pinned a core for an hour at boot and has been idle since still reports high.
The "top 20 by CPU" list is therefore not a snapshot of current load, and long-lived
processes systematically outrank genuinely busy short-lived ones.

**Fix:** keep a `map[int32]procSample{createTime, cpuTimeTotal, sampledAt}` on the
`Collector`. Each tick, read `p.Times()` and compute
`delta_cpu_seconds / (wall_seconds * numCores) * 100`. Key the map on `pid + createTime`
so PID reuse cannot corrupt a delta. Skip a process on its first sighting (no baseline
yet) and prune entries for PIDs that disappeared. Sort *after* deltas are computed.

**Proof:** `collector/process_cpu_test.go` with a fake sampler: constant CPU time across
ticks yields 0%, one core fully consumed for the whole interval yields ~100/numCores.

### B2. Counter reset fabricates enormous spikes

**File:** `collector/collector.go` (delta block in `collect`)

**Defect:** on wrap or NIC/disk counter reset the code does `delta = current` instead of
detecting the reset. A reboot or NIC reset produces a single sample claiming gigabytes per
second, which then poisons every average and max in the assessment report.

**Fix:** when `current < previous`, emit `0` for that interval, log at debug, and reseed
the baseline. Extract to `func counterDelta(cur, prev uint64) (delta uint64, reset bool)`
and use it for all four counter pairs.

**Proof:** table test over `counterDelta` covering normal, equal, and reset cases.

### B3. Internal/external network split is dead code

**Files:** `collector/collector.go`, `storage/db.go`, `storage/postgres.go`, `server/server.go`, `dashboard/`

**Defect:** `prevExtSent`, `prevExtRecv`, `prevIntSent`, `prevIntRecv` are assigned every
tick and never read. The four `net_*_external` / `net_*_internal` columns store raw
since-boot totals, not rates, so any chart drawn from them is a monotonic ramp rather than
traffic. Phase 10 is half-landed.

**Fix:** compute `net_mbps_external` and `net_mbps_internal` from the stored previous
counters using `counterDelta` from B2, add those two columns, and keep the cumulative
values for auditing. Surface both rates in `/api/metrics` and the dashboard.

### B4. External NIC detection is a guess

**File:** `collector/collector.go` (`detectExternalIface`)

**Defect:** "the interface with the most bytes" is a heuristic that the comment itself
admits is not a real implementation. On a host with a busy internal/storage NIC it picks
the wrong one, silently inverting the internal/external split. Loopback skipping is a
hardcoded two-name string match that misses `lo0`, `Loopback Pseudo-Interface 2`, etc.

**Fix:** resolve the interface holding the default route:
- Linux: parse `/proc/net/route` for destination `00000000`.
- Windows: `GetBestInterfaceEx` via `iphlpapi.dll`, or parse `route print 0.0.0.0`.
- Fall back to dialing a UDP socket at `8.8.8.8:80` (no packets sent) and matching
  `LocalAddr().IP` against `net.Interfaces()`. This is portable and works today.
Use `net.Interface.Flags & FlagLoopback` instead of name matching. Cache the result and
re-detect every 5 minutes. Log the chosen NIC at startup so operators can spot a bad pick,
and keep `-external-iface` as the override.

**Proof:** `collector/iface_test.go` asserts detection returns a non-loopback interface that
exists in `net.Interfaces()`.

### B5. Blocking CPU sample causes interval drift

**File:** `collector/collector.go` (`collect`)

**Defect:** `cpu.Percent(time.Second, false)` blocks for a full second inside a 10s ticker,
so real spacing is ~11s and every rate denominator is off by 10%. The first sample also
arrives 10s after start because the ticker fires before the first collect.

**Fix:** switch to the non-blocking form `cpu.Percent(0, false)`, which deltas against the
previous call: exactly the cadence we already have. Prime it with one throwaway call at
startup. Call `collect()` once immediately before entering the ticker loop. Continue using
measured `elapsed` (already correct) for all rate math.

**Proof:** `collector/collector_test.go` asserts three ticks at 200ms land within a 700ms
budget and that a sample exists before the first tick.

### B6. Disk coverage is root-partition-only and IOPS double-counts devices

**File:** `collector/collector.go`

**Defect:** `disk.Usage("/")` then `C:\` as fallback means a full data volume is invisible.
`disk.IOCounters()` sums every device including loop/ram/virtual devices and both a
partition and its parent disk, inflating IOPS.

**Fix:** enumerate `disk.Partitions(false)`, skip pseudo filesystems
(`tmpfs`, `devtmpfs`, `overlay`, `squashfs`, `proc`, `sys`), and store per-mount usage in a
new `disk_usage` table (`timestamp, tenant_id, server_id, mount, total_gb, free_gb, used_pct`).
Keep `disk_free_gb` / `disk_total_gb` on `metrics` as the root volume for backward
compatibility. For IOPS, filter to physical devices only (`nvme*`, `sd*`, `vd*`,
`xvd*`, `PhysicalDrive*`) via an allowlist regex, and add
`disk_read_bytes_ps` / `disk_write_bytes_ps` plus average queue latency from
`IOCounters().WeightedIO`.

### B7. Two disagreeing concurrent-user implementations

**Files:** `collector/usertracker.go`, `server/server.go` (`trackRequest`, `GetConcurrentUsers`)

**Defect:** `Server` implements `GetConcurrentUsers()` by counting dashboard viewer IPs and
`TCPUserTracker` counts inbound TCP peers. Only the latter is wired to the collector
(`collector.New` always installs `NewTCPUserTracker()`), so the server's implementation is
unreachable dead code that nonetheless satisfies the same interface. Two definitions of
"user" in one binary is a bug waiting to be wired up wrongly.

Accuracy problems in the TCP tracker:
- `CLOSE_WAIT`, `FIN_WAIT1`, and `FIN_WAIT2` are counted as active. Those are closing
  sockets and inflate the count, badly on Windows where `CLOSE_WAIT` lingers.
- Every client behind one NAT collapses to a single user, so an entire office counts as 1.
- With no `-app-port` set, it treats *every* non-DB listening port as an app port, so
  SSH/RDP/SMB sessions and monitoring pollers count as users.
- The 60s window is a fixed constant with no accessor.

**Fix:**
1. Delete `Server.trackRequest` / `Server.GetConcurrentUsers`. If dashboard viewer counts
   are wanted, expose them as a separate `dashboard_viewers` metric with its own name.
2. Count only `ESTABLISHED` and `SYN_RECV`.
3. Record `remoteIP:remotePort` as the connection key and report both
   `active_connections` (distinct sockets) and `concurrent_users` (distinct IPs). Two
   honest numbers beat one ambiguous one.
4. When `appPorts` is empty, log a loud warning that the count is a rough upper bound, and
   extend the exclusion list beyond DB ports (22, 3389, 445, 139, 135, 5985, 5986).
5. Add `SetWindow(time.Duration)` and a `-user-window` flag.

**Proof:** `collector/usertracker_test.go` with an injectable connection lister: asserts
`CLOSE_WAIT` is ignored, two ports from one IP yield 2 connections / 1 user, and entries
expire after the window.

---

# Track C: Agent Durability (P1)

### C1. Metrics are lost forever after two failed attempts

**File:** `agent/agent.go` (`post`)

**Defect:** two attempts, fixed 2s sleep, then the row is dropped and the caller only logs.
A 30-second hub deploy or a flaky link silently punches a hole in the client's history, and
nothing anywhere records that data is missing.

**Fix:** add a bounded on-disk spool at `DataDir()/spool/` (newline-delimited JSON,
segment-rotated, default cap 100 MB with oldest-segment eviction). On post failure, append
and return nil. A background drainer retries with exponential backoff plus jitter
(1s to 5m) and honours `Retry-After` on 429. Expose spool depth on `/api/health`.

**Proof:** `agent/spool_test.go`: hub down for N ticks then up, assert every row eventually
arrives exactly once and the spool empties.

### C2. One HTTP request per row (21 posts per tick, per agent)

**Files:** `agent/agent.go`, `collector/collector.go`, `server/server.go` (`handleIngest`)

**Defect:** the collector calls `InsertMetric` once and `InsertProcess` 20 times per tick.
In agent mode that is 21 sequential HTTPS round trips every 10 seconds, each with a 15s
timeout. Under latency a single tick can outlast the interval, and the collector blocks on
network I/O it should not care about.

**Fix:** add `InsertBatch(snapshot Snapshot) error` to `storage.Store` where `Snapshot`
holds one `MetricRow` plus its `[]ProcessRow`. Collector emits one batch per tick. Agent
posts it to `/api/ingest?type=batch` gzipped. Hub decodes and writes in a single
transaction. Keep the single-row endpoints for one release for compatibility, then remove.

**Result:** 21 requests per tick becomes 1, and process rows land atomically with their
parent metric row instead of partially on error.

### C3. Agent has no identity beyond hostname

**Files:** `main.go` (`runAgentMode`), `agent/agent.go`

**Defect:** `server_id` is `os.Hostname()`. Two clients with a machine named `WIN-SERVER`
merge into one series inside a tenant, and a hostname change orphans all history.

**Fix:** generate a UUID on first run, persist at `DataDir()/agent_id`, send it as
`server_id` and hostname as a display label. Add `-server-id` to override.

### C4. No agent version or heartbeat reported

**Fix:** inject `version` and `commit` via `-ldflags` in `build_release.ps1`, send them as
`X-Agent-Version`, and store `last_seen_at` / `agent_version` in a new `servers` table on
every ingest. This is the data Track D's staleness alert depends on.

---

# Track D: Server Robustness & Alerting (P1)

### D1. No HTTP timeouts and no graceful shutdown

**File:** `server/server.go` (`Start`)

**Defect:** bare `http.ListenAndServe` has no `ReadTimeout`, `WriteTimeout`, or
`IdleTimeout`, which is textbook Slowloris exposure: a handful of slow-header connections
exhaust the server. `Stop` cancels the collector context but never shuts the HTTP server
down, so in-flight CSV exports are killed mid-stream on service stop.

**Fix:** construct an explicit `&http.Server{ReadHeaderTimeout: 5s, ReadTimeout: 30s,
WriteTimeout: 120s, IdleTimeout: 120s, MaxHeaderBytes: 1<<20}`. Add
`func (s *Server) Shutdown(ctx context.Context) error` calling `srv.Shutdown`, and call it
from `program.Stop` and the foreground signal handler with a 10s grace period. Fix the
foreground path's `os.Exit(0)`, which currently skips shutdown entirely.

### D2. It is a monitor that cannot tell you anything is wrong

**New package:** `alerting/`

**Defect:** the product collects, stores, charts, and exports, but never notifies. Nobody
watches a dashboard at 3am. This is the single biggest functional gap.

**Fix:**
- Rules in `alerts.yaml` (or an `alert_rules` table for hub mode):
  `metric, comparison, threshold, for_duration, severity, targets`.
- Evaluator goroutine on a 30s tick, reading recent rows via `Store`.
- Hysteresis: fire only after `for_duration` sustained breach, resolve after an equal
  period below threshold, so flapping does not spam.
- Sinks: webhook (generic JSON), SMTP email, and Slack-format webhook. Interface
  `Notifier{ Send(Alert) error }` so more can be added.
- `alert_events` table plus `GET /api/alerts` and a dashboard banner.
- Ship sane defaults: CPU > 90% for 5m, mem > 90% for 5m, disk free < 10%, disk free < 5%
  (critical), agent not seen for 3 intervals.

### D3. A dead agent looks exactly like an idle agent

**Files:** `alerting/`, `server/server.go`

**Defect:** the only evidence of a live agent is rows arriving. If an agent crashes, the
charts simply stop advancing, and nothing distinguishes that from a quiet server.

**Fix:** using the `servers` table from C4, alert when `now - last_seen_at > 3 * interval`.
Render stale servers greyed out with a "last seen" timestamp in the dashboard server picker.

### D4. `/api/health` is not a real health check

**File:** `server/server.go` (`handleHealth`)

**Defect:** it swallows both DB errors (`mc, _ := s.db.CountMetrics()`) and always returns
`status: "ok"` with HTTP 200, even when the database is unreachable. `COUNT(*)` on an
unbounded metrics table also gets slower forever, so the health check degrades as the
product succeeds.

**Fix:** run `db.PingContext` with a 2s timeout, return 503 and `status: "degraded"` on
failure. Replace the counts with `last_metric_age_seconds`, `spool_depth`, `uptime_seconds`,
`version`. Add `/api/ready` for orchestrators and `/metrics` in Prometheus text format.

### D5. Retention never runs on Postgres

**Files:** `main.go`, `retention/retention.go`, `storage/postgres.go`

**Defect:** `retention.New(sqliteDB.Conn())` is only constructed when `sqliteDB != nil`, and
`main.go` says outright that Postgres has no retention. The hub, the one deployment that
accumulates data from many agents, is the one that never prunes. It grows until Aiven
fills up.

**Fix:** move pruning behind the `Store` interface as
`PruneBefore(t time.Time, table string) (int64, error)`, implement for both backends, and
construct the retention job unconditionally. Delete in batches
(`DELETE ... WHERE id IN (SELECT id ... LIMIT 10000)`) so a large first run does not lock
the table. Make windows configurable via `WMONITOR_RETENTION_RAW_DAYS` /
`_ROLLUP_DAYS`. Run `VACUUM`/`ANALYZE` after large SQLite prunes.

### D6. Silent failures throughout the collector

**File:** `collector/collector.go` (`collect`)

**Defect:** ~8 `log.Printf` error paths continue with zero values, so a failed memory read
writes `mem_pct = 0` rather than recording "unknown". Zeros are indistinguishable from real
readings and drag every average down.

**Fix:** make the affected `MetricRow` fields nullable (`*float64`) or add a
`collect_errors TEXT` column listing which subsystems failed. Add a
`wmonitor_collect_errors_total` counter to `/metrics`. Never persist a fabricated zero as if
it were measured.

---

# Track E: Scale, Query Performance, CI (P2)

### E1. Queries load every row into memory

**Files:** `storage/db.go`, `storage/postgres.go`, `server/server.go`, `export/`

**Defect:** `QueryMetrics` returns `[]MetricRow` with no `LIMIT`. At 10s resolution one
server produces ~259k rows per 30 days: `range=30d` on a 20-agent hub materialises ~5M
structs and JSON-encodes all of them. `server_id` filtering happens in Go *after* the full
fetch, so filtering to one server still pays for all of them.

**Fix:**
1. Push `server_id` into the SQL `WHERE` clause. Change the signature to
   `QueryMetrics(q MetricQuery)` with `Since, Until, TenantID, ServerID, Limit, Bucket`.
2. Add composite indexes: `(tenant_id, server_id, timestamp)` on both tables, plus
   `(tenant_id, timestamp)` on `processes` (which today has only `timestamp`).
3. Add rollup tables `metrics_1m`, `metrics_5m`, `metrics_1h` populated by the retention
   job (avg/min/max/p95 per bucket). Auto-select granularity by requested range: 24h reads
   raw, 7d reads 5m, 30d reads 1h. Target under 2000 points per chart response.
4. Cap raw responses with a documented default `limit=5000` and downsample above it.

**Proof:** benchmark asserting `range=30d` on a 1M-row table responds in under 500ms and
under 50 MB allocated.

### E2. No CI

**Defect:** `collector`, `server`, `storage`, `agent`, and `retention` all have test files
and nothing runs them. Regressions land unnoticed.

**Fix:** `.github/workflows/ci.yml` running on push and PR: `go vet`, `go test -race
./...`, `staticcheck`, `gofmt -l` gate, plus `govulncheck`. Matrix on
`ubuntu-latest` / `windows-latest` since the collector is platform-sensitive. A second
`release.yml` builds cross-platform binaries on tag and attaches them to a GitHub Release
(replacing the committed artifacts from A4).

### E3. Config handling is ad hoc

**File:** `main.go` (the ~40-line flag/env reconciliation block, `loadConfigEnv`)

**Defect:** precedence is implemented by comparing flags against their default values, so
`-port 8080` typed explicitly is indistinguishable from the default and gets silently
overridden by `WMONITOR_PORT`. The hand-rolled `.env` parser has no quoting or escape
handling.

**Fix:** use `flag.Visit` to detect explicitly-set flags, or move to a single `Config`
struct with explicit precedence (flag > env > config.env > build default). Add
`wmonitor -print-config` to dump the resolved config with secrets masked.

### E4. Structured logging

**Defect:** `log.Printf` with `[prefix]` strings everywhere is unqueryable in aggregation.

**Fix:** move to `log/slog` with JSON output when `WMONITOR_LOG_FORMAT=json`, carrying
`server_id` and `tenant_id` as standard attributes. Keep human-readable text as the default.

### E5. Feature backlog (post-hardening)

- Per-metric anomaly baselines (rolling mean and stddev) so "unusual for this host" beats a
  fixed threshold.
- Capacity forecasting: linear regression on disk free to project days-to-full in the
  assessment report.
- Process grouping by name so 40 chrome PIDs read as one line item.
- Service and port up/down checks (TCP dial and HTTP status) as first-class monitored objects.
- Dashboard: server comparison view, dark mode, shareable time-range URLs.
- Systemd unit and Docker image for Linux hub deployment.
- Windows: pull `Get-Counter` / WMI for accurate per-core and per-disk queue length.

---

# Execution Order

| Step | Items | Rationale |
|------|-------|-----------|
| 1 | A1, A2, A3, A4 | Close the open hub before anything else ships. |
| 2 | B2, B5, B1 | Cheap, self-contained accuracy wins. B1 is the big one. |
| 3 | E2 | CI in place before the wider refactors land. |
| 4 | B4, B7, B3, B6 | Correctness work that touches the schema. |
| 5 | C2, C1, C3, C4 | Batching first, then durability on top of it. |
| 6 | D1, D4, D5, D6 | Operational hardening. |
| 7 | D2, D3 | Alerting, which depends on C4's heartbeat data. |
| 8 | E1 | Rollups, best done once schema changes have settled. |
| 9 | E3, E4, E5 | Polish and features. |

## Schema Migration Notes

Steps 4, 5, 7, and 8 all add columns or tables. The current `migrate()` approach
(`CREATE TABLE IF NOT EXISTS` plus a list of `ALTER TABLE` statements whose errors are
deliberately ignored) will not survive this. Before step 4, introduce a
`schema_migrations` table with numbered, ordered, error-checked migrations shared by both
backends. Ignoring migration errors means a genuinely failed migration is indistinguishable
from an already-applied one.

## Definition of Done

- `go test -race ./...` green on Linux and Windows in CI.
- Unknown API key cannot read or write hub data.
- Process CPU percentages match Task Manager / `top` within a few points.
- Hub restart during collection loses zero samples.
- A CPU threshold breach delivers a notification within 60s.
- `range=30d` on a multi-agent hub responds in under 500ms.
- No build artifacts or plaintext secrets tracked in git.
