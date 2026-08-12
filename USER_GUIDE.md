# W-Monitor User Guide

Welcome to **W-Monitor**, a single-binary system monitoring tool. W-Monitor collects CPU, memory, disk, and network metrics locally and provides a web dashboard and detailed CSV/TXT reports.

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
