# Legacy hidden per-user startup is intentionally contained. It previously
# killed unrelated sysmon/wmonitor processes and bypassed the service lifecycle.
$ErrorActionPreference = 'Stop'
throw 'Per-user hidden installation is disabled. Use the protected machine-service installer, or explicitly run a loopback-only foreground collector with an approved config. No processes, startup items or PATH entries were changed.'
