# W-Monitor (Zeus)

**A single-binary, Go-based monitoring agent, hub, and pre-migration infrastructure assessment tool for Windows and Linux server fleets.**

W-Monitor deploys one universal binary to every server you need to assess, streams OS-level metrics back to a central hub over HTTPS, and turns the collected data into client-ready HTML reports and CSV exports for cloud migration sizing. It can also run standalone on a single machine with no central hub at all.


---

## What it does

- **Collects** CPU, memory, disk, network, and process-level metrics from Windows and Linux hosts (via [gopsutil](https://github.com/shirou/gopsutil)).
- **Ships** metrics to a central Hub over outbound-only HTTPS (`POST /api/v1/ingest/batches`), authenticated with a per-organization API key — monitored servers never open inbound ports and never talk to the database directly.
- **Buffers** locally to disk when the Hub is unreachable and drains the backlog automatically once connectivity is restored.
- **Serves** a web dashboard (Chart.js, self-hosted — no CDN dependency) with a per-server selector, either from the central Hub or from a single standalone instance.
- **Generates** self-contained HTML migration-assessment reports and raw CSV exports for further financial/sizing modeling.
- **Runs as a native OS service** on Windows and Linux (via [kardianos/service](https://github.com/kardianos/service)), with CLI-driven install/uninstall/start/stop.

## Architecture

W-Monitor resolves one of three modes at startup:

| Mode | Trigger | Purpose | Local DB | Dashboard |
|---|---|---|---|---|
| `agent` | `-agent <hub_url>` or `WMONITOR_MODE=agent` | Collects metrics on a client machine and pushes them to the Hub | None (disk spool buffer) | None (zero open ports) |
| `hub` | `-hub` or `WMONITOR_MODE=hub` | Central server that receives metrics and serves the dashboard | PostgreSQL (`-db postgres`) | Hosted at the Hub's URL |
| `standalone` | Default (no flags) | Single-machine assessment, no central hub | Embedded SQLite (`wmonitor.db`) | `http://localhost:8080` |

```
┌─────────────────────────────────────────────────────────────────┐
│                    CLIENT ENVIRONMENT (e.g. Acme Corp)            │
│   Same wmonitor binary deployed to all Windows & Linux servers    │
│   All servers configured with Acme Corp's Organization API Key    │
│                                                                     │
│   App Server 01     App Server 02     Database Node               │
│        └──────────────────┴──────────────────┘                     │
│                            │ HTTPS POST /api/v1/ingest/batches      │
│                            │ Header: X-API-Key: wma_...             │
│                            │ (outbound ONLY — 0 inbound ports)      │
└────────────────────────────┼───────────────────────────────────────┘
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                  CENTRAL W-MONITOR HUB (e.g. Render.com)           │
│  - Validates the org API key via SHA-256 hash lookup in Postgres  │
│  - Maps servers to the client's isolated tenant ID                 │
│  - Serves the multi-server web dashboard                           │
│  - Generates HTML assessment reports & CSV exports                 │
│                            │                                       │
│                            ▼                                       │
│                    PostgreSQL Database                             │
└─────────────────────────────────────────────────────────────────┘
```

Full write-ups of this model live in [`USER_GUIDE.md`](USER_GUIDE.md) (fleet rollout, dashboard, standalone mode, CLI/env reference) and [`ADMIN_GUIDE.md`](ADMIN_GUIDE.md) (Hub setup, client onboarding, key management, security model).

## Repository layout

```
.
├── main.go                # Entry point: flag parsing, service wiring, mode dispatch
├── runtime.go              # Foreground run loop, graceful shutdown
├── config.go               # Config loading/validation (env, file, CLI)
├── client_admin.go         # CLI admin commands (add/revoke client, enroll codes, tokens)
├── agent/                  # Agent-side collection, credential store, spool, transport
├── alerting/               # Alert rule evaluation and notification
├── collector/              # OS metric collection (CPU/mem/disk/net/process, user tracking)
├── dashboard/              # Dashboard HTML/JS serving (dashboard/static holds the frontend)
├── export/                 # CSV and HTML assessment report generation
├── internal/fsroot/        # Filesystem path-safety helpers
├── internal/testisolate/   # Test isolation helpers (no production paths in tests)
├── retention/              # Data retention / capacity jobs
├── server/                 # Hub HTTP server: auth, sessions, enrollment, ingest, admin API
├── storage/                # SQLite + PostgreSQL storage backends, API keys, audit log
├── install.sh / install.ps1        # Fresh Linux/Windows service installers
├── install_user.ps1                # Per-user install (currently disabled/throws)
├── build_release.ps1               # Builds clean cross-platform release binaries
├── render.yaml                     # Render.com Hub deployment blueprint
├── docs/PHASE1-STATUS.md           # Security hardening / verification ledger
├── CHANGELOG.md                    # Detailed technical changelog per work item
├── USER_GUIDE.md / ADMIN_GUIDE.md  # Full operator documentation
└── AGENTS.md                       # Notes for AI coding agents working in this repo
```

10 Go packages, ~110 source files, ~50 test files, ~19k lines of Go (`go.mod` targets Go 1.26).

## Getting started

### Build

```powershell
# Windows PowerShell — builds clean release binaries with no embedded secrets
.\build_release.ps1
# → dist\wmonitor.exe        (Windows amd64)
# → dist\wmonitor_linux      (Linux amd64)
```

or directly with `go build`:

```sh
go build -o wmonitor .
```

### Run standalone (single machine, no Hub)

```sh
./wmonitor
# Dashboard at http://localhost:8080, data in ./wmonitor.db (SQLite)
```

### Run as a Hub

```sh
WMONITOR_MODE=hub WMONITOR_DB=postgres WMONITOR_DB_DSN=<postgres-dsn> ./wmonitor -hub
```

The included [`render.yaml`](render.yaml) deploys the Hub to Render.com with a managed PostgreSQL DSN and a generated `WMONITOR_API_KEY`.

### Enroll an agent

On the Hub, issue an enrollment code and client credentials:

```sh
./wmonitor -new-admin-token          # one-time admin token
./wmonitor -add-client "AcmeCorp"    # dashboard read token for the client
./wmonitor -new-enroll-code "AcmeCorp" -ttl 72h -max-uses 25
```

Then, on each client server, drop a protected config file (no secrets on the command line) and run the appropriate installer:

```text
WMONITOR_MODE=agent
WMONITOR_AGENT_HUB=https://your-hub-url
WMONITOR_ENROLL_CODE=WM-XXXX-XXXX-XXXX
```

```sh
sudo ./install.sh --config /root/wmonitor.env --binary ./dist/wmonitor_linux --sha256 <verified-digest>
```

```powershell
.\install.ps1 -Config C:\path\to\config.env -Binary .\dist\wmonitor.exe -Sha256 <verified-digest>
```

Installers refuse to embed secrets as arguments, refuse to replace an existing install, and require an independently verified SHA-256 of the binary.

Full step-by-step fleet rollout (Windows/Linux, GPO/Ansible/cloud-init automation, dashboard usage) is in [`USER_GUIDE.md`](USER_GUIDE.md) and [`ADMIN_GUIDE.md`](ADMIN_GUIDE.md).

### Generate a report

```sh
./wmonitor -assessment-report ./report.html -since 720h   # last 30 days, self-contained HTML
./wmonitor -export-csv ./metrics.csv
```

## Key CLI flags

| Flag | Purpose |
|---|---|
| `-agent <url>` / `-hub` | Run as agent (push to Hub) or as Hub |
| `-db sqlite\|postgres` | Storage backend |
| `-port` / `-app-port` | Dashboard HTTP port(s) |
| `-install` / `-uninstall` / `-start` / `-stop` | OS service control |
| `-add-client`, `-revoke-client`, `-list-clients` | Client organization management |
| `-new-enroll-code`, `-enroll-code` | Agent self-registration codes |
| `-new-admin-token` | One-time admin credential issuance |
| `-list-agents`, `-revoke-agent` | Per-server agent identity management |
| `-assessment-report`, `-export-csv`, `-export-txt` | Report/data export |
| `-print-config` | Print resolved configuration (secrets redacted) |
| `-config <path>` | Explicit protected config file path |

Run `wmonitor -h` for the full list, or see section 9 of [`USER_GUIDE.md`](USER_GUIDE.md) for a complete flag/environment-variable reference.

## Security model (summary)

- Agents authenticate to the Hub with a per-organization API key over HTTPS; the key is only ever compared as a SHA-256 hash server-side.
- Enrollment codes are single-purpose handshake secrets, separate from the API key and from dashboard read tokens.
- Dashboard sessions use opaque `HttpOnly`/`SameSite` cookies with exact origin/scheme checks; no bearer tokens in the URL or `localStorage`.
- All admin-facing credential and session mutations write to an append-only, tenant-scoped audit log (`audit_events`); the browser login path fails closed if that audit write fails.
- Metric/process/server reads are cursor-paginated and capped, and report their own completeness rather than silently truncating.
- Installers require an absolute, root-owned config file and an independently verified binary checksum, and never accept secrets as command-line arguments.

This is a summary — see [`ADMIN_GUIDE.md`](ADMIN_GUIDE.md) section 7 and [`docs/PHASE1-STATUS.md`](docs/PHASE1-STATUS.md) for the authoritative, currently-verified state of each control.

## Development

```sh
go build ./...
go vet ./...
gofmt -l .
go test ./... -count=1
```

An AST-based guard (`go test ./agent -run '^TestNoProductionPathCallsInTests$'`) checks that tests don't call production credential/data-path entry points directly. See [`docs/PHASE1-STATUS.md`](docs/PHASE1-STATUS.md) for the full disposable-runner verification command list, including the checks that currently cannot run in every environment (e.g. `go test -race` requires a C compiler on `PATH`).

## Documentation

- [`USER_GUIDE.md`](USER_GUIDE.md) — end-to-end fleet rollout, dashboard usage, standalone mode, CLI/env reference, troubleshooting
- [`ADMIN_GUIDE.md`](ADMIN_GUIDE.md) — Hub setup, client onboarding, key management, security model
- [`CHANGELOG.md`](CHANGELOG.md) — detailed per-change technical log
- [`docs/PHASE1-STATUS.md`](docs/PHASE1-STATUS.md) — security hardening plan and verification ledger

## License

No license file is currently included in this repository. All rights reserved by the author unless a license is added.
