# W-Monitor Technical Changelog

All notable changes, architectural updates, CLI modifications, and documentation updates across the W-Monitor project are recorded in this file.

---

## [2026-09-04] - Method B Universal Standard & Codebase Documenter Skill

### Summary
Established **Method B (Universal Generic Binaries)** as the primary multi-server deployment standard, configured installer scripts to default to the production Central Hub URL (`https://wmonitor-hub.onrender.com`), fully excised legacy `sysmon` references across deployment scripts and user guides, and introduced the `codebase-documenter` skill.

### Affected Components
- [install.ps1:L41-L95](file:///c:/Users/markmv/Desktop/Zeus/install.ps1#L41-L95):
  - Updated `$HubUrl` default parameter to `"https://wmonitor-hub.onrender.com"`.
  - Updated `$installDir` target directory to `$env:ProgramFiles\W-Monitor`.
  - Added automated cleanup for legacy `sysmon` Windows service before registering `wmonitor`.
- [install.sh:L10-L75](file:///c:/Users/markmv/Desktop/Zeus/install.sh#L10-L75):
  - Updated `EXE_NAME` from `"sysmon"` to `"wmonitor"`.
  - Updated `HUB_URL` default variable to `"https://wmonitor-hub.onrender.com"`.
  - Added loop to automatically stop and disable legacy `sysmon` or `wmonitor` systemd services during upgrade.
  - Updated status and log inspection commands to `systemctl status wmonitor` and `journalctl -u wmonitor -f`.
- [.agents/skills/codebase-documenter/SKILL.md](file:///c:/Users/markmv/Desktop/Zeus/.agents/skills/codebase-documenter/SKILL.md):
  - Created an automated, structured skill for maintaining granular documentation, tracking AST diffs, updating user/admin guides, and re-indexing the graphify knowledge graph.
- [USER_GUIDE.md](file:///c:/Users/markmv/Desktop/Zeus/USER_GUIDE.md):
  - Fully overhauled manual covering the Method B fleet rollout, Organization API Key provisioning, Centralized Web Dashboard usage, Standalone offline testing, and cloud migration deliverables.

### Added / Modified Script Defaults
| Script / Component | Parameter / Variable | Previous Default | New Default | Rationale |
|---|---|---|---|---|
| `install.ps1` | `-HubUrl` | `""` | `"https://wmonitor-hub.onrender.com"` | Allows single-parameter agent deployment (`.\install.ps1 -ApiKey <Key>`) while keeping binaries generic |
| `install.sh` | `--hub-url` | `""` | `"https://wmonitor-hub.onrender.com"` | Allows single-parameter Linux deployment (`sudo ./install.sh --api-key <Key>`) |
| `install.sh` | `EXE_NAME` | `"sysmon"` | `"wmonitor"` | Resolves legacy naming drift |
| `install.ps1` | `$installDir` | `ProgramFiles\Sysmon` | `ProgramFiles\W-Monitor` | Standardizes installation directory name |

### Operational & Migration Notes
- Operators rolling out agents on client servers now only need to provide their generated Organization API Key:
  - Windows: `.\install.ps1 -ApiKey "<Key>"`
  - Linux: `sudo ./install.sh --api-key "<Key>"`
- Old machines running the legacy `sysmon` background process should run:
  ```powershell
  Stop-Process -Name "sysmon", "wmonitor*" -Force -ErrorAction SilentlyContinue
  Remove-Item "$env:APPDATA\Microsoft\Windows\Start Menu\Programs\Startup\Sysmon.vbs" -Force -ErrorAction SilentlyContinue
  ```
