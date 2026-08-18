# W-Monitor User Guide

Welcome to **W-Monitor**, a lightweight, single-binary system monitoring and pre-migration assessment tool. W-Monitor collects high-frequency utilization data (CPU, memory, disk, network, disk IOPS, and active processes) and provides a sleek web dashboard, automated export utilities, and client-ready cloud migration assessment reports.

---

## Table of Contents

1. [Architectural Overview & Operating Modes](#architectural-overview--operating-modes)
2. [Choosing Which Database to Run (SQLite vs PostgreSQL vs Agent Zero-DB)](#choosing-which-database-to-run)
3. [Where to Get the API Key for Client Agents](#where-to-get-the-api-key-for-client-agents)
4. [Running for a Limited Time Only (e.g. 1h, 24h, 7d)](#running-for-a-limited-time-only-eg-1-hour-24-hours-7-days)
5. [Windows Guide](#windows-guide)
   - [Step 1: Downloading & Obtaining the Binary](#windows-step-1-downloading--obtaining-the-binary)
   - [Step 2: Installation as a Client Agent (Multi-Server Assessment)](#windows-step-2-installation-as-a-client-agent)
   - [Step 3: Installation as a Standalone Local Monitor](#windows-step-3-installation-as-a-standalone-local-monitor)
   - [Step 4: Running Interactively (Foreground Mode)](#windows-step-4-running-interactively)
   - [Step 5: Managing the Windows Service](#windows-step-5-managing-the-windows-service)
   - [Step 6: Generating Reports & Data Exports](#windows-step-6-generating-reports--data-exports)
   - [Windows File Locations & Security](#windows-file-locations--security)
6. [Linux Guide](#linux-guide)
   - [Step 1: Downloading & Obtaining the Binary](#linux-step-1-downloading--obtaining-the-binary)
   - [Step 2: Installation as a Client Agent (Multi-Server Assessment)](#linux-step-2-installation-as-a-client-agent)
   - [Step 3: Installation as a Standalone Local Monitor](#linux-step-3-installation-as-a-standalone-local-monitor)
   - [Step 4: Running Interactively (Foreground Mode)](#linux-step-4-running-interactively)
   - [Step 5: Managing the Systemd Service](#linux-step-5-managing-the-systemd-service)
   - [Step 6: Generating Reports & Data Exports](#linux-step-6-generating-reports--data-exports)
   - [Linux File Locations & Security](#linux-file-locations--security)
7. [Using the Web Dashboard](#using-the-web-dashboard)
8. [Advanced Metrics & Sizing Recommendations](#advanced-metrics--sizing-recommendations)

---

## Architectural Overview & Operating Modes

W-Monitor operates in two main modes:

1. **Client Agent Mode (Zero Database Footprint):**
   - Ideal for target client servers during pre-migration assessments.
   - The agent machine **never receives or touches database passwords or cloud credentials**.
   - Collects system metrics every 10 seconds and securely pushes them over HTTPS/HTTP to the central Hub via `POST /api/ingest` with an `X-API-Key` header.
   - Zero local database footprint and no open dashboard port on client machines.

2. **Standalone / Hub Mode:**
   - Runs locally on a single machine or acts as a central aggregator for multiple client agents (e.g., hosted on Render or your laptop).
   - Stores metrics in **SQLite** (default zero-config) or **PostgreSQL** (multi-server centralized, e.g. Aiven.io).
   - Serves the real-time web dashboard on port `8080` (customizable with `-port` or `$PORT`).

---

## Choosing Which Database to Run

W-Monitor supports two database backends:

| Backend | When to Use | How to Choose |
|---|---|---|
| **Local SQLite** *(Default)* | Single-server local monitoring or offline assessment on a client server | Just run `wmonitor` (or `wmonitor -db sqlite`). No setup required. Data is stored locally in `wmonitor.db`. |
| **Aiven / PostgreSQL** | Centralized Hub mode collecting metrics from 10+ client servers | Run with `-db postgres` and provide the DSN via the `WMONITOR_DB_DSN` environment variable. |
| **Client Agent** *(No DB)* | Target client servers pushing data to your Hub | Run with `-agent <hub-url> -api-key <key>`. **No local DB is created or needed on the client server.** |

### 1. Running with Local SQLite (Zero-Config)
```powershell
# Windows:
.\wmonitor.exe

# Linux:
./wmonitor_linux
```

### 2. Running with Aiven.io PostgreSQL (Central Hub)
```powershell
# Windows PowerShell:
$env:WMONITOR_DB_DSN = "postgres://avnadmin:PASSWORD@pg-host:11914/defaultdb?sslmode=require"
.\wmonitor.exe -hub -db postgres

# Linux:
export WMONITOR_DB_DSN="postgres://avnadmin:PASSWORD@pg-host:11914/defaultdb?sslmode=require"
./wmonitor_linux -hub -db postgres
```

---

## Where to Get the API Key for Client Agents

The **API Key** is a shared secret token that protects your central Hub (`POST /api/ingest`) from unauthorized data submissions.

### How it is Created:
- **If Hosting on Render**:
  - In your Render Dashboard under **Environment**, look for **`WMONITOR_API_KEY`**. Render generates a secure random key for you, or you can type any passphrase you choose (e.g., `client-cluster-key-2026`).
- **If Hosting on Your Own Server/Laptop**:
  - You decide the key when starting the Hub by setting `$env:WMONITOR_API_KEY = "my-secret-key"`.

### How to Give it to the Client:
Provide the key string to the client so their agent can authenticate with your Hub:
```powershell
# Windows:
.\wmonitor.exe -agent "https://<your-hub-url>" -api-key "client-cluster-key-2026"

# Linux:
./wmonitor_linux -agent "https://<your-hub-url>" -api-key "client-cluster-key-2026"
```

---

## Running for a Limited Time Only (e.g., 1 Hour, 24 Hours, 7 Days)

If you or your client only wants to run W-Monitor for a specific duration (e.g. during a 1-hour load test, a 24-hour baseline, or a 7-day pre-migration assessment) and then automatically stop and export reports:

Use the **`-run-for`** flag (`1h`, `30m`, `24h`, `168h` for 7 days):

### Windows Examples:

```powershell
# 1. Run for 1 hour, then automatically export a 24-hour summary CSV on exit:
.\wmonitor.exe -run-for 1h -export-filter daily

# 2. Run for 1 hour in standalone mode, then generate an HTML Assessment Report:
.\wmonitor.exe -run-for 1h
.\wmonitor.exe -assessment-report assessment_1h.html -since 1h

# 3. Run for 24 hours as an Agent pushing to your Hub, then exit:
.\wmonitor.exe -agent "https://your-hub.onrender.com" -api-key "your-key" -run-for 24h
```

### Linux Examples:

```bash
# 1. Run for 1 hour and auto-export a summary text report on exit:
./wmonitor_linux -run-for 1h -export-filter daily

# 2. Run for 1 hour in standalone mode, then generate an HTML Assessment Report:
./wmonitor_linux -run-for 1h
./wmonitor_linux -assessment-report assessment_1h.html -since 1h

# 3. Run for 24 hours in agent mode pushing to your Hub, then exit:
./wmonitor_linux -agent "https://your-hub.onrender.com" -api-key "your-key" -run-for 24h
```

---

## Windows Guide

### Windows Step 1: Downloading & Obtaining the Binary

You will typically receive a release package containing:
- `wmonitor.exe` (The compiled 64-bit Windows binary)
- `install.ps1` (Automated service installation script)

If you are building from source:
```powershell
# From the repository root:
.\build_release.ps1
```
This produces `wmonitor.exe` in your workspace.

---

### Windows Step 2: Installation as a Client Agent

If you are installing W-Monitor on a client machine to send metrics back to your central Hub:

1. Open **PowerShell as Administrator** (Right-click Start &rarr; *Windows PowerShell (Admin)* or *Terminal (Admin)*).
2. Navigate to the directory containing `wmonitor.exe` and `install.ps1`:
   ```powershell
   cd C:\path\to\wmonitor-package
   ```
3. Run the installer with `-Mode agent`, specifying the Hub URL and shared API Key:
   ```powershell
   .\install.ps1 -Mode agent -HubUrl "https://<hub-address>:8080" -ApiKey "<your-shared-api-key>"
   ```
4. **What happens automatically:**
   - Validates Administrator permissions.
   - Copies `wmonitor.exe` to `C:\Program Files\Sysmon\wmonitor.exe`.
   - Adds the directory to the System `PATH`.
   - Writes credentials into `%LOCALAPPDATA%\Sysmon\config.env`.
   - Locks `config.env` using Windows ACLs (`icacls`) so only `SYSTEM` and the installer account can read it.
   - Registers and starts the `wmonitor` Windows Service with startup type *Automatic*.

---

### Windows Step 3: Installation as a Standalone Local Monitor

To monitor the local Windows machine with a local SQLite database and local dashboard:

```powershell
# In Administrator PowerShell:
.\install.ps1 -Mode hub -Db sqlite -ApiKey "local-admin-key"
```

Once installed, open your browser and navigate to:
```
http://localhost:8080
```

---

### Windows Step 4: Running Interactively

You can test W-Monitor directly in your terminal without installing a service:

**Local standalone monitor (SQLite):**
```powershell
.\wmonitor.exe
```

**Run as an agent pushing to a Hub:**
```powershell
.\wmonitor.exe -agent "https://hub.example.com" -api-key "your-api-key"
```

**Run for a specific duration (e.g. 1 hour) and auto-export on exit:**
```powershell
.\wmonitor.exe -run-for 1h -export-filter daily
```

**Override the external network interface:**
```powershell
# Treat "Ethernet 2" as the external/public interface
.\wmonitor.exe -external-iface "Ethernet 2"
```

---

### Windows Step 5: Managing the Windows Service

When installed as a Windows service, manage W-Monitor using either the CLI flags or standard Windows service commands:

**Using W-Monitor CLI (Admin PowerShell):**
```powershell
# Start the service
wmonitor -start

# Stop the service
wmonitor -stop

# Uninstall the service
wmonitor -uninstall
```

**Using PowerShell Service cmdlets:**
```powershell
Get-Service wmonitor
Start-Service wmonitor
Stop-Service wmonitor
Restart-Service wmonitor
```

---

### Windows Step 6: Generating Reports & Data Exports

Generate summary reports directly from the stored metrics without starting the server:

**1. Client-Ready HTML Assessment Report (Printable to PDF):**
```powershell
# Generates a 30-day assessment report with cloud sizing recommendations
wmonitor -assessment-report assessment_report.html -since 720h
```

**2. CSV Spreadsheet Export:**
```powershell
wmonitor -export-csv migration_metrics.csv -since 720h
```

**3. Plain-Text Terminal Report:**
```powershell
wmonitor -export-txt summary.txt -since 168h
```

---

### Windows File Locations & Security

| Component | Default Path | Description |
|-----------|--------------|-------------|
| **Binary** | `C:\Program Files\Sysmon\wmonitor.exe` | Executable path in system PATH |
| **Config File** | `%LOCALAPPDATA%\Sysmon\config.env` | Locked configuration (`icacls` restricted) |
| **Local SQLite DB** | `%LOCALAPPDATA%\Sysmon\wmonitor.db` | Stored metrics database (in Standalone/Hub mode) |
| **Windows Service** | `wmonitor` | Registered in Windows Service Control Manager |

---

## Linux Guide

### Linux Step 1: Downloading & Obtaining the Binary

You will typically receive a release package containing:
- `wmonitor_linux` (The compiled 64-bit Linux binary)
- `install.sh` (Automated systemd installation script)

If you are building from source:
```bash
# On a machine with Go installed:
GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o wmonitor_linux .
chmod +x wmonitor_linux install.sh
```

---

### Linux Step 2: Installation as a Client Agent

To install on a Linux client machine pushing to your central Hub:

1. Make sure `install.sh` is executable:
   ```bash
   chmod +x install.sh
   ```
2. Run the script with `sudo`:
   ```bash
   sudo ./install.sh --mode agent --hub-url "https://<hub-address>:8080" --api-key "<your-shared-api-key>"
   ```
3. **What happens automatically:**
   - Validates root privileges.
   - Copies `wmonitor_linux` to `/usr/local/bin/sysmon`.
   - Creates `~/.local/share/sysmon/config.env` and sets permissions to `chmod 600` (readable only by owner).
   - Installs and enables the systemd service.
   - Starts the agent service immediately.

---

### Linux Step 3: Installation as a Standalone Local Monitor

To monitor the Linux server locally with an embedded SQLite database and dashboard:

```bash
sudo ./install.sh --mode hub --db sqlite --api-key "local-admin-key"
```

Access the dashboard at `http://<server-ip>:8080`.

---

### Linux Step 4: Running Interactively

Test or run W-Monitor in the foreground:

**Local monitor:**
```bash
./wmonitor_linux
```

**Agent mode:**
```bash
./wmonitor_linux -agent "https://hub.example.com:8080" -api-key "your-api-key"
```

**Custom run duration with automated export on exit:**
```bash
./wmonitor_linux -run-for 2h -export-filter daily
```

**Specify custom HTTP port:**
```bash
./wmonitor_linux -port 9090
```

---

### Linux Step 5: Managing the Systemd Service

Manage the running service with standard systemd commands:

```bash
# Check service status
sudo systemctl status sysmon

# View real-time logs
sudo journalctl -u sysmon -f

# Stop or restart
sudo systemctl stop sysmon
sudo systemctl restart sysmon

# Uninstall service
sudo sysmon -uninstall
```

---

### Linux Step 6: Generating Reports & Data Exports

**1. HTML Assessment Report:**
```bash
sysmon -assessment-report assessment.html -since 720h
```

**2. CSV Export:**
```bash
sysmon -export-csv metrics_export.csv -since 720h
```

**3. Text Summary Report:**
```bash
sysmon -export-txt report.txt -since 168h
```

---

### Linux File Locations & Security

| Component | Default Path | Description |
|-----------|--------------|-------------|
| **Binary** | `/usr/local/bin/sysmon` | Standard system binary path |
| **Config File** | `~/.local/share/sysmon/config.env` | Protected config file (`chmod 600`) |
| **Local SQLite DB** | `~/.local/share/sysmon/wmonitor.db` | Stored metrics database (Hub mode) |
| **Service Name** | `sysmon.service` | Managed by `systemd` |

---

## Using the Web Dashboard

When running in Standalone or Hub mode, open `http://localhost:8080` in your web browser:

1. **Server Filter Dropdown (Top Bar):**
   - Click the server dropdown to toggle between **"All Servers"** aggregate view or inspect a specific client server node.
2. **Time Range Selectors:**
   - Toggle between **24h** (high-resolution raw 10-second data), **7d**, and **30d** (hourly downsampled averages).
3. **Real-Time KPI Cards:**
   - **CPU Usage:** Average & peak core consumption.
   - **Memory:** Average & peak RAM utilization.
   - **Disk Free:** Minimum free storage observed.
   - **Network I/O:** Total bandwidth transferred and peak throughput (split by external/internal interfaces).
   - **Disk IOPS:** Real-time and peak read/write ops per second.
   - **Concurrent Users:** Active sessions tracked.
4. **Top Processes Table:**
   - Displays top processes ranked by CPU and RAM consumption.
5. **One-Click CSV Export:**
   - Click the **"Export CSV"** button in the header to download data for the active server and time range.

---

## Advanced Metrics & Sizing Recommendations

W-Monitor computes suggested cloud target specs based on observed peak workload + safety headroom:

- **Suggested Minimum Specs:** Measured peak resource utilization + 20% margin.
- **Suggested Recommended Specs:** Peak workload doubled (2.0x) to accommodate burst traffic and 12-month data growth.
- **Network Split:** External vs Internal traffic is automatically categorized by identifying the interface with the default gateway route, allowing precise cloud egress cost forecasting.
