# Zeus (w-monitor) Implementation Plan: Multi-Server Assessment, Aiven Backend & Migration Reports

## Purpose

Zeus is being extended from a single-machine local monitor into a **pre-migration
assessment tool**: install an agent on a client's existing on-prem/cloud servers
(with no cloud tooling of their own), collect utilization data over an assessment
period, and produce a report that supports a cloud migration/sizing proposal.

Key constraint driving the design: **client environments vary**. Some have an
on-prem load balancer (nginx/HAProxy), some don't (DNS round-robin or a single
server). Some have separate public/internal NICs (the common case), some don't.
The tool needs to work across these without per-client code changes — only
per-client **configuration**.

Second constraint, settled in this revision: **client machines must never touch
Aiven credentials.** Only the Hub talks to Postgres. Agents authenticate to the
Hub with a simple shared API key, not a database password. This keeps install
on client servers to a single command with no manual credential handling.

---

## Phase 0 — Aiven Postgres setup

- [ ] Sign up at aiven.io, create a free-tier PostgreSQL service
- [ ] Retrieve connection string (host, port, db, user, password, `sslmode=require`)
- [ ] Verify connectivity with `psql` or a GUI client (DBeaver) before writing Go code

## Phase 1 — Storage abstraction

**Files:** `storage/store.go` (new), `storage/db.go` (refactor)

- [ ] Define a `Store` interface: `InsertMetric`, `InsertProcess`, `QueryMetrics`,
      `QueryProcesses`, `CountMetrics`, `Close`
- [ ] Make existing `*storage.DB` (SQLite) satisfy this interface — no behavior change
- [ ] Update `main.go`, `collector`, `server`, `retention` to depend on the
      interface type instead of `*storage.DB` directly

## Phase 2 — Postgres backend

**File:** `storage/postgres.go` (new)

- [ ] Implement `Store` using `jackc/pgx` (preferred over `lib/pq` — actively maintained)
- [ ] Port schema from `db.go`'s `migrate()`:
  - `?` placeholders -> `$1, $2...`
  - `INTEGER PRIMARY KEY AUTOINCREMENT` -> `SERIAL PRIMARY KEY`
- [ ] Include `server_id`/`hostname` column from the start (Postgres is the
      multi-server path)

## Phase 3 — Backend selection & credential handling

**File:** `main.go`

- [ ] Add `-db=sqlite|postgres` flag (default `sqlite`, no config needed —
      preserves today's zero-config local behavior)
- [ ] DSN is **never** taken as a bare CLI flag value in normal use. Read order:
  1. `WMONITOR_DB_DSN` environment variable (primary method — set once, on the
     Hub machine only, via `[Environment]::SetEnvironmentVariable(...)`)
  2. Optional `-dsn-file=<path>` flag pointing to a config file (see below),
     for cases where an env var isn't practical
  3. A raw `-dsn=` flag stays available only as a documented "quick local test"
     fallback, with a clear `USER_GUIDE.md` warning not to use it for anything
     beyond a one-off test (visible in process list / shell history)
- [ ] When logging startup info, **never log the DSN as-is** — parse out
      host/db name only for the log line, strip user/password before printing
      (applies to the existing `log.Printf("[wmonitor] DB path: %s", dbPath)`
      pattern, extended for the Postgres case)

## Phase 4 — Schema parity for SQLite

**File:** `storage/db.go`

- [ ] Add `hostname`/`server_id` column to SQLite schema too via
      `ALTER TABLE ... ADD COLUMN` (matches existing migration pattern) —
      keeps both backends schema-compatible

---

## Phase 5 — Agent mode (no DB credentials involved, ever)

**File:** `main.go`, new `agent/` package

- [ ] Add `-agent=<hub-url>` flag and `-api-key=<key>` flag (or read
      `WMONITOR_API_KEY` env var — same "env var primary, flag as fallback"
      pattern as Phase 3)
- [ ] Collector still runs locally (`collector.Run`, unchanged), but instead of
      writing to a local `Store`, POSTs each `MetricRow`/`ProcessRow` as JSON to
      `<hub-url>/api/ingest`, with the API key in a request header
      (`X-API-Key`), over HTTPS
- [ ] No local dashboard, no local DB, **no Postgres/Aiven credentials of any
      kind** required on agent machines — the agent only ever knows the Hub's
      address and the shared API key

## Phase 6 — Hub mode

**File:** `server/server.go`

- [ ] Add `-hub` flag
- [ ] New handler: `POST /api/ingest` — validates `X-API-Key` header against
      `WMONITOR_API_KEY` (constant-time comparison), accepts JSON metric/process
      payloads with `server_id`, writes to the configured `Store`
- [ ] Reject requests with missing/invalid key (401) before touching the DB
- [ ] Hub keeps serving its normal dashboard alongside the new ingest endpoint

## Phase 7 — Dashboard: server selector

**Files:** `dashboard/static/*`

- [ ] New `/api/servers` endpoint — distinct `server_id`s seen in the DB
- [ ] Dropdown to filter charts/tables by server, plus an "All servers" aggregate view

---

## Phase 8 — Install script: parameterized, no manual credential steps

**Files:** `install.ps1`, `install.sh`

This is the phase that actually removes the "hassle" of setting things up
per client machine — agents get a single install command with no manual
PowerShell env-var steps.

- [ ] Add a `param()` block to `install.ps1` (and equivalent flag parsing to
      `install.sh`):
  ```powershell
  param(
      [string]$Mode = "agent",     # "agent" or "hub"
      [string]$HubUrl = "",
      [string]$ApiKey = "",
      [string]$Dsn = ""            # only used when -Mode hub -Db postgres
  )
  ```
- [ ] Script writes these into a config file under the existing `DataDir()`
      location (e.g. `%LOCALAPPDATA%\Sysmon\config.env`), **not** a system-wide
      env var, so nothing needs to be typed by hand on the target machine
- [ ] Lock the config file down with `icacls` (Windows) / `chmod 600` (Linux)
      so only the service account can read it
- [ ] Add `config.env`-style paths to `.gitignore` so a config file can never
      accidentally get committed if the tool is ever run from inside a cloned
      repo directory
- [ ] Example client-facing install command, once this phase is done:
  ```powershell
  .\install.ps1 -Mode agent -HubUrl "https://<hub-address>:8080" -ApiKey "<generated-key>"
  ```
  One command, no manual credential handling on the client machine, and the
  client machine never sees the Aiven DSN at all.

### Where `<hub-address>` and `<generated-key>` actually come from

Neither is issued by a third party — both are decided by you, once, before
running any installs:

- **Hub address**: wherever you choose to run the Hub (see Phase 9 below) —
  a LAN IP if testing locally, or a public IP/domain if the Hub runs on a
  cloud VM
- **API key**: a random string you generate yourself (e.g. a 32-char random
  hex value), set as `WMONITOR_API_KEY` on the Hub once, then reused as the
  `-ApiKey` value for every agent install

## Phase 9 — Hub deployment decision (operational, not code)

No code change here — this is a per-engagement/ops decision, documented so
it's not re-litigated every time:

- [ ] **Default for early engagements:** run the Hub on your own laptop for
      the duration of the assessment. Zero extra infrastructure, works over
      any internet connection since Aiven is already cloud-hosted.
- [ ] **If an always-on Hub becomes worth it** (multiple concurrent
      engagements, or wanting a persistent dashboard): move the Hub to a
      small VM — check Huawei Cloud partner/sandbox access first, fall back
      to Oracle Cloud's Always Free tier (genuinely always-on, unlike
      Render's free tier which sleeps and would silently drop agent pushes)
- [ ] Whichever option is chosen, open the Hub's port (default 8080, or
      whatever is configured) in that machine's firewall/security group —
      a one-time step per Hub location, not per agent

---

## Phase 10 — Traffic split: external vs internal

**File:** `collector/collector.go`

Client NICs are usually separated (public-facing + internal/VPC), so this uses
the clean per-interface approach:

- [ ] Switch `gopsutil_net.IOCounters(false)` -> `IOCounters(true)` (per-interface)
- [ ] Auto-detect the external interface as the one holding the **default route**;
      treat all others as internal
- [ ] Manual override flag `-external-iface=<name>` for cases where auto-detect
      guesses wrong
- [ ] Store as separate columns: `net_sent_external`, `net_recv_external`,
      `net_sent_internal`, `net_recv_internal` (schema change in both backends)

This becomes a primary input for sizing egress bandwidth on the target cloud —
not just a trend line, since there's no cloud bill to cross-reference pre-migration.

## Phase 11 — Concurrent users: per-server + aggregate

**Files:** `collector/collector.go`, new `collector/lbstats.go`

Per-server (agent-side, always available):
- [ ] Replace current `GetConcurrentUsers()` (which counts viewers of Zeus's own
      dashboard — not what's needed) with real app traffic: count `ESTABLISHED`
      TCP connections on the app's listening port via `gopsutil_net.Connections("tcp")`

Aggregate (Hub-side, pluggable per client — since LB presence varies):
- [ ] Add `-lb-mode=nginx|haproxy|none` flag on the Hub, set once per client
      engagement based on discovery findings (not auto-detected)
- [ ] `nginx`: poll the client's `stub_status` endpoint (requires enabling it —
      one-line config change, low-risk ask)
- [ ] `haproxy`: poll stats page with `;csv`, parse active session count
- [ ] `none`: fall back to estimating aggregate users by merging distinct client
      IPs seen across all agents' connection snapshots in the same polling window
- [ ] Store the aggregate as a cluster-wide row, tagged with its source
      (`nginx` / `haproxy` / `estimated`)
- [ ] Dashboard/report must label estimated numbers clearly (e.g.
      "Est. unique users (approx)") — don't present dedup estimates with false
      precision next to LB-sourced exact counts

---

## Phase 12 — Assessment report / export view

**File:** `export/export.go` (extend existing), new `export/report.go`

Existing `CSVReport`/`TextReport` cover raw data dumps. This phase adds a
**client-ready summary report** — the actual deliverable for a migration
proposal, not just raw metrics.

- [ ] New function `AssessmentReport(store Store, start, end time.Time) (Report, error)`
      that aggregates across the full assessment period:
  - Per-server: avg/peak CPU %, RAM %, disk free/total, avg/peak disk IOPS
  - Traffic: total external GB, total internal GB (period sum), peak throughput,
    per-server breakdown
  - Concurrent users: avg/peak per server, avg/peak aggregate (with source label:
    exact from LB, or estimated)
  - Assessment window (start/end dates, sample count, uptime coverage)
- [ ] Output as a clean, self-contained **HTML report** (reuse dashboard's
      existing CSS/styling from `dashboard/static` for visual consistency) —
      printable to PDF from any browser, no new heavy Go dependency needed
  - Rationale: keeps Zeus a single static binary with no CGO/external deps,
    consistent with the existing build philosophy (`build_release.ps1`)
- [ ] Keep raw CSV export available alongside the report, for anyone who wants
      to build custom charts/pivot tables from the underlying data
- [ ] New CLI flag: `wmonitor -assessment-report <output.html> -since <duration>`
      (mirrors the existing `-export-csv`/`-export-txt` pattern in `main.go`)

Later refinement (optional, not blocking): a "recommended target sizing" section
that translates observed peak CPU/RAM/IOPS into suggested cloud instance specs —
useful for turning this directly into a quotation input, once the core report
is validated against a couple of real client assessments.

---

## Suggested build order

1. **Phase 0–1** (Aiven + Store interface) — foundational, unblocks everything else
2. **Phase 2–4** (Postgres backend + schema parity + credential handling) —
   get dual-backend working end to end, credentials handled correctly from
   the start rather than retrofitted later
3. **Phase 5–7** (Agent/Hub + API key auth + dashboard selector) — validates
   the distributed architecture
4. **Phase 8–9** (install script parameters + Hub deployment) — this is what
   actually makes client rollout a single command instead of a manual process;
   do this once Phase 5–7 exist, since there's nothing to install until then
5. **Phase 10–11** (traffic split + concurrent users) — the metrics that
   actually matter for migration sizing
6. **Phase 12** (report/export) — turns collected data into a deliverable;
   do this last since it depends on all prior schema decisions being settled