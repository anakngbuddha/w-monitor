# W-Monitor Administrator & Operations Guide

This guide is intended for administrators, DevOps engineers, and cloud architects conducting pre-migration assessments with **W-Monitor**. It covers running the database locally, configuring the central Hub, managing client agents, securing credentials, and generating client-ready cloud migration assessment deliverables.

---

## Table of Contents

1. [Architecture & Assessment Workflow](#1-architecture--assessment-workflow)
2. [Setting Up the Database on Your Local Machine](#2-setting-up-the-database-on-your-local-machine)
   - [Option A: Local PostgreSQL with Docker (Recommended)](#option-a-local-postgresql-with-docker-recommended)
   - [Option B: Native Local PostgreSQL (Windows / Linux)](#option-b-native-local-postgresql-windows--linux)
   - [Option C: Managed Cloud PostgreSQL (Aiven Free Tier)](#option-c-managed-cloud-postgresql-aiven-free-tier)
   - [Option D: Local SQLite (Zero-Setup Alternative)](#option-d-local-sqlite-zero-setup-alternative)
3. [Running the Central W-Monitor Hub](#3-running-the-central-w-monitor-hub)
   - [Environment Variables & Credential Management](#environment-variables--credential-management)
   - [Starting the Hub in Foreground](#starting-the-hub-in-foreground)
   - [Installing the Hub as a Background Service](#installing-the-hub-as-a-background-service)
4. [Client Agent Rollout & Management](#4-client-agent-rollout--management)
   - [Agent Authentication Model](#agent-authentication-model)
   - [Client Install Command Generation](#client-install-command-generation)
   - [Network & Firewall Considerations](#network--firewall-considerations)
5. [Generating Assessment Reports & Migration Deliverables](#5-generating-assessment-reports--migration-deliverables)
   - [Self-Contained HTML Assessment Report](#self-contained-html-assessment-report)
   - [CSV Export for Custom Pivot Tables & Charts](#csv-export-for-custom-pivot-tables--charts)
6. [Database Schema & Direct Queries](#6-database-schema--direct-queries)
7. [Maintenance, Retention & Troubleshooting](#7-maintenance-retention--troubleshooting)

---

## 1. Architecture & Assessment Workflow

```
┌────────────────────────────────────────────────────────┐
│                   CLIENT ENVIRONMENT                   │
│                                                        │
│   ┌──────────────┐     ┌──────────────┐                │
│   │ Client Node 1│     │ Client Node 2│    ...         │
│   │  (Agent)     │     │  (Agent)     │                │
│   └──────┬───────┘     └──────┬───────┘                │
│          │                    │                        │
└──────────┼────────────────────┼────────────────────────┘
           │ HTTPS POST /api/ingest                      
           │ (X-API-Key Header, NO DB credentials)       
           ▼                                             
┌────────────────────────────────────────────────────────┐
│                   ADMIN / HUB NODE                     │
│                                                        │
│   ┌────────────────────────────────────────────────┐   │
│   │              W-Monitor Hub Server              │   │
│   │   - Validates X-API-Key                        │   │
│   │   - Serves Web Dashboard (Port 8080)           │   │
│   │   - Generates HTML Assessment Reports          │   │
│   └───────────────────────┬────────────────────────┘   │
│                           │                            │
│                           ▼                            │
│             ┌───────────────────────────┐              │
│             │    Database (PostgreSQL)  │              │
│             │  Docker / Local / Aiven   │              │
│             └───────────────────────────┘              │
└────────────────────────────────────────────────────────┘
```

**Core Principle:** Client machines only ever receive the Hub URL and a shared API key. They **never** receive direct database credentials or network access to Postgres.

---

## 2. Setting Up the Database on Your Local Machine

### Option A: Local PostgreSQL with Docker (Recommended)

Running Postgres in Docker on your local laptop or management workstation provides an isolated, repeatable backend.

#### 1. Run PostgreSQL container:
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

#### 2. Verify connectivity:
```bash
docker exec -it wmonitor-postgres psql -U wmonitor -d wmonitor_db -c "SELECT 1;"
```

#### 3. Your Local DSN:
```
postgres://wmonitor:secretpassword@localhost:5432/wmonitor_db?sslmode=disable
```

---

### Option B: Native Local PostgreSQL (Windows / Linux)

If you have PostgreSQL installed natively on your OS:

#### 1. Create user and database in `psql`:
```sql
CREATE USER wmonitor WITH PASSWORD 'secretpassword';
CREATE DATABASE wmonitor_db OWNER wmonitor;
GRANT ALL PRIVILEGES ON DATABASE wmonitor_db TO wmonitor;
```

#### 2. DSN string:
```
postgres://wmonitor:secretpassword@localhost:5432/wmonitor_db?sslmode=disable
```

---

### Option C: Managed Cloud PostgreSQL (Aiven Free Tier)

For multi-site assessments where the Hub runs on a laptop roaming across different networks:

1. Create a free-tier PostgreSQL service at [aiven.io](https://aiven.io).
2. Retrieve the Service URI (DSN) from the Aiven console. Example:
   ```
   postgres://avnadmin:PASSWORD@pg-service-name.aivencloud.com:15432/defaultdb?sslmode=require
   ```
3. Test connectivity with `psql` or DBeaver before launching the Hub.

---

### Option D: Local SQLite (Zero-Setup Alternative)

For single-machine or early lightweight testing, SQLite requires zero setup:
- Default DB location: `%LOCALAPPDATA%\Sysmon\wmonitor.db` (Windows) or `~/.local/share/sysmon/wmonitor.db` (Linux).
- W-Monitor creates and migrates the database automatically on startup.

---

## 3. Running the Central W-Monitor Hub

### Environment Variables & Credential Management

W-Monitor enforces a strict credential hierarchy to prevent passwords from showing in the process list (`ps` / Task Manager) or shell history.

| Priority | Method | Recommended For |
|:---:|---|---|
| **1 (Primary)** | `WMONITOR_DB_DSN` & `WMONITOR_API_KEY` Environment Variables | Production & Services |
| **2** | `-dsn-file=<path>` Flag | Automated deployments with secret files |
| **3** | `-dsn="postgres://..."` Flag | One-off local testing only (logs a warning) |

#### Setting Environment Variables on Windows:
```powershell
# In PowerShell (for current session):
$env:WMONITOR_DB_DSN = "postgres://wmonitor:secretpassword@localhost:5432/wmonitor_db?sslmode=disable"
$env:WMONITOR_API_KEY = "my-secure-32-char-random-api-key"

# Machine-level persistent (requires Admin):
[Environment]::SetEnvironmentVariable("WMONITOR_DB_DSN", "postgres://wmonitor:secretpassword@localhost:5432/wmonitor_db?sslmode=disable", "Machine")
[Environment]::SetEnvironmentVariable("WMONITOR_API_KEY", "my-secure-32-char-random-api-key", "Machine")
```

#### Setting Environment Variables on Linux:
```bash
export WMONITOR_DB_DSN="postgres://wmonitor:secretpassword@localhost:5432/wmonitor_db?sslmode=disable"
export WMONITOR_API_KEY="my-secure-32-char-random-api-key"
```

---

### Starting the Hub in Foreground

Once the database is running and environment variables are set:

```bash
# Windows
.\wmonitor.exe -hub -db postgres -port 8080

# Linux
./wmonitor_linux -hub -db postgres -port 8080
```

**Startup Log Output:**
```
2026/08/18 17:00:00 [wmonitor] DB backend: postgres @ localhost/wmonitor_db
2026/08/18 17:00:00 [server] hub mode enabled — POST /api/ingest accepting agent data
2026/08/18 17:00:00 [server] listening on http://localhost:8080
```
*(Notice that passwords are never printed to the logs).*

---

### Installing the Hub as a Background Service

**Windows (Administrator PowerShell):**
```powershell
.\install.ps1 -Mode hub -Db postgres -Dsn "postgres://wmonitor:secretpassword@localhost:5432/wmonitor_db?sslmode=disable" -ApiKey "my-secure-api-key"
```

**Linux (root):**
```bash
sudo ./install.sh --mode hub --db postgres --dsn "postgres://wmonitor:secretpassword@localhost:5432/wmonitor_db?sslmode=disable" --api-key "my-secure-api-key"
```

---

## 4. Client Agent Rollout & Management

### Agent Authentication Model

- Agents authenticate to the Hub via HTTP header:
  `X-API-Key: <WMONITOR_API_KEY>`
- The Hub uses **constant-time string comparison** (`crypto/subtle.ConstantTimeCompare`) to defend against timing attacks.
- Unauthorized requests return `401 Unauthorized` before executing any database queries.

---

### Client Install Command Generation

Generate an install command for your client machines:

#### For Windows Client Servers:
```powershell
# Execute in Administrator PowerShell on client node:
.\install.ps1 -Mode agent -HubUrl "https://<your-hub-ip-or-dns>:8080" -ApiKey "my-secure-api-key"
```

#### For Linux Client Servers:
```bash
# Execute as root on client node:
sudo ./install.sh --mode agent --hub-url "https://<your-hub-ip-or-dns>:8080" --api-key "my-secure-api-key"
```

---

### Network & Firewall Considerations

- Open incoming port `8080` (or your chosen `-port`) on the Hub host's firewall / security group:
  ```powershell
  # Windows Defender Firewall rule for Hub (Admin PowerShell):
  New-NetFirewallRule -DisplayName "W-Monitor Hub" -Direction Inbound -Protocol TCP -LocalPort 8080 -Action Allow
  ```
  ```bash
  # Linux ufw:
  sudo ufw allow 8080/tcp
  ```
- Agents require **outbound** TCP access to `<hub-host>:8080`.
- No inbound ports need to be opened on client agent servers.

---

## 5. Generating Assessment Reports & Migration Deliverables

### Self-Contained HTML Assessment Report

Generate a cloud-sizing assessment report at the end of the assessment window:

```bash
# Generate HTML assessment report covering the last 30 days (720 hours)
wmonitor -db postgres -assessment-report client_assessment_report.html -since 720h
```

**Report Features:**
1. **System Baseline:** vCPU, RAM, Disk capacities.
2. **CPU & Memory Trends:** Average and peak utilization percentages.
3. **Disk IOPS Profile:** Peak and average read/write IOPS.
4. **Traffic Breakdown:** Total sent/received GB, peak bandwidth rate.
5. **Concurrent User Activity:** Peak active client sessions.
6. **Target Sizing Recommendations:**
   - **Minimum Cloud Specs:** Peak observed resource usage + 20% safety margin.
   - **Recommended Cloud Specs:** Peak observed usage &times; 2.0 (for growth and traffic burst headroom).
7. **Printable to PDF:** Clean CSS styling designed for browser print-to-PDF.

---

### CSV Export for Custom Pivot Tables & Charts

Export granular timestamped data to CSV for custom Excel modeling:

```bash
wmonitor -db postgres -export-csv client_data_dump.csv -since 720h
```

---

## 6. Database Schema & Direct Queries

The Postgres backend automatically provisions and migrates the schema on startup:

### `metrics` Table Structure

| Column | Type | Description |
|---|---|---|
| `id` | `SERIAL PRIMARY KEY` | Auto-incrementing row ID |
| `timestamp` | `BIGINT NOT NULL` | Epoch seconds (`idx_metrics_ts`) |
| `server_id` | `TEXT NOT NULL` | Identifier of agent (`idx_metrics_server`) |
| `hostname` | `TEXT NOT NULL` | Node OS hostname |
| `cpu_pct` | `DOUBLE PRECISION` | CPU usage percentage |
| `mem_pct` | `DOUBLE PRECISION` | Memory usage percentage |
| `disk_free_gb` | `DOUBLE PRECISION` | Free storage on root disk |
| `net_sent_bytes` | `BIGINT` | Cumulative network bytes sent |
| `net_recv_bytes` | `BIGINT` | Cumulative network bytes received |
| `net_sent_external`| `BIGINT` | Public/External NIC bytes sent |
| `net_recv_external`| `BIGINT` | Public/External NIC bytes received |
| `net_sent_internal`| `BIGINT` | VPC/Internal NIC bytes sent |
| `net_recv_internal`| `BIGINT` | VPC/Internal NIC bytes received |
| `disk_iops` | `DOUBLE PRECISION` | Measured IOPS (read + write) |
| `net_mbps` | `DOUBLE PRECISION` | Instantaneous bandwidth rate (MB/s) |
| `concurrent_users`| `INT` | Active connections / user count |

---

### Useful SQL Queries for Analysis

```sql
-- 1. List all active servers sending metrics in the last 24 hours:
SELECT server_id, hostname, to_timestamp(MAX(timestamp)) AS last_seen, COUNT(*) AS samples
FROM metrics
WHERE timestamp >= EXTRACT(EPOCH FROM (NOW() - INTERVAL '24 hours'))
GROUP BY server_id, hostname
ORDER BY last_seen DESC;

-- 2. Peak resource utilization per server:
SELECT 
    server_id,
    ROUND(MAX(cpu_pct)::numeric, 2) AS peak_cpu_pct,
    ROUND(MAX(mem_pct)::numeric, 2) AS peak_mem_pct,
    ROUND(MAX(disk_iops)::numeric, 1) AS peak_iops,
    ROUND(MAX(net_mbps)::numeric, 2) AS peak_net_mbps
FROM metrics
GROUP BY server_id;

-- 3. External vs Internal Traffic totals (GB) across the fleet:
SELECT 
    server_id,
    ROUND((MAX(net_sent_external) - MIN(net_sent_external)) / (1024.0^3)::numeric, 2) AS ext_sent_gb,
    ROUND((MAX(net_recv_external) - MIN(net_recv_external)) / (1024.0^3)::numeric, 2) AS ext_recv_gb,
    ROUND((MAX(net_sent_internal) - MIN(net_sent_internal)) / (1024.0^3)::numeric, 2) AS int_sent_gb,
    ROUND((MAX(net_recv_internal) - MIN(net_recv_internal)) / (1024.0^3)::numeric, 2) AS int_recv_gb
FROM metrics
GROUP BY server_id;
```

---

## 7. Maintenance, Retention & Troubleshooting

### Troubleshooting Checklist

1. **Agent shows `401 Unauthorized` in logs:**
   - Verify that `WMONITOR_API_KEY` on the Hub matches the `-ApiKey` parameter given to the client agent.
2. **Agent cannot connect to Hub (`connection refused` or timeout):**
   - Check if the Hub machine firewall has port `8080` open.
   - Test connectivity from the client node: `Test-NetConnection -ComputerName <hub-ip> -Port 8080` (Windows) or `nc -zv <hub-ip> 8080` (Linux).
3. **Database connection error on Hub startup:**
   - Verify Postgres is running: `docker ps` or `pg_isready`.
   - Test DSN using `psql "<dsn-string>"`.
4. **Database Backup:**
   ```bash
   # Backup Postgres database:
   docker exec -t wmonitor-postgres pg_dump -U wmonitor wmonitor_db > wmonitor_backup_$(date +%Y%m%d).sql
   ```
