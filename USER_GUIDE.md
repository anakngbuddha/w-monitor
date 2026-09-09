# W-Monitor Complete User & Operator Guide
## Centralized Multi-Server Fleet Monitoring & Cloud Migration Assessment

Welcome to **W-Monitor**, a lightweight, high-performance Go monitoring suite and pre-migration sizing agent designed for enterprise multi-server assessments.

This guide covers the **Method B (Universal Generic Binary)** workflow, deploying collector agents across Windows and Linux server fleets, using the centralized web dashboard on Render, and generating cloud migration deliverables.

---

## Table of Contents

1. [Architecture & The Method B Standard](#1-architecture--the-method-b-standard)
2. [Operating Modes Overview](#2-operating-modes-overview)
3. [End-to-End Quick Start (Method B Fleet Rollout)](#3-end-to-end-quick-start-method-b-fleet-rollout)
   - [Step 1: Build the Universal Binaries](#step-1-build-the-universal-binaries)
   - [Step 2: Generate an Organization API Key on the Hub](#step-2-generate-an-organization-api-key-on-the-hub)
   - [Step 3: Deploy Agents on Windows Servers](#step-3-deploy-agents-on-windows-servers)
   - [Step 4: Deploy Agents on Linux Servers](#step-4-deploy-agents-on-linux-servers)
4. [Centralized Web Dashboard Guide](#4-centralized-web-dashboard-guide)
5. [Standalone / Solo Mode (Offline / Single Machine)](#5-standalone--solo-mode-offline--single-machine)
6. [Generating Assessment Reports & Data Exports](#6-generating-assessment-reports--data-exports)
7. [Client Key Auditing & Access Revocation](#7-client-key-auditing--access-revocation)
8. [Background Service Operations & Maintenance](#8-background-service-operations--maintenance)
9. [CLI Flags & Environment Variables Reference](#9-cli-flags--environment-variables-reference)
10. [Troubleshooting & FAQ](#10-troubleshooting--faq)

---

## 1. Architecture & The Method B Standard

W-Monitor is built around the **Universal Generic Binary + Organization API Key** standard:

```
┌─────────────────────────────────────────────────────────────────────────┐
│                    CLIENT ENVIRONMENT (e.g. Acme Corp)                  │
│                                                                         │
│   Same wmonitor binary deployed to all Windows & Linux servers          │
│   All servers configured with Acme Corp's Organization API Key          │
│                                                                         │
│    ┌──────────────────┐    ┌──────────────────┐    ┌─────────────────┐  │
│    │  App Server 01   │    │  App Server 02   │    │  Database Node  │  │
│    │ (ID: SRV-APP-01) │    │ (ID: SRV-APP-02) │    │ (ID: SRV-DB-01) │  │
│    └────────┬─────────┘    └────────┬─────────┘    └────────┬────────┘  │
│             │                       │                       │           │
│             └───────────────────────┼───────────────────────┘           │
│                                     │ HTTPS POST /api/v1/ingest/batches │
│                                     │ Header: X-API-Key: <wma_ token>    │
│                                     │ (Outbound ONLY — 0 inbound ports) │
└─────────────────────────────────────┼───────────────────────────────────┘
                                      │
                                      ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                      CENTRAL W-MONITOR HUB (Render.com)                 │
│                      https://wmonitor-hub.onrender.com                  │
│                                                                         │
│  - Validates Org API Key via SHA-256 hash lookup in PostgreSQL          │
│  - Maps all client servers to Acme Corp's isolated Tenant ID            │
│  - Serves multi-server Web Dashboard with individual server selector    │
│  - Generates consolidated HTML assessment & cloud sizing deliverables   │
│                                     │                                   │
│                                     ▼                                   │
│                        ┌────────────────────────┐                       │
│                        │  PostgreSQL Database   │                       │
│                        │ (Render / Aiven Cloud) │                       │
│                        └────────────────────────┘                       │
└─────────────────────────────────────────────────────────────────────────┘
```

### Core Principles:
* **One Key per Client Organization:** You generate **one** API key per client company. All servers in that company share this key.
* **Automatic Server Distinction:** Each machine automatically derives a unique, persistent `server_id` (e.g. `WIN-SRV01-4f8a12`) stored in `agent_id`.
* **Zero Database Exposure:** Monitored client servers never communicate with PostgreSQL and never store database credentials.
* **Spooling Resilience:** If the central Hub or network is temporarily offline, agents buffer metrics on local disk and automatically drain them once reconnected.

---

## 2. Operating Modes Overview

W-Monitor operates in three distinct modes resolved at runtime:

| Mode | Flag / Trigger | Purpose | Local DB | Dashboard |
| :--- | :--- | :--- | :--- | :--- |
| **`agent`** | `-agent <hub_url>` or `install.ps1 -Mode agent` | Collects OS metrics on client machines and pushes them to the Hub | None (disk spool buffer) | None (zero open ports) |
| **`hub`** | `-hub` or `WMONITOR_MODE=hub` | Central server on Render/cloud that receives metrics and serves dashboard | PostgreSQL (`-db postgres`) | Hosted at your Render URL |
| **`standalone`** | Default (no flags) | Single-machine assessment with local storage | Embedded SQLite (`wmonitor.db`) | Hosted at `http://localhost:8080` |

---

## 3. End-to-End Quick Start (Method B Fleet Rollout)

### Step 1: Build the Universal Binaries

Run the builder script in PowerShell:

```powershell
.\build_release.ps1
```

This compiles two clean, generic binaries with zero hardcoded credentials. Passing `-EnrollCode` or `-ApiKey` is rejected:
* `dist\wmonitor.exe` (Windows 64-bit universal binary)
* `dist\wmonitor_linux` (Linux 64-bit universal binary)

A SHA-256 of those files is an integrity check, not a publisher signature.

---

### Step 2: Generate Credentials on the Hub

On your Hub machine, run these commands (the hub doesn't need to be running for CLI commands):

**A. Create an admin token** (once — needed for CI/CD automation):
```powershell
.\wmonitor.exe -new-admin-token
# Output: wmk_aBcDeFgH...  -- save this, shown only once
```

**B. Create a dashboard read token** for the client:
```powershell
.\wmonitor.exe -add-client "AcmeCorp"
# Output: wmr_xxxxxx  -- give this to AcmeCorp for dashboard login
```

**C. Create an enrollment code** so the agent binary can self-register on first run:
```powershell
.\wmonitor.exe -new-enroll-code "AcmeCorp" -ttl 72h -max-uses 25
# Output: WM-XXXX-XXXX-XXXX  -- use this in Step 2B below
```
The enrollment code is a handshake secret only. It cannot be sent as `X-API-Key` to `/api/ingest` or dashboard routes. Duplicate client display names are rejected; pass a unique name or create the code with an explicit tenant after listing clients.

**D. Provision a protected machine config** (UTF-8, no BOM). Do not bake codes into the binary:

```text
WMONITOR_MODE=agent
WMONITOR_AGENT_HUB=https://wmonitor-hub.onrender.com
WMONITOR_ENROLL_CODE=WM-XXXX-XXXX-XXXX
```

Distribute the universal binary, `install.ps1` / `install.sh`, and this config file. Existing installs are not replaced by the fresh-install scripts.

---

### Step 3: Deploy Agents on Windows Servers

Package `wmonitor.exe` and `install.ps1` and provide them to the client's Windows admin.

#### A. Install as a Background Windows Service (Production)
Run in **PowerShell as Administrator**:

```powershell
# Run as Administrator. Secrets stay in ConfigPath; they are never service arguments.
Get-FileHash .\dist\wmonitor.exe -Algorithm SHA256
.\install.ps1 -ConfigPath C:\secure\wmonitor.env -BinaryPath .\dist\wmonitor.exe -ExpectedSHA256 "<64-hex-digest>"
```

**What this does automatically:**
1. Verifies the binary digest, then copies it to `C:\Program Files\W-Monitor\wmonitor.exe`.
2. Writes `%ProgramData%\wmonitor\config.env` with a SYSTEM + Administrators DACL.
3. Runs `wmonitor -config ... -print-config` then `-install` and `-start`.
4. On first start, the agent calls `POST /api/enroll` using `WMONITOR_ENROLL_CODE` from that file.
5. The Hub returns a scoped `wma_` ingest token, saved under `%ProgramData%\wmonitor` (DPAPI, SYSTEM + Administrators).
6. If `wmonitor` already exists as a service or config, the script exits without stopping or replacing it.
5. Subsequent runs load the token from the credential store — no re-enrollment needed.
6. Replacing a machine token requires proving the current token (`current_token` in the enroll JSON) or an operator `POST /api/admin/agents/rotate`. A lost first reply is recovered automatically for five minutes.
7. Production agents require an `https://` hub URL (loopback HTTP is allowed only for local tests). Changing the hub URL does **not** send the old token or the old spool backlog; re-enroll at the new hub.

#### B. Interactive Foreground Run (Testing Only)
If you want to test without installing a service:
```powershell
# The binary auto-enrolls on first run and saves the token to the credential store
.\wmonitor.exe -agent "https://wmonitor-hub.onrender.com"
```

---

### Step 4: Deploy Agents on Linux Servers

Package `wmonitor_linux` and `install.sh` and transfer them to the target Linux machine.

#### A. Install as a systemd Background Service (Production)
Run in terminal as `root`:

```bash
sha256sum dist/wmonitor_linux
sudo ./install.sh --config /root/wmonitor.env --binary ./dist/wmonitor_linux --sha256 "<64-hex-digest>"
```

*(Secrets are never command-line arguments. The source config must be an absolute, root-owned, mode `600` regular file.)*

**What this does automatically:**
1. Copies the binary to `/usr/local/bin/wmonitor` after digest check.
2. Installs `/etc/wmonitor/config.env` mode `0600` root-only.
3. Registers and starts `wmonitor.service` via `systemd` using `-config /etc/wmonitor/config.env`.
4. On first start, the agent calls `POST /api/enroll` using `WMONITOR_ENROLL_CODE` from that file.
5. The Hub returns a scoped `wma_` ingest token, saved to `/etc/wmonitor` (`0600`, root-only).
6. If `wmonitor.service` or `/etc/wmonitor/config.env` already exists, the script exits without stopping or replacing it.

#### B. Interactive Foreground Run (Testing Only)
```bash
chmod +x wmonitor_linux
# Auto-enrolls on first run, saves token to /etc/wmonitor/token.json
./wmonitor_linux -agent "https://wmonitor-hub.onrender.com"
```

---

## 4. Centralized Web Dashboard Guide

1. Open your browser and navigate to your deployed Central Hub:
   ```text
   https://wmonitor-hub.onrender.com
   ```
2. **Read-token session authentication:**
   * An authentication modal will appear: `🔐 W-Monitor Client Access`.
   * Paste a dashboard **read token** (`wmr_…`) issued by `-add-client`. Agent (`wma_`) tokens cannot sign in.
   * Login is `POST /api/session` over HTTPS (loopback HTTP is allowed only on 127.0.0.1). The token is never stored in the URL, `localStorage`, or request headers after login.
   * The Hub sets an HttpOnly, SameSite=Strict `wmonitor_session` cookie. Logout is `DELETE /api/session` and clears charts.
   * Status labels report `Recent samples`, `Stale data (over 2 minutes)`, or `Result limit reached (incomplete)` when `complete` is false. Chart gaps are shown (`spanGaps: false`).
   * Metric and process APIs are keyset-paginated: `GET /api/metrics?range=24h&limit=100000&cursor=<unix>:<id>`. Follow `next_cursor` until `complete` is true. The default cap is 100000 rows. CSV export uses the same `limit`/`cursor` query parameters and returns `X-Result-Complete` / `X-Next-Cursor`.
   * Server lists are paginated separately: `GET /api/servers?limit=1000&cursor=<server_id>`.
3. **Filtering by Server:**
   * Look at the top navigation bar for the **Server Dropdown** (`All Servers`).
   * Choose **All Servers** to view aggregated fleet metrics.
   * Or click the dropdown to select a specific server (e.g. `WIN-SRV01`, `LINUX-DB-02`) to view that machine's isolated utilization.
4. **Time Window Ranges:**
   * Click **24h**, **7d**, or **30d**. Destructive hourly downsampling remains disabled (V07 contained until P2.03). Incomplete or gapped series stay visible.
5. **Observed Metrics:**
   * **CPU Usage:** Average vs. Peak utilization.
   * **Memory Usage:** Average vs. Peak RAM consumed.
   * **Disk Free:** Minimum storage headroom remaining.
   * **Network Bandwidth:** Ingress/egress throughput with automatic isolation of **External Internet** vs. **Internal VPC** traffic.
   * **Disk IOPS:** Real-time read and write IOPS.
   * **Active Users:** Application concurrent user tracking.
   * **Top Processes:** Live rankings of processes by CPU and RAM consumption.

---

## 5. Standalone / Solo Mode (Offline / Single Machine)

For offline, single-machine evaluations where no central Hub is needed:

```powershell
# Windows (loopback-only unauthenticated listener):
.\wmonitor.exe -config C:\secure\standalone.env
```

# Linux:
./wmonitor_linux
```

* W-Monitor creates a local embedded SQLite database in `%LOCALAPPDATA%\Sysmon\wmonitor.db` (Windows) or `~/.local/share/sysmon/wmonitor.db` (Linux).
* The real-time web dashboard is served directly at:
  ```text
  http://localhost:8080
  ```

---

## 6. Generating Assessment Reports & Data Exports

At the conclusion of your monitoring period (e.g. 7 days, 14 days, or 30 days), generate deliverables directly from the PostgreSQL backend:

### 1. Publication-Ready HTML Assessment Report
Generates an interactive HTML report complete with resource percentiles, IOPS distribution, network egress breakdowns, and cloud VM sizing recommendations (printable directly to PDF via browser):

```powershell
.\wmonitor.exe -db postgres -dsn "YOUR_POSTGRES_DSN" -assessment-report AcmeCorp_Cloud_Assessment.html -since 720h
```

### 2. Granular CSV Metrics Export
Generates a CSV dump for TCO calculators and Excel pivot tables. **`-export-csv` is spreadsheet-safe:** Server ID and hostname values that begin with `=`, `+`, `-`, `@`, tab, or CR/LF are prefixed with `'` so spreadsheet apps treat them as text. Lossless (unprefixed) export is `export.WriteCSVLossless` for machine consumers only — do not open that file in Excel.

```powershell
.\wmonitor.exe -db postgres -dsn "YOUR_POSTGRES_DSN" -export-csv AcmeCorp_Metrics_Dump.csv -since 720h
```

### 3. Plain-Text Terminal Summary
```powershell
.\wmonitor.exe -db postgres -dsn "YOUR_POSTGRES_DSN" -export-txt summary.txt -since 168h
```

---

## 7. Client Key Auditing & Access Revocation

Run these administrative commands against PostgreSQL:

### Audit Registered Clients & Last-Seen Activity
```powershell
.\wmonitor.exe -db postgres -dsn "YOUR_POSTGRES_DSN" -list-clients
```

**Example Output:**
```text
CLIENT               TENANT                                 STATUS     LAST SEEN            KEY HASH (prefix)
AcmeCorp             t_a8f3b219c0de447192bc55ef812034aa     active     2026-09-04 08:30:12  7a1f89bc430e
BetaLLC              t_4e1c2219b1aa448301ec99901452efgh     active     2026-09-03 22:15:00  9bd183f01ca2
```

### Revoke a Client Organization
To immediately stop accepting metrics from a client and block dashboard access:
```powershell
.\wmonitor.exe -db postgres -dsn "YOUR_POSTGRES_DSN" -revoke-client "AcmeCorp"
```
*The Hub invalidates its authorization cache within 60 seconds.*

---

## 8. Background Service Operations & Maintenance

### Windows Service Management (`wmonitor`)

Run in PowerShell:
```powershell
# Check service status
Get-Service wmonitor

# Restart service
Restart-Service wmonitor

# Stop service
Stop-Service wmonitor

# Uninstall service completely
.\wmonitor.exe -uninstall
```

### Linux systemd Service Management (`wmonitor.service`)

Run in terminal:
```bash
# Check service status
sudo systemctl status wmonitor

# View live streaming logs
sudo journalctl -u wmonitor -f

# Restart service
sudo systemctl restart wmonitor

# Stop service
sudo systemctl stop wmonitor

# Uninstall service completely
sudo /usr/local/bin/wmonitor -uninstall
```

---

## 9. CLI Flags & Environment Variables Reference

W-Monitor enforces a strict configuration precedence:
`CLI Flag` > `Environment Variable` > one absolute protected config file (`-config` / `WMONITOR_CONFIG` / machine `config.env`) > `Built-in default`. Embedded `-ldflags` credentials are rejected at startup. There is no current-directory or executable-directory config search.

| Flag | Env Variable | Default | Description |
| :--- | :--- | :--- | :--- |
| `-config <path>` | `WMONITOR_CONFIG` | `%ProgramData%\wmonitor\config.env` / `/etc/wmonitor/config.env` | Absolute protected UTF-8 config. Windows machine config ACLs: SYSTEM + Administrators. Linux: not group/other-readable. |
| `-agent <url>` | `WMONITOR_AGENT_HUB` | `""` | Agent mode. Production destination must be an `https://` origin with no credentials, path, query, or fragment. |
| `-api-key <key>` | `WMONITOR_API_KEY` | `""` | Test-only token. Prefer protected config. Visible in process lists if passed as a flag. |
| `-hub` | `WMONITOR_MODE=hub` | `false` | Enable authenticated Hub. Agents post `POST /api/v1/ingest/batches`. |
| `-port <port>` | `WMONITOR_PORT` / `PORT` | `8080` | HTTP port. Standalone listens on `127.0.0.1` unless `WMONITOR_LISTEN_HOST` is a loopback address. Hub may bind `0.0.0.0`. |
| `-db <type>` | `WMONITOR_DB` | `sqlite` | Database backend (`sqlite` or `postgres`) |
| `-dsn <dsn>` | `WMONITOR_DB_DSN` | `""` | Test-only DSN. Remote PostgreSQL requires `sslmode=verify-full`. Prefer `-dsn-file` or env. |
| `-dsn-file` | — | `""` | Protected DSN file (same ownership rules as config). |
| `-app-port <p>` | `WMONITOR_APP_PORT` | `""` | Ports to monitor for concurrent active users (e.g. `80,443,3000`) |
| `-external-iface`| `WMONITOR_EXTERNAL_IFACE`| `""` | Override network interface for cloud egress tracking |
| `-assessment-report`| — | `""` | Generate HTML cloud assessment report and exit |
| `-export-csv` | — | `""` | Export spreadsheet-safe CSV (formula-prefixed names) and exit |
| `-tenant <id>` | — | `t_local` | Tenant scope for `-export-csv`, `-export-txt`, and `-assessment-report`. Empty is never a global read. Hub operators must pass the client `t_<hex>` id. |
| `-since <dur>` | — | `720h` (30d) | Time window for assessment & export reports |
| `-add-client <name>`| — | `""` | Generate a dashboard **read** token and opaque `t_<hex>` tenant for a new unique client name (Hub only). Duplicate names are refused. |
| `-list-clients` | — | `false` | Audit all registered clients and activity timestamps |
| `-revoke-client` | — | `""` | Revoke all API keys for a client organization |
| `-import-clients <csv>` | — | `""` | Disabled pending a reviewed migration. Use `-add-client` and `-new-enroll-code`. |
| `-new-enroll-code <name>` | — | `""` | Issue a handshake-only enrollment code. Ambiguous display names fail. First `POST /api/enroll` for a new `server_id` consumes one use. Replacement needs `current_token` or operator rotate. |
| `-new-admin-token` | — | `false` | Issue a platform admin token (`wmk_`, `kind=admin`, `scope=admin`). |
| `-migrate-opaque-tenants` | — | `false` | SQLite-only: remap plaintext `tenant_id` values to `t_<hex>` after writing a backup. |
| `-opaque-tenant-backup-dir` | — | `""` | Required backup directory for `-migrate-opaque-tenants`. |
| `-print-config` | — | `false` | Print resolved runtime config. Secrets are shown as `(set)` / `(not set)`, never as a prefix of the value. |
| `-show-key` | — | `false` | Secret recovery is disabled. Request a new scoped credential. |
| `-install` / `-start` / `-stop` / `-uninstall` | — | `false` | Service control. Stop/uninstall still work if workload config is broken. Choose exactly one control action. |
| — | `WMONITOR_LISTEN_HOST` | Hub `0.0.0.0`; else `127.0.0.1` | Unauthenticated non-loopback binds are rejected. |
| — | `WMONITOR_DAILY_ROW_QUOTA` | `20000000` | Shared tenant accepted rows per UTC day. |
| — | `WMONITOR_DAILY_BYTE_QUOTA` | `21474836480` | Shared tenant accepted bytes per UTC day (20 GiB). |
| — | `WMONITOR_AGENT_DAILY_ROW_QUOTA` | `250000` | Per-agent accepted rows per UTC day. |
| — | `WMONITOR_AGENT_DAILY_BYTE_QUOTA` | `268435456` | Per-agent accepted bytes per UTC day (256 MiB). |
| — | `WMONITOR_CREDENTIAL_DIR` | OS default (`%PROGRAMDATA%\wmonitor` on Windows; `/etc/wmonitor` as root or `~/.local/share/sysmon` otherwise) | Directory that stores the machine enrollment token. Used by tests and disposable runners so production token files are never overwritten. |
| — | `WMONITOR_DATA_DIR` | Service: `%ProgramData%\wmonitor\data` / `/var/lib/wmonitor`; interactive OS default | Data directory for spool, `agent_id`, and local SQLite. |
| — | `WMONITOR_DISK_BUDGET_BYTES` | `10737418240` (10 GiB) | Maximum size of the SQLite data file. When exceeded, retention reports disk pressure and does not delete or downsample rows. `0` disables the check. |
| — | `WMONITOR_TRUSTED_PROXIES` | `""` | Comma-separated proxy IPs or CIDRs. Only these peers may set `X-Forwarded-Proto` for HTTPS detection. Empty means the header is never trusted. |

Precedence for credential/data directories: in-process test override (`SetCredentialDir` / `SetDataDir`) > environment variable > OS default. There is no CLI flag for these paths.

Liveness is `GET /api/health` (no tenant freshness or expected-agent inventory). Readiness is `GET /api/ready`. Hub Prometheus `GET /metrics` requires an admin credential even when the peer is loopback. Tenant audit history is `GET /api/admin/audit?tenant_id=` (admin token only). Expected-agent inventory is `GET /api/admin/agents/expected?tenant_id=` and always reports `completeness: "not_assessed"` until a reviewed campaign contract exists.

---

## 10. Troubleshooting & FAQ

### 1. The dashboard opens `localhost:8080` and displays "Sysmon" instead of "W-Monitor"
* **Cause:** A legacy background process (`sysmon.exe`) from before the project rebranding is running on your machine and holding port 8080.
* **Fix:** Stop the legacy process and remove the startup file:
  ```powershell
  Stop-Process -Name "sysmon", "wmonitor*" -Force -ErrorAction SilentlyContinue
  Remove-Item "$env:APPDATA\Microsoft\Windows\Start Menu\Programs\Startup\Sysmon.vbs" -Force -ErrorAction SilentlyContinue
  ```
  Then, navigate directly to your Render URL: `https://wmonitor-hub.onrender.com`.

### 2. Agent logs: `[server] rejected unknown API key`
* **Cause:** The API key passed to the agent has not been registered in PostgreSQL.
* **Fix:** Run `.\wmonitor.exe -db postgres -add-client "ClientName"` against your database, and verify the client status with `.\wmonitor.exe -db postgres -list-clients`.

### 3. Agent cannot connect to Hub (`connection refused` or timeout)
* **Cause:** Outbound firewall rule blocking HTTPS or incorrect Hub URL.
* **Fix:** Verify connectivity from the agent machine:
  * Windows: `Test-NetConnection -ComputerName wmonitor-hub.onrender.com -Port 443`
  * Linux: `curl -I https://wmonitor-hub.onrender.com/api/health`

### 4. How much disk space does an agent use?
* **Answer:** **Zero database footprint.** The agent stores no persistent metric database. It only maintains a tiny in-memory circular buffer and a disk spool directory in `%LOCALAPPDATA%\Sysmon` / `~/.local/share/sysmon` that is only populated if the network drops.
