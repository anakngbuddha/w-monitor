# W-Monitor Administrator & Operations Guide
## Multi-Server Cloud Migration Assessment with Centralized Multi-Tenancy

This guide is intended for cloud architects, DevOps engineers, and system administrators managing pre-migration assessments using **W-Monitor**. It covers setting up the centralized Hub, provisioning organization-level client API keys, deploying universal agent binaries across multi-server fleets, and generating migration assessment deliverables.

---

## Table of Contents

1. [Architecture & Assessment Workflow](#1-architecture--assessment-workflow)
2. [Setting Up the Central Hub Database](#2-setting-up-the-central-hub-database)
   - [Option A: Managed Cloud PostgreSQL (Aiven / Render - Recommended)](#option-a-managed-cloud-postgresql-aiven--render-recommended)
   - [Option B: Local PostgreSQL in Docker](#option-b-local-postgresql-in-docker)
   - [Option C: Local SQLite (Lightweight / Testing)](#option-c-local-sqlite-lightweight--testing)
3. [Running the Central W-Monitor Hub](#3-running-the-central-w-monitor-hub)
   - [Environment Variables & Credentials](#environment-variables--credentials)
   - [Starting the Hub in Foreground](#starting-the-hub-in-foreground)
   - [Deploying Hub on Cloud (Render.com)](#deploying-hub-on-cloud-rendercom)
4. [Client Onboarding & API Key Management](#4-client-onboarding--api-key-management)
   - [The Organization API Key Model](#the-organization-api-key-model)
   - [Adding a New Client Organization](#adding-a-new-client-organization)
   - [Auditing Registered Clients](#auditing-registered-clients)
   - [Revoking Client Access](#revoking-client-access)
5. [Universal Binary Distribution & Fleet Rollout](#5-universal-binary-distribution--fleet-rollout)
   - [Building Universal Binaries](#building-universal-binaries)
   - [Windows Server Fleet Rollout](#windows-server-fleet-rollout)
   - [Linux Server Fleet Rollout](#linux-server-fleet-rollout)
   - [Automated Mass Deployment (GPO, Ansible, Cloud-Init)](#automated-mass-deployment-gpo-ansible-cloud-init)
6. [Generating Assessment Reports & Migration Deliverables](#6-generating-assessment-reports--migration-deliverables)
   - [Self-Contained HTML Assessment Report](#self-contained-html-assessment-report)
   - [Raw CSV Data Dump for Custom Financial Modeling](#raw-csv-data-dump-for-custom-financial-modeling)
7. [Security Model, Network Rules & Troubleshooting](#7-security-model-network-rules--troubleshooting)

---

## 1. Architecture & Assessment Workflow

W-Monitor uses a **Universal Generic Binary + Organization API Key** architecture. You build the executable **once** and distribute the identical binary to all client servers.

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                       CLIENT ENVIRONMENT (e.g. Acme Corp)                   │
│                                                                             │
│  Universal Binary (wmonitor.exe / wmonitor_linux) on all target servers     │
│                                                                             │
│   ┌────────────────────┐     ┌────────────────────┐                         │
│   │   App Server 01    │     │   Database Node    │     ... (Up to 100+)    │
│   │   (server_id: A)   │     │   (server_id: B)   │                         │
│   └─────────┬──────────┘     └─────────┬──────────┘                         │
│             │                          │                                    │
│             └─────────────┬────────────┘                                    │
│                           │ HTTPS POST /api/ingest                          │
│                           │ Header: X-API-Key: <AcmeCorp_APIKey>            │
│                           │ (NO database credentials on client servers)     │
└───────────────────────────┼─────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                           CENTRAL W-MONITOR HUB                             │
│                                                                             │
│   ┌─────────────────────────────────────────────────────────────────────┐   │
│   │                         W-Monitor Hub Server                        │   │
│   │  - Validates API Key via SHA-256 hash lookup in Postgres            │   │
│   │  - Maps all Acme Corp servers to Acme Corp's isolated Tenant ID     │   │
│   │  - Serves multi-tenant Web Dashboard & REST API                     │   │
│   │  - Generates Cloud Migration Sizing & HTML Assessment Reports       │   │
│   └──────────────────────────────────┬──────────────────────────────────┘   │
│                                      │                                      │
│                                      ▼                                      │
│                        ┌───────────────────────────┐                        │
│                        │    PostgreSQL Database    │                        │
│                        │  (Aiven / Render / Docker)│                        │
│                        └───────────────────────────┘                        │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Core Tenancy Principles:
1. **Universal Binary:** Binaries contain no hardcoded secrets or customer names. The same `wmonitor.exe` is deployed everywhere.
2. **One Key Per Organization:** All servers belonging to "Acme Corp" share Acme Corp's Organization API Key.
3. **Automatic Server Distinction:** Each server machine automatically generates its own stable, unique `server_id` (e.g. `WIN-SRV01-4f8a12`) and reports its hostname.
4. **Data Isolation:** Metrics from different clients are strictly isolated by `TenantID` in the database.
5. **Zero Direct DB Access:** Client servers never talk to PostgreSQL directly and never hold database passwords.

---

## 2. Setting Up the Central Hub Database

### Option A: Managed Cloud PostgreSQL (Aiven / Render - Recommended)

For multi-site assessments where the Hub is hosted in the cloud:
1. Create a free/starter PostgreSQL instance on [aiven.io](https://aiven.io) or [render.com](https://render.com).
2. Copy the Service URI (DSN). Example:
   ```text
   postgres://avnadmin:SecretPassword123@pg-service.aivencloud.com:15432/defaultdb?sslmode=require
   ```

### Option B: Local PostgreSQL in Docker

For running the Hub on your management laptop or staging environment:
```bash
docker run -d \
  --name wmonitor-postgres \
  -p 5432:5432 \
  -e POSTGRES_USER=wmonitor \
  -e POSTGRES_PASSWORD=secretpassword \
  -e POSTGRES_DB=wmonitor_db \
  -v wmonitor_pgdata:/var/lib/postgresql/data \
  postgres:16-alpine
```
**DSN:**
```text
postgres://wmonitor:secretpassword@localhost:5432/wmonitor_db?sslmode=disable
```

### Option C: Local SQLite (Lightweight / Testing)

For single-machine assessments without a central server, W-Monitor defaults to an embedded SQLite database stored in `%LOCALAPPDATA%\sysmon\wmonitor.db` (Windows) or `~/.local/share/sysmon/wmonitor.db` (Linux).

---

## 3. Running the Central W-Monitor Hub

### Environment Variables & Credentials

W-Monitor enforces a strict credential hierarchy to prevent secrets from leaking in process lists (`ps` / Task Manager).

| Variable | Description | Example |
|---|---|---|
| `WMONITOR_DB` | Database backend | `postgres` (or `sqlite`) |
| `WMONITOR_DB_DSN` | PostgreSQL connection string | `postgres://user:pass@host:5432/db?sslmode=require` |
| `WMONITOR_MODE` | Server operating mode | `hub` |
| `WMONITOR_PORT` | HTTP dashboard port | `8080` or `10000` |
| `WMONITOR_API_KEY` | Hub default API key (optional) | Prefer `config.env` / env; never put this on the Windows service command line |
| `WMONITOR_TRUSTED_PROXIES` | Reverse-proxy IPs or CIDRs allowed to set `X-Forwarded-Proto` | `10.0.0.0/8` (empty = never trust the header) |

Remote PostgreSQL DSNs must include `sslmode=require`, `verify-ca`, or `verify-full`. Loopback hosts may use `sslmode=disable`. `-dsn` remains visible in the process list; use `WMONITOR_DB_DSN` or `-dsn-file` in production.

Windows installer writes `%ProgramData%\wmonitor\config.env` with a SYSTEM + Administrators ACL. Linux installer writes `/etc/wmonitor/config.env` mode `600`. Both are loaded at process start; service arguments do not include `-api-key`, `-dsn`, or `-enroll-code`.

#### Setting Environment Variables on Windows:
```powershell
$env:WMONITOR_DB = "postgres"
$env:WMONITOR_DB_DSN = "postgres://avnadmin:pass@pg-host.aivencloud.com:15432/defaultdb?sslmode=require"
$env:WMONITOR_MODE = "hub"
$env:WMONITOR_PORT = "8080"
```

#### Setting Environment Variables on Linux:
```bash
export WMONITOR_DB="postgres"
export WMONITOR_DB_DSN="postgres://avnadmin:pass@pg-host.aivencloud.com:15432/defaultdb?sslmode=require"
export WMONITOR_MODE="hub"
export WMONITOR_PORT="8080"
```

---

### Starting the Hub in Foreground

```powershell
# Windows
.\wmonitor.exe -hub -db postgres -port 8080

# Linux
./wmonitor_linux -hub -db postgres -port 8080
```

**Startup Log Output:**
```text
2026/08/22 16:00:00 [wmonitor] DB backend: postgres @ pg-host.aivencloud.com/defaultdb
2026/08/22 16:00:00 [server] hub mode enabled — POST /api/ingest requires a registered API key
2026/08/22 16:00:00 [server] listening on http://localhost:8080/
```

---

### Deploying Hub on Cloud (Render.com)

The repository includes a [render.yaml](file:///c:/Users/markv/Desktop/w-monitor/render.yaml) blueprint:
1. Connect your Git repository to Render.
2. In Render environment settings, configure:
   - `WMONITOR_DB_DSN`: Your PostgreSQL DSN string.
   - `WMONITOR_MODE`: `hub`
   - `WMONITOR_DB`: `postgres`
3. Render will deploy and expose your Hub at `https://your-service-name.onrender.com`.

---

## 4. Client Onboarding & API Key Management

### Typed credentials (P1.03)

Do not treat client display names as identity. Each credential has a kind (`enroll`, `agent`, `read`, `admin`, `legacy`) and a scope (`enroll`, `ingest`, `read`, `admin`, `all`). Platform administration requires `kind=admin` **and** `scope=admin`. A legacy key with `scope=all` can still read/ingest **that tenant**; it cannot call `/api/admin/clients` or mint enrollment codes.

- Dashboard login uses a `wmr_` **read** token (`-add-client`).
- Agents receive a `wma_` **ingest** token bound to one `server_id` at enrollment. That token cannot write another server's rows.
- First enrollment of a `server_id` needs only the handshake code. Replacement needs the current `wma_` token in `POST /api/enroll` (`current_token`) **or** platform-admin `POST /api/admin/agents/rotate`. A concurrent enroll for the same identity cannot create a second active token.
- A lost enroll HTTP reply is recovered for 5 minutes (same code + `server_id` returns the same token, no extra use). After first ingest or TTL, recovery ends.
- `WM-XXXX-XXXX-XXXX` enrollment codes are consumed only at `POST /api/enroll`. Presenting them as `X-API-Key` is rejected.
- Only the SHA-256 hash is stored. Tokens are hashed as exact trimmed bytes; enrollment codes are normalized then hashed.
- Duplicate `client_name` values never pick a tenant. Pass `tenant_id` when creating an enrollment code, or use unique names.

Hub start does **not** auto-import `clients_registry.csv`. Use `-import-clients` explicitly; existing or revoked hashes are left unchanged. `WMONITOR_API_KEY` / `WMONITOR_ADMIN_TOKEN` are registered on first boot only when the hash is absent.

### Adding a New Client Organization

Run the `-add-client` command pointing at the Hub database:

```powershell
.\wmonitor.exe -db postgres -add-client "AcmeCorp"
```

**Output:**
```text
Client:     AcmeCorp
Tenant ID:  t_a8f3b219c0de447192bc55ef812034aa
Read Token (Dashboard): wmr_...

Store this token now. Use it to log in to the central dashboard.
```

Then issue an enrollment code for agents:

```powershell
.\wmonitor.exe -new-enroll-code "AcmeCorp" -ttl 72h -max-uses 25
```

### Opaque tenant migration (SQLite)

If historical rows still use a raw API key as `tenant_id`:

1. Stop the Hub.
2. Copy/verify a restorable backup (the command also writes one).
3. Run:

```powershell
.\wmonitor.exe -migrate-opaque-tenants -opaque-tenant-backup-dir C:\backup\wmonitor-opaque
```

```bash
./wmonitor -migrate-opaque-tenants -opaque-tenant-backup-dir /var/backups/wmonitor-opaque
```

Rollback: keep the Hub stopped and replace the live SQLite file with the backup. Do not downgrade into a build that stores raw keys as tenant IDs or that auto-imports CSV. PostgreSQL remap is not included in P1.03 — snapshot PG yourself.

---

### Auditing Registered Clients

To view all active client organizations and their last activity timestamps:

```powershell
.\wmonitor.exe -db postgres -list-clients
```

**Output:**
```text
CLIENT               TENANT                                 STATUS     LAST SEEN            KEY HASH (prefix)
AcmeCorp             t_a8f3b219c0de447192bc55ef812034aa     active     2026-08-22 15:45:10  7a1f89bc430e
DemoClient           t_6f1c432098ba418301ec99901452abcd     active     2026-08-22 14:12:00  8cdb1d068df8
```

---

### Revoking Client Access

If an assessment is concluded or credentials need immediate invalidation:

```powershell
.\wmonitor.exe -db postgres -revoke-client "AcmeCorp"
```
*Hub instances reject revoked credentials immediately on the instance that performed the revoke. Other replicas refresh `auth_epoch` at least every 5 seconds (`AuthRevocationMaxDelay`); a warmed positive cache cannot outlive credential expiry.*

Hub CLI exports (`-export-csv`, `-export-txt`, `-assessment-report`) require `-tenant t_<hex>` for hub data. Omitting `-tenant` reads only the standalone tenant `t_local`, never every customer.

### Rotating a single agent token (operator approval)

When the machine cannot prove the current token (lost credential file after the 5-minute handshake window):

```powershell
# Platform admin token in X-API-Key
Invoke-RestMethod -Method POST -Uri "https://your-hub/api/admin/agents/rotate" `
  -Headers @{ "X-API-Key" = $adminToken; "Content-Type" = "application/json" } `
  -Body '{"server_id":"srv-app-01","tenant_id":"t_a8f3b219c0de447192bc55ef812034aa"}'
```

```bash
curl -sS -X POST "https://your-hub/api/admin/agents/rotate" \
  -H "X-API-Key: $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"server_id":"srv-app-01","tenant_id":"t_a8f3b219c0de447192bc55ef812034aa"}'
```

The JSON response contains a new `wma_` token. Place it in the agent credential store. The previous token is revoked; replicas reject it within 5 seconds.

---

## 5. Universal Binary Distribution & Fleet Rollout

### Building Universal Binaries

Run the universal builder script from the repository root:

```powershell
.\build_release.ps1
```

This compiles:
- `wmonitor.exe` (Windows x64 generic binary)
- `wmonitor_linux` (Linux x64 generic binary)

Package these binaries alongside `install.ps1` and `install.sh` to provide to client teams.

---

### Windows Server Fleet Rollout

Provide the following single-line command to the client's Windows administrator (runs in Administrator PowerShell):

```powershell
.\install.ps1 -Mode agent -HubUrl "https://your-hub.onrender.com" -ApiKey "J8q7xKv9mP2LzY10aB+cdE4fGhIjKlMnOpQrStUvWxY="
```

**What `install.ps1` does automatically:**
1. Installs `wmonitor.exe` to `C:\Program Files\Sysmon\`.
2. Creates and locks `%LOCALAPPDATA%\Sysmon\config.env` with Windows ACLs (SYSTEM and current admin only).
3. Registers and starts the `wmonitor` Windows Service with startup type *Automatic*.
4. Generates a persistent local server identifier in `%LOCALAPPDATA%\Sysmon\agent_id`.

---

### Linux Server Fleet Rollout

For Linux target servers (Ubuntu, Debian, RHEL, Rocky, CentOS, Alma):

```bash
sudo ./install.sh --mode agent --hub-url "https://your-hub.onrender.com" --api-key "J8q7xKv9mP2LzY10aB+cdE4fGhIjKlMnOpQrStUvWxY="
```

**What `install.sh` does automatically:**
1. Installs binary to `/usr/local/bin/wmonitor`.
2. Writes credentials to `/etc/wmonitor/config.env` (permissions `0600` root-only).
3. Installs and enables the `systemd` service (`wmonitor.service`).

---

### Automated Mass Deployment (GPO, Ansible, Cloud-Init)

#### Active Directory Group Policy (GPO Startup Script):
```powershell
\\domain.local\sysvol\wmonitor\install.ps1 -Mode agent -HubUrl "https://hub.example.com" -ApiKey "J8q7x..."
```

#### Ansible Playbook Task:
```yaml
- name: Deploy W-Monitor Agent
  win_shell: |
    C:\Temp\install.ps1 -Mode agent -HubUrl "https://hub.example.com" -ApiKey "{{ wmonitor_api_key }}"
```

---

## 6. Generating Assessment Reports & Migration Deliverables

At the conclusion of the monitoring period (e.g. 7 days, 14 days, or 30 days), generate deliverables directly from the PostgreSQL backend.

### Self-Contained HTML Assessment Report

Generates a publication-ready, interactive HTML report with resource percentiles, disk IOPS distributions, bandwidth analysis, and target cloud VM sizing recommendations:

```powershell
# Generate report for the last 30 days (720 hours)
.\wmonitor.exe -db postgres -assessment-report AcmeCorp_Migration_Assessment.html -since 720h
```

**Report Highlights:**
1. **Executive Fleet Summary:** Total physical/virtual cores, total fleet RAM, aggregated disk usage.
2. **Per-Server Sizing Recommendations:**
   - **Minimum Cloud VM Spec:** Peak observed usage + 20% buffer.
   - **Recommended Cloud VM Spec:** Peak observed usage &times; 2.0 (for headroom and traffic bursts).
3. **Network & IOPS Profile:** Ingress/egress split, average vs peak IOPS.
4. **Print-to-PDF Ready:** Fully self-contained CSS layout for browser PDF export.

---

### Raw CSV Data Dump for Custom Financial Modeling

For Excel financial modeling, TCO calculators, and custom pivot tables:

```powershell
.\wmonitor.exe -db postgres -export-csv AcmeCorp_Metrics_Dump.csv -since 720h
```

---

## 7. Security Model, Network Rules & Troubleshooting

### Network & Firewall Rules

| Endpoint | Direction | Protocol | Port | Description |
|---|---|---|---|---|
| **Client Servers (Agents)** | **Outbound only** | HTTPS/TCP | `443` or `8080` | Pushes metrics to Hub. **Zero inbound ports needed.** |
| **Central Hub Server** | **Inbound** | HTTPS/TCP | `8080` or `443` | Accepts ingest POSTs and serves dashboard. |
| **PostgreSQL Database** | **Inbound from Hub only** | TCP | `5432` or `15432` | Storage backend. Client agents never connect to DB. |

---

### Troubleshooting Checklist

#### 1. Agent logs `[server] rejected unknown API key`:
- **Cause:** The API key passed to the agent has not been registered in PostgreSQL.
- **Fix:** Run `.\wmonitor.exe -db postgres -add-client "<ClientName>"` on the Hub or verify with `.\wmonitor.exe -db postgres -list-clients`.

#### 2. Agent fails with `connection refused` or timeout:
- **Cause:** Firewall blocking Hub port or incorrect Hub URL.
- **Fix:** Test connectivity from client: `Test-NetConnection -ComputerName <hub-host> -Port 8080` (Windows) or `nc -zv <hub-host> 8080` (Linux).

#### 3. Service status check on client server:
- **Windows:** `Get-Service wmonitor`
- **Linux:** `systemctl status wmonitor`

#### 4. Checking recent agent logs:
- **Windows:** View Windows Event Viewer &rarr; *Application Logs* (Source: `wmonitor`) or run interactively `wmonitor -agent <hub-url> -api-key <key>`.
- **Linux:** `journalctl -u wmonitor -f`

---

### Test isolation (do not run the suite on enrolled hosts)

`go test` used to write and delete the live machine token (`%PROGRAMDATA%\wmonitor\token.dat` on Windows, `/etc/wmonitor/token.json` or `~/.local/share/sysmon/token.json` on Linux). That is V22.

**Windows (PowerShell) and Linux:** always set isolation, or rely on CI which already does:

```powershell
$env:WMONITOR_TEST_ISOLATION = "1"
go test ./agent ./storage ./internal/fsroot
```

```bash
export WMONITOR_TEST_ISOLATION=1
go test ./agent ./storage ./internal/fsroot
```

CI (`.github/workflows/ci.yml`) sets `WMONITOR_TEST_ISOLATION=1`, records `go version` / `go list -m all` / `go mod verify`, uses Go **1.26.6**, and pins `staticcheck@v0.6.1` and `govulncheck@v1.1.4` (not `@latest`). Module language version in `go.mod` remains `go 1.26.5`.

### Retention containment (V07) — backup before any future enablement

Hourly **downsampling and automatic 30-day purge are disabled** until P2.03. `/api/health` includes a `retention` object (`downsampling_enabled: false`). Disk growth is bounded by `WMONITOR_DISK_BUDGET_BYTES` (default 10 GiB); exhaustion is reported as an error, not silent deletion.

**Before any future rollout that re-enables retention:**
1. Verify a restorable backup of SQLite/PostgreSQL.
2. If a live secret was ever exposed (CSV auto-import, logs, process list), rotate it with an authorized operator procedure. **W-Monitor does not rotate credentials automatically.** CSV is no longer auto-imported at Hub start (P1.03).
3. Do not downgrade to a build that still calls `downsampleMetrics`.

### Identity rollback (P1.03)

Schema/identity changes are not a blind downgrade. After `-migrate-opaque-tenants`, restore the backup SQLite file written under `-opaque-tenant-backup-dir` rather than reintroducing plaintext tenant IDs. A revoked credential must stay revoked across restart and CSV drop.







.\wmonitor.exe -db postgres -dsn "postgres://avnadmin:PASSWORD@pg-service.aivencloud.com:15432/defaultdb?sslmode=require" -add-client "ClientName"


.\install.ps1 -Mode agent -HubUrl https://wmonitor-hub.onrender.com -ApiKey 3qzmUw7d+QfQIZ0MDvyloUeOxiYAnkNwVGrHhwp79g0=