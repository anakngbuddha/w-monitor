# W-Monitor Technical Changelog

All notable changes, architectural updates, CLI modifications, and documentation updates across the W-Monitor project are recorded in this file.

---

## [2026-09-04] - Phase 13: Credential & Provisioning Overhaul

### Summary
Replaced the static baked-key model with a **hub-issued, per-agent token system**. Agents now auto-enroll on first run using a short-lived enrollment code, receive a scoped `wma_*` ingest-only token, and persist it via OS-native credential storage (DPAPI on Windows, `0600` file on Linux). The dashboard switches from `localStorage` API keys to `HttpOnly` session cookies. All four previously identified security vulnerabilities are closed.

### Security Vulnerabilities Closed
| CVE-class | Location | Fix |
|-----------|----------|-----|
| File-drop auth bypass | `autoSeedHubKeys` CSV auto-import | Removed automatic CSV import from disk |
| Plaintext key exfiltration | `build_release.ps1 -ApiKey` | Replaced with enrollment code; plaintext never baked |
| Privilege escalation (ingest→read) | `server/auth.go` | Scope enforcement: `ingest` tokens get 403 on read routes |
| `localStorage` token theft | `dashboard/static/index.html` | Session cookie (`HttpOnly; Secure; SameSite=Strict`) |

### New Files
| File | Purpose |
|------|---------|
| [`server/enroll.go`](file:///c:/Users/markmv/Desktop/Zeus/server/enroll.go) | `POST /api/enroll`, `POST /api/admin/enroll-codes`, `GET+DELETE /api/admin/agents` |
| [`server/session.go`](file:///c:/Users/markmv/Desktop/Zeus/server/session.go) | `POST /api/session` — issues `HttpOnly` cookie from `wmr_` read token |
| [`agent/credstore.go`](file:///c:/Users/markmv/Desktop/Zeus/agent/credstore.go) | `StoredCredentials` struct and `ErrNoCredentials` sentinel |
| [`agent/credstore_windows.go`](file:///c:/Users/markmv/Desktop/Zeus/agent/credstore_windows.go) | DPAPI `CryptProtectData` (`CRYPTPROTECT_LOCAL_MACHINE`) at `%ProgramData%\wmonitor\token.dat` |
| [`agent/credstore_unix.go`](file:///c:/Users/markmv/Desktop/Zeus/agent/credstore_unix.go) | JSON at `/etc/wmonitor/token.json` or `~/.local/share/sysmon/token.json` with `0600` perms |
| [`agent/enroll.go`](file:///c:/Users/markmv/Desktop/Zeus/agent/enroll.go) | `Enroll()` — HTTP handshake + credential persistence |

### Modified Files
| File | Changes |
|------|---------|
| [`storage/apikeys.go`](file:///c:/Users/markmv/Desktop/Zeus/storage/apikeys.go) | Extended `APIKeyRecord` with `Kind`, `Scope`, `KeyPrefix`, `ExpiresAt`, `MaxUses`, `Uses`, `ServerID`, `IssuedBy`; added `GenerateToken()`, `GenerateEnrollCode()` (Crockford Base32), `ConsumeEnrollCode()` (atomic), `NewTenantID()`, `ExtractKeyPrefix()`, `NormalizeEnrollCode()` |
| [`storage/db.go`](file:///c:/Users/markmv/Desktop/Zeus/storage/db.go) | `MigrateTenantID()` for historical data migration |
| [`storage/postgres.go`](file:///c:/Users/markmv/Desktop/Zeus/storage/postgres.go) | `MigrateTenantID()` + `PurgeOld()` (Postgres retention) |
| [`server/auth.go`](file:///c:/Users/markmv/Desktop/Zeus/server/auth.go) | `authTenantScope()` scope enforcement; `authCache.invalidate()` on revoke; cookie auth fallback; `AdminStore` interface |
| [`server/server.go`](file:///c:/Users/markmv/Desktop/Zeus/server/server.go) | Routes: `/api/enroll`, `/api/admin/enroll-codes`, `/api/admin/agents`, `/api/session`; `EnableHubMode()` |
| [`retention/retention.go`](file:///c:/Users/markmv/Desktop/Zeus/retention/retention.go) | `NewWithPruner()` — Postgres retention via `Pruner` interface; fixes Render storage exhaustion |
| [`build_release.ps1`](file:///c:/Users/markmv/Desktop/Zeus/build_release.ps1) | `-ApiKey` → `-EnrollCode`; auto-request from Hub via `WMONITOR_ADMIN_TOKEN`; outputs to `dist/<ClientName>/`; build log records fingerprint only |
| [`install.ps1`](file:///c:/Users/markmv/Desktop/Zeus/install.ps1) | `-ApiKey` now optional for agent mode (enrollment-code binaries self-provision) |
| [`main.go`](file:///c:/Users/markmv/Desktop/Zeus/main.go) | New CLI: `-new-enroll-code`, `-list-agents`, `-revoke-agent`, `-new-admin-token`; `defaultEnrollCode` ldflag replaces `defaultAPIKey` |

### New Token Kinds & Prefixes
| Prefix | Kind | Scope | Usage |
|--------|------|-------|-------|
| `wma_` | `agent` | `ingest` | Machine-bound agent token (hub-issued, stored via DPAPI/0600) |
| `wmr_` | `read` | `read` | Dashboard read token (issued by operator, used for session login) |
| `wmk_` | `admin` | `admin` | Admin token (operator only; for build automation + CLI management) |
| `wme_` | `enroll` | `ingest` | Short-lived enrollment code — consumed once to mint `wma_` |

### New CLI Commands (hub side)
```
wmonitor -new-enroll-code "AcmeCorp" [-ttl 72h] [-max-uses 25]
wmonitor -new-admin-token
wmonitor -list-clients
wmonitor -list-agents "AcmeCorp"
wmonitor -revoke-agent <server-id>
wmonitor -revoke-client "AcmeCorp"
wmonitor -add-client "AcmeCorp"         # issues wmr_ read token
```

### Breaking Changes
- **Existing agents** with baked-in `defaultAPIKey` continue to work during this release via `kind=legacy, scope=all`. The next major release will remove legacy key acceptance.
- **`build_release.ps1 -ApiKey`** emits a deprecation warning. Switch to `-EnrollCode`.
- **`install.ps1 -ApiKey`** is now optional for agent mode. Remove it from deployment scripts that use enrollment-code binaries.

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
