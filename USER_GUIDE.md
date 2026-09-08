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
│                                     │ HTTPS POST /api/ingest            │
│                                     │ Header: X-API-Key: <Acme_Key>     │
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

This compiles two clean, generic binaries with zero hardcoded credentials:
* `wmonitor.exe` (Windows 64-bit universal binary)
* `wmonitor_linux` (Linux 64-bit universal binary)

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

**D. Build a client binary with the enrollment code baked in:**
```powershell
.\build_release.ps1 -ClientName "AcmeCorp" -HubUrl "https://wmonitor-hub.onrender.com" -EnrollCode "WM-XXXX-XXXX-XXXX"
# Output: dist\AcmeCorp\wmonitor_AcmeCorp.exe  (Windows)
#         dist\AcmeCorp\wmonitor_AcmeCorp_linux (Linux)
#         .\wmonitor.exe (copy to root for install.ps1)
```

Distribute the built binary (`wmonitor_AcmeCorp.exe` / `wmonitor_AcmeCorp_linux`) to AcmeCorp's servers.
No API key or secret needs to be shared — the enrollment code is embedded in the binary and consumed on first run.

---

### Step 3: Deploy Agents on Windows Servers

Package `wmonitor.exe` and `install.ps1` and provide them to the client's Windows admin.

#### A. Install as a Background Windows Service (Production)
Run in **PowerShell as Administrator**:

```powershell
# No API key needed -- the binary auto-enrolls on first start
.\install.ps1 -Mode agent -HubUrl "https://wmonitor-hub.onrender.com"
```

*(Note: `-HubUrl` automatically defaults to `https://wmonitor-hub.onrender.com`. You only need to pass `-HubUrl` if using a custom domain.)*

**What this does automatically:**
1. Installs binary to `C:\Program Files\W-Monitor\wmonitor.exe`.
2. Registers and starts the `wmonitor` Windows Service with startup type *Automatic*.
3. On first start, the agent calls `POST /api/enroll` with the baked enrollment code.
4. The Hub returns a scoped `wma_` ingest token, saved to `%ProgramData%\wmonitor\token.dat` (DPAPI-encrypted, SYSTEM + Administrators only).
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
chmod +x install.sh
# No API key needed -- the binary auto-enrolls on first start
sudo ./install.sh --mode agent --hub-url https://wmonitor-hub.onrender.com
```

*(Note: `--hub-url` automatically defaults to `https://wmonitor-hub.onrender.com`.)*

**What this does automatically:**
1. Copies binary to `/usr/local/bin/wmonitor`.
2. Registers, enables, and starts `wmonitor.service` via `systemd`.
3. On first start, the agent calls `POST /api/enroll` with the baked enrollment code.
4. The Hub returns a scoped `wma_` ingest token, saved to `/etc/wmonitor/token.json` (`0600`, root-only).
5. Subsequent runs load the token from the credential file -- no re-enrollment needed.
6. Replacing a machine token requires `current_token` or operator `POST /api/admin/agents/rotate`. A lost first reply is recovered for five minutes.

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
2. **Organization Key Authentication:**
   * An authentication modal will appear: `🔐 W-Monitor Client Access`.
   * Paste your client's **Organization API Key** and click **Access Dashboard**.
   * The dashboard saves your key in browser storage and activates your isolated tenant session.
3. **Filtering by Server:**
   * Look at the top navigation bar for the **Server Dropdown** (`All Servers`).
   * Choose **All Servers** to view aggregated fleet metrics.
   * Or click the dropdown to select a specific server (e.g. `WIN-SRV01`, `LINUX-DB-02`) to view that machine's isolated utilization.
4. **Time Window Ranges:**
   * Click **24h** for real-time 10-second metric resolution.
   * Click **7d** or **30d** for historical trends with automatic hourly downsampling.
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
# Windows:
.\wmonitor.exe

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
`CLI Flag` > `Environment Variable` > `config.env file` > `Build-time default (-ldflags)` > `Built-in default`.

| Flag | Env Variable | Default | Description |
| :--- | :--- | :--- | :--- |
| `-agent <url>` | `WMONITOR_AGENT_HUB` | `""` | Run in Agent mode, forwarding metrics to this Hub URL |
| `-api-key <key>` | `WMONITOR_API_KEY` | `""` | Organization API key for authentication |
| `-hub` | `WMONITOR_MODE=hub` | `false` | Enable Hub ingest endpoint (`POST /api/ingest`) |
| `-port <port>` | `WMONITOR_PORT` / `PORT` | `8080` | HTTP port for the web dashboard |
| `-db <type>` | `WMONITOR_DB` | `sqlite` | Database backend (`sqlite` or `postgres`) |
| `-dsn <dsn>` | `WMONITOR_DB_DSN` | `""` | PostgreSQL connection string |
| `-app-port <p>` | `WMONITOR_APP_PORT` | `""` | Ports to monitor for concurrent active users (e.g. `80,443,3000`) |
| `-external-iface`| `WMONITOR_EXTERNAL_IFACE`| `""` | Override network interface for cloud egress tracking |
| `-assessment-report`| — | `""` | Generate HTML cloud assessment report and exit |
| `-export-csv` | — | `""` | Export spreadsheet-safe CSV (formula-prefixed names) and exit |
| `-tenant <id>` | — | `t_local` | Tenant scope for `-export-csv`, `-export-txt`, and `-assessment-report`. Empty is never a global read. Hub operators must pass the client `t_<hex>` id. |
| `-since <dur>` | — | `720h` (30d) | Time window for assessment & export reports |
| `-add-client <name>`| — | `""` | Generate a dashboard **read** token and opaque `t_<hex>` tenant for a new unique client name (Hub only). Duplicate names are refused. |
| `-list-clients` | — | `false` | Audit all registered clients and activity timestamps |
| `-revoke-client` | — | `""` | Revoke all API keys for a client organization |
| `-import-clients <csv>` | — | `""` | Explicit CSV import of client keys. Skips hashes that already exist (including revoked). Does **not** run at Hub start. |
| `-new-enroll-code <name>` | — | `""` | Issue a handshake-only enrollment code. Ambiguous display names fail. First `POST /api/enroll` for a new `server_id` consumes one use. Replacement needs `current_token` or operator rotate. |
| `-new-admin-token` | — | `false` | Issue a platform admin token (`wmk_`, `kind=admin`, `scope=admin`). |
| `-migrate-opaque-tenants` | — | `false` | SQLite-only: remap plaintext `tenant_id` values to `t_<hex>` after writing a backup. |
| `-opaque-tenant-backup-dir` | — | `""` | Required backup directory for `-migrate-opaque-tenants`. |
| `-print-config` | — | `false` | Print resolved runtime config. Secrets are shown as `(set)` / `(not set)`, never as a prefix of the value. |
| `-show-key` | — | `false` | Display configured API key and exit (operator console only; not written to the service log). |
| — | `WMONITOR_CREDENTIAL_DIR` | OS default (`%PROGRAMDATA%\wmonitor` on Windows; `/etc/wmonitor` as root or `~/.local/share/sysmon` otherwise) | Directory that stores the machine enrollment token. Used by tests and disposable runners so production token files are never overwritten. |
| — | `WMONITOR_DATA_DIR` | OS default (`%LOCALAPPDATA%\sysmon` / `~/.local/share/sysmon`) | Data directory for spool, `agent_id`, and local SQLite. |
| — | `WMONITOR_DISK_BUDGET_BYTES` | `10737418240` (10 GiB) | Maximum size of the SQLite data file. When exceeded, retention reports disk pressure and does not delete or downsample rows. `0` disables the check. |
| — | `WMONITOR_TRUSTED_PROXIES` | `""` | Comma-separated proxy IPs or CIDRs. Only these peers may set `X-Forwarded-Proto` for HTTPS detection. Empty means the header is never trusted. |

Precedence for credential/data directories: in-process test override (`SetCredentialDir` / `SetDataDir`) > environment variable > OS default. There is no CLI flag for these paths.

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
