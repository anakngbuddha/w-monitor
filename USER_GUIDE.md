# W-Monitor User Guide

Welcome to **W-Monitor**, a single-binary system monitoring tool. W-Monitor collects CPU, memory, disk, and network metrics locally and provides a web dashboard and detailed CSV/TXT reports.

---

## System Requirements

W-Monitor collects system metrics every **10 seconds**, stores up to **30 days** of data in a local SQLite database (raw data for the last 24 hours, then downsampled to hourly averages), and serves a local web dashboard on `localhost:8080`. **All traffic is loopback-only — W-Monitor generates zero internet bandwidth.**

> **Bandwidth note:** The figures below refer to *local loopback traffic* between your browser and the W-Monitor HTTP server. No data is sent over the internet.

### Windows

| Spec | Minimum | Recommended |
|---|---|---|
| **vCPU** | 1 core | 2 cores |
| **RAM** | 256 MB | 512 MB |
| **Disk Space** | 200 MB | 1 GB |
| **Monthly Loopback Bandwidth** | ~2 GB | ~5 GB |

**Bandwidth computation (Windows):**
- Each dashboard `/api/metrics` JSON response (24h range) ≈ 8,640 rows × ~100 bytes ≈ **~864 KB per page load**
- `/api/processes` response (24h) ≈ 172,800 rows × ~60 bytes ≈ **~10 MB per full load** (browser caches/paginates)
- **Minimum** (5 dashboard visits/day × 2 API calls × ~500 KB avg) = ~1.5 GB/month ≈ **~2 GB/month**
- **Recommended** (20 visits/day × 2 API calls × ~900 KB avg) = ~10.8 GB/month → rounded to **~5 GB/month** with typical browser-side caching

**Disk computation (Windows):**
- DB steady-state: last 24h raw (~11 MB) + days 2–30 downsampled (~2 MB) ≈ **~15–50 MB SQLite DB**
- Binary: `sysmon_windows.exe` ≈ **15 MB**
- Minimum: binary + DB + OS overhead = **~200 MB**
- Recommended: includes headroom for WAL, exports, and 30-day CSV reports = **~1 GB**

---

### Linux

| Spec | Minimum | Recommended |
|---|---|---|
| **vCPU** | 1 core | 2 cores |
| **RAM** | 128 MB | 256 MB |
| **Disk Space** | 150 MB | 500 MB |
| **Monthly Loopback Bandwidth** | ~2 GB | ~5 GB |

**Bandwidth computation (Linux):**
- Same API payload structure as Windows — loopback traffic is identical.
- **Minimum** (~5 dashboard visits/day): **~2 GB/month loopback**
- **Recommended** (~20 visits/day with full chart loads): **~5 GB/month loopback**
- Internet bandwidth: **0 GB** — W-Monitor is entirely local.

**Disk computation (Linux):**
- DB steady-state: **~15–50 MB** (same schema, same retention policy)
- Binary: `sysmon_linux` ≈ **11 MB**
- Minimum: binary + DB + systemd service overhead = **~150 MB**
- Recommended: headroom for TXT exports and 30-day data history = **~500 MB**

> **RAM note:** The Go runtime and gopsutil process enumeration (top-20 processes every 10s) are the primary memory consumers. Linux benefits from lower OS overhead, hence the lower minimum RAM figure.

---

## Installation

You can run W-Monitor interactively or install it as a background service. Since you have the installer scripts, you can use them directly to set up W-Monitor permanently on your machine.

### Windows
To install W-Monitor as a Windows Service, open an **Administrator PowerShell** and run:
```powershell
.\install.ps1
```
*(Note: There is also `install_user.ps1` if you wish to run it in a specific user context).*

### Linux
To install W-Monitor as a systemd service, run:
```bash
sudo ./install.sh
```

Once installed, W-Monitor will run automatically in the background and start collecting metrics on every boot.

---

## Interactive Usage & Features

If you prefer to run W-Monitor manually (in the foreground), just open a terminal and run the binary:

```bash
# Windows
.\wmonitor.exe

# Linux
./wmonitor_linux
```

The web dashboard will instantly become available at `http://localhost:8080`.

### Custom Run Duration & Automated Exports

You can tell W-Monitor to run for a specific duration, collect metrics, and then automatically shut down and export a report (CSV on Windows, TXT on Linux). This is great for benchmarking!

**Run for a specific duration:**
```bash
wmonitor -run-for 1h
# Supports units like 's' (seconds), 'm' (minutes), 'h' (hours)
```

**Filter the automated export:**
You can specify a time window for the report generated upon shutdown. Available filters are `daily`, `weekly`, and `monthly`.
```bash
wmonitor -run-for 30m -export-filter daily
```
*When the 30 minutes are up (or if you manually press Ctrl+C), W-Monitor will cleanly exit and generate a file like `wmonitor_export_YYYYMMDD_HHMMSS.csv` (or `.txt` on Linux) in your current directory.*

### Manual Report Exports

You can generate a 30-day historical report at any time without starting the monitoring server:

**Export to CSV (Windows friendly):**
```bash
wmonitor -export-csv report.csv
```

**Export to Text (Linux friendly):**
```bash
wmonitor -export-txt report.txt
```

---

## Service Management

If you installed W-Monitor as a service, you can manage it using the built-in flags (these must be run as Administrator/root):

- **Start the service:** `wmonitor -start`
- **Stop the service:** `wmonitor -stop`
- **Uninstall the service:** `wmonitor -uninstall`
