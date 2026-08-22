# W-Monitor User & Operator Guide
## Universal System Monitoring & Pre-Migration Cloud Sizing Agent

Welcome to **W-Monitor**, a lightweight, high-performance, single-binary system monitoring and pre-migration assessment tool. W-Monitor collects granular utilization metrics (CPU, RAM, Disk, IOPS, Network bandwidth, and Top Processes) and provides an embedded real-time web dashboard, automated export utilities, and publication-ready cloud migration assessment reports.

---

## Table of Contents

1. [Architecture & Deployment Modes](#1-architecture--deployment-modes)
2. [How the Organization API Key Works](#2-how-the-organization-api-key-works)
3. [Quick Start: Windows Client Servers](#3-quick-start-windows-client-servers)
4. [Quick Start: Linux Client Servers](#4-quick-start-linux-client-servers)
5. [Standalone Local Monitoring (Zero-Cloud / Offline)](#5-standalone-local-monitoring-zero-cloud--offline)
6. [Time-Bounded Assessments (e.g. 1 hour, 24 hours, 7 days)](#6-time-bounded-assessments)
7. [Managing the Background Service](#7-managing-the-background-service)
8. [Generating Assessment Reports & Data Exports](#8-generating-assessment-reports--data-exports)
9. [Web Dashboard Walkthrough](#9-web-dashboard-walkthrough)
10. [Cloud Sizing Metrics & Cost Forecasting](#10-cloud-sizing-metrics--cost-forecasting)

---

## 1. Architecture & Deployment Modes

W-Monitor uses **universal, generic binaries** (`wmonitor.exe` for Windows, `wmonitor_linux` for Linux). The same binary runs in two modes:

### Mode 1: Client Agent Mode *(Recommended for Cloud Migration Assessments)*
- Deployed across target client servers (1 to 100+ servers per client).
- **Zero Database Footprint:** No database engine or local storage required on target machines.
- **Zero Inbound Ports:** Agents only make outbound HTTPS requests to the central Hub.
- **No Database Passwords:** Agents only hold an Organization API Key and never see PostgreSQL credentials.
- Pushes metric snapshots every 10 seconds to the central Hub via `POST /api/ingest`.

### Mode 2: Standalone / Hub Mode
- Runs on a central server (e.g., Render, cloud VM, or laptop).
- Accepts metric streams from all client agents, isolated by organization/tenant.
- Serves the real-time web dashboard on port `8080` (configurable).
- Stores data in **PostgreSQL** (centralized multi-tenant) or **SQLite** (local single-server).

---

## 2. How the Organization API Key Works

When assessing a client company (e.g., *Acme Corp*):

```
┌────────────────────────────────────────────────────────────┐
│                  ACME CORP SERVERS (Fleet)                 │
│                                                            │
│  All servers run standard wmonitor.exe                     │
│  All servers use SAME Acme Corp API Key                    │
│                                                            │
│   Server 01          Server 02          Server 03 ...      │
│  (ID: srv-01)       (ID: srv-02)       (ID: srv-03)        │
│       │                  │                  │              │
│       └──────────────────┼──────────────────┘              │
│                          │ Outbound HTTPS                  │
│                          │ Header: X-API-Key: <Acme_Key>   │
│                          ▼                                 │
│             ┌─────────────────────────┐                    │
│             │  Central W-Monitor Hub  │                    │
│             │  (PostgreSQL Backend)   │                    │
│             └─────────────────────────┘                    │
└────────────────────────────────────────────────────────────┘
```

1. **One Key Per Client:** The assessment team generates **one API key for the entire client organization** on the Hub.
2. **Every Server is Identified Automatically:** Each server generates a persistent `server_id` and reports its hostname.
3. **Automatic Fleet Grouping:** All servers with the same organization key appear grouped together under that client's dashboard and assessment reports.

---

## 3. Quick Start: Windows Client Servers

### Step 1: Package Contents
You will receive:
- `wmonitor.exe` (Universal Windows 64-bit binary)
- `install.ps1` (Automated service installer)

### Step 2: One-Line Service Installation
Open **PowerShell as Administrator** and execute:

```powershell
.\install.ps1 -Mode agent -HubUrl "https://your-hub.example.com" -ApiKey "your-organization-api-key"
```

### What Happens Automatically:
- Copies `wmonitor.exe` to `C:\Program Files\Sysmon\wmonitor.exe`.
- Adds `C:\Program Files\Sysmon` to the System `PATH`.
- Writes credentials securely to `%LOCALAPPDATA%\Sysmon\config.env` locked with Windows ACLs (SYSTEM & Admin only).
- Registers and starts the `wmonitor` Windows Service with startup type *Automatic*.
- The agent immediately begins pushing metrics to your Hub.

### Step 3: Interactive / Foreground Testing (Optional)
If you want to test running in the terminal without installing a service:

```powershell
.\wmonitor.exe -agent "https://your-hub.example.com" -api-key "your-organization-api-key"
```

---

## 4. Quick Start: Linux Client Servers

### Step 1: Package Contents
You will receive:
- `wmonitor_linux` (Universal Linux 64-bit binary)
- `install.sh` (Automated systemd installer)

### Step 2: One-Line Service Installation
Make `install.sh` executable and run as `root`:

```bash
chmod +x install.sh
sudo ./install.sh --mode agent --hub-url "https://your-hub.example.com" --api-key "your-organization-api-key"
```

### What Happens Automatically:
- Installs binary to `/usr/local/bin/sysmon`.
- Stores credentials in `/etc/wmonitor/config.env` (permissions `0600` root-only).
- Registers and enables the `systemd` service (`sysmon.service`).
- Starts collecting and streaming metrics immediately.

---

## 5. Standalone Local Monitoring (Zero-Cloud / Offline)

If you are performing an offline assessment on a single isolated machine with no internet connection:

### Windows:
```powershell
# Run in terminal (SQLite database created automatically in %LOCALAPPDATA%\Sysmon\wmonitor.db)
.\wmonitor.exe

# Or install as local background service:
.\install.ps1 -Mode hub -Db sqlite -ApiKey "local-pass"
```

### Linux:
```bash
# Run in terminal
./wmonitor_linux

# Or install as local systemd service:
sudo ./install.sh --mode hub --db sqlite --api-key "local-pass"
```

Open your browser to `http://localhost:8080` to view the local real-time dashboard.

---

## 6. Time-Bounded Assessments

To run W-Monitor for an exact duration (e.g. 1 hour load test, 24-hour baseline, or 7-day migration assessment) and have it automatically stop and generate reports upon completion:

Use the **`-run-for`** flag:

```powershell
# 1. Run for 1 hour as an Agent pushing to Hub, then stop:
.\wmonitor.exe -agent "https://hub.example.com" -api-key "your-key" -run-for 1h

# 2. Run for 24 hours locally, then auto-export a daily CSV upon exit:
.\wmonitor.exe -run-for 24h -export-filter daily

# 3. Run for 7 days (168 hours):
.\wmonitor.exe -run-for 168h
```

---

## 7. Managing the Background Service

### Windows Service Management

```powershell
# Check service status
Get-Service wmonitor

# Restart service
Restart-Service wmonitor

# Stop service
Stop-Service wmonitor

# Uninstall service completely
wmonitor -uninstall
```

### Linux systemd Service Management

```bash
# Check service status
sudo systemctl status sysmon

# View real-time streaming logs
sudo journalctl -u sysmon -f

# Restart service
sudo systemctl restart sysmon

# Stop service
sudo systemctl stop sysmon
```

---

## 8. Generating Assessment Reports & Data Exports

Generate cloud sizing reports directly from the collected data:

### 1. Self-Contained HTML Assessment Report (Printable to PDF)
Generates an interactive, publication-ready HTML deliverable with resource percentiles, IOPS distributions, bandwidth analysis, and target cloud VM recommendations:

```powershell
# Generate report covering the last 30 days (720 hours)
wmonitor -assessment-report cloud_assessment_report.html -since 720h
```

### 2. Granular CSV Export
Exports raw timestamped data suitable for custom Excel financial modeling:

```powershell
wmonitor -export-csv fleet_metrics_dump.csv -since 720h
```

### 3. Quick Plain-Text Terminal Summary
```powershell
wmonitor -export-txt summary.txt -since 168h
```

---

## 9. Web Dashboard Walkthrough

When viewing the dashboard at `http://localhost:8080` (or your Hub URL):

1. **Server Fleet Selector:** Filter between viewing the entire aggregated fleet or an individual server node.
2. **Time Window Filters:** Toggle between **24h** (raw 10s high-res data), **7d**, and **30d** (downsampled hourly).
3. **Core Metric Gauges & Trends:**
   - **CPU:** Average vs Peak core consumption.
   - **Memory:** Average vs Peak RAM utilization.
   - **Disk Free:** Minimum remaining storage headroom.
   - **Network Throughput:** Ingress/egress bandwidth with automatic split for **External (Internet)** vs **Internal (VPC)** traffic.
   - **Disk IOPS:** Measured real-time read and write IOPS.
   - **Concurrent Users:** Active application connections.
4. **Top Processes:** Real-time top 10 processes ranked by CPU and RAM consumption.
5. **Instant CSV Export:** Download data directly from the top navigation bar.

---

## 10. Cloud Sizing Metrics & Cost Forecasting

W-Monitor automatically calculates target cloud specifications:

- **Minimum Cloud VM Specs:** Peak measured utilization + 20% safety headroom.
- **Recommended Cloud VM Specs:** Peak measured utilization &times; 2.0 (for burst traffic and annual business growth).
- **Network Egress Isolation:** Automatically identifies public network interfaces to provide exact cloud egress bandwidth projections for AWS, Azure, and Google Cloud cost estimation.
