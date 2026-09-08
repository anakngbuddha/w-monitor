# W-Monitor Technical Changelog

All notable changes, architectural updates, CLI modifications, and documentation updates across the W-Monitor project are recorded in this file.

---

## [2026-09-08] - P1.06: Transport, destination, and secret-file boundaries

### Summary
Agents send enrollment codes and tokens only over HTTPS (loopback HTTP remains for tests). Spoofed `X-Forwarded-Proto` is ignored unless the peer is listed in `WMONITOR_TRUSTED_PROXIES`. Authenticated HTTP clients refuse cross-origin and downgrade redirects and verify TLS (min TLS 1.2). Stored tokens and the on-disk spool are bound to the hub origin (and tenant/server when known); a changed destination does not send the previous token or backlog. Remote PostgreSQL DSNs require `sslmode=require|verify-ca|verify-full`. Credential files are written atomically, refuse symlinks, check Unix ownership/0600, and on Windows use a protected DACL (SYSTEM + Administrators + current user). `-print-config` no longer prints secret prefixes; service install arguments omit `-api-key`/`-dsn`/`-enroll-code`. V07 remains contained. Phase 1 is not complete.

### Affected Components
- `agent/transport.go`: `RequireHTTPSHub`, `SameHubOrigin`, redirect guard, TLS-verifying hub client.
- `agent/enroll.go`: HTTPS required before the enroll POST.
- `agent/spool.go`: `BindDestination` quarantines backlog on origin/tenant/server mismatch.
- `agent/agent.go`: binds spool origin; `BindIdentity`.
- `agent/credstore.go` / `credstore_unix.go` / `credstore_windows.go`: atomic write, symlink refuse, Unix owner check, Windows DACL.
- `server/https.go`: trusted-proxy gate for `X-Forwarded-Proto`.
- `main.go`: remote PG TLS check, secret-free service args, `config.env` under `%ProgramData%\wmonitor` or `/etc/wmonitor`.
- `install.ps1`: secrets only in locked `config.env`, not service arguments.

### Added / Modified CLI Flags & Environment Variables
| Flag / Env Var | Type | Default | Description |
|---|---|---|---|
| `WMONITOR_TRUSTED_PROXIES` | string | `""` | Comma-separated proxy IPs or CIDRs allowed to set `X-Forwarded-Proto`. Empty never trusts the header. |
| `-print-config` | bool | `false` | Prints `(set)` / `(not set)` for secrets; never a prefix or suffix of the value. |

### API & Protocol Changes
- Agent enroll and ingest clients follow same-origin HTTPS redirects only.
- `POST /api/enroll` still requires HTTPS as seen by the hub (`requestIsHTTPS`), which ignores spoofed forwarded proto from untrusted peers.

### Operational & Migration Notes
- Pointing an enrolled agent at a different hub URL refuses to send the stored token. Re-enroll at the new hub. Existing spool segments for the old origin are quarantined under `spool/quarantine/` and are not drained.
- Remote Postgres without a verifying `sslmode` fails at startup. Loopback may use `sslmode=disable`.
- Windows install writes `%ProgramData%\wmonitor\config.env` (SYSTEM + Administrators). Do not pass `-api-key` on the service command line.
- Existing `wma_`/`wmr_`/`wmk_` hashes stored under the old P1.03 case-normalization may still need re-issue.
- Do not re-enable `downsampleMetrics`. Next ticket: P1.07.
- Native OS ACL test with a second non-admin Windows user: **NOT RUN** (DACL contents are asserted on this runner). Live PostgreSQL: **NOT RUN**. Browser: **NOT RUN**.

---

## [2026-09-08] - P1.05: Tenant scope across storage, compute, and reports

### Summary
Customer-facing metric/process/server queries and CSV/HTML/text exports require an explicit tenant ID. Empty tenant is never a global read. Standalone (non-hub) mode uses `t_local`. Health and alert evaluation may read all tenants through `AllTenantsQuerier`, then isolate by `tenant_id` (alert keys are tenant+rule+server). SQL applies tenant/server/time bounds, a row limit, and query context cancellation. PostgreSQL RLS was **not** added untested. V07 remains contained. Phase 1 is not complete.

### Affected Components
- [`storage/query.go`](file:///c:/Users/markmv/Desktop/Zeus/storage/query.go): `LocalTenantID`, `ErrTenantRequired`, `MetricQuery`, `AllTenantsQuerier`.
- [`storage/db.go`](file:///c:/Users/markmv/Desktop/Zeus/storage/db.go) / [`storage/postgres.go`](file:///c:/Users/markmv/Desktop/Zeus/storage/postgres.go): scoped `QueryMetrics`/`QueryProcesses`/`QueryServers`; `QueryMetricsQ`; `QueryMetricsAllTenants`.
- [`server/auth.go`](file:///c:/Users/markmv/Desktop/Zeus/server/auth.go): standalone principal is `t_local`.
- [`server/alerts.go`](file:///c:/Users/markmv/Desktop/Zeus/server/alerts.go): empty tenant no longer returns all alerts.
- [`alerting/evaluator.go`](file:///c:/Users/markmv/Desktop/Zeus/alerting/evaluator.go): per-tenant alert state.
- [`export/export.go`](file:///c:/Users/markmv/Desktop/Zeus/export/export.go) / [`export/report.go`](file:///c:/Users/markmv/Desktop/Zeus/export/report.go): tenant argument required.
- [`main.go`](file:///c:/Users/markmv/Desktop/Zeus/main.go): `-tenant` for CLI exports.
- [`collector/collector.go`](file:///c:/Users/markmv/Desktop/Zeus/collector/collector.go): local samples tagged `t_local`.

### Added / Modified CLI Flags & Environment Variables
| Flag / Env Var | Type | Default | Description |
|---|---|---|---|
| `-tenant` | string | `t_local` | Tenant scope for `-export-csv`, `-export-txt`, `-assessment-report`. Never a wildcard. |

### API & Protocol Changes
- `GET /api/metrics`, `/api/processes`, `/api/servers`, `/api/export/csv`, `/api/alerts`: require a resolved tenant (hub key or standalone `t_local`). Empty tenant is 400, not all customers.
- Privileged health freshness uses `QueryMetricsAllTenants` with a 64-row cap; it is not an export.

### Operational & Migration Notes
- Hub CLI exports must pass `-tenant t_<hex>`. Omitting it reads only `t_local` (usually empty on a hub DB).
- Historical rows with empty `tenant_id` are not rewritten; new inserts coerce empty to `t_local`.
- PostgreSQL RLS: **NOT RUN** (no live PG fixture). Do not claim isolation from RLS.
- Next ticket: P1.06. Do not re-enable `downsampleMetrics`.

---

## [2026-09-08] - P1.04: Atomic enrollment, rotation, and auth-cache lifetime

### Summary
First enrollment of a `(tenant, server_id)` consumes the handshake code and issues one agent token in a single SQLite/Postgres transaction. A lost HTTP reply inside five minutes returns the same token without consuming another use. Replacement of an existing machine requires the current ingest token (`current_token`) or an operator `POST /api/admin/agents/rotate`. Concurrent enrollments cannot leave two active tokens (partial unique index). Failed inserts roll back the use increment. Positive auth cache TTL is min(60s, credential expiry); revoke/rotate invalidates locally and bumps `auth_epoch` so replicas drop warmed entries within 5 seconds. V07 remains contained. Phase 1 is not complete.

### Affected Components
- [`storage/enrollment.go`](file:///c:/Users/markmv/Desktop/Zeus/storage/enrollment.go): `CompleteEnrollment`, `ReplaceAgent`, handshake table, `auth_epoch`, one-active-agent index.
- [`storage/enrollment_pg.go`](file:///c:/Users/markmv/Desktop/Zeus/storage/enrollment_pg.go): same operations on Postgres.
- [`storage/apikeys.go`](file:///c:/Users/markmv/Desktop/Zeus/storage/apikeys.go): revoke bumps epoch; `TouchAPIKey` deletes handshake plaintext after first use.
- [`server/enroll.go`](file:///c:/Users/markmv/Desktop/Zeus/server/enroll.go): transactional enroll; `POST /api/admin/agents/rotate`; DELETE agents invalidates cache hashes.
- [`server/auth.go`](file:///c:/Users/markmv/Desktop/Zeus/server/auth.go): cache deadline bounded by expiry; epoch-aware get; `AuthRevocationMaxDelay` = 5s.
- [`agent/enroll.go`](file:///c:/Users/markmv/Desktop/Zeus/agent/enroll.go): optional `current_token` on `POST /api/enroll`.

### Added / Modified CLI Flags & Environment Variables
None. Handshake recovery TTL is fixed at 5 minutes (`storage.HandshakeTTL`). Replica cache lag is fixed at 5 seconds (`storage.AuthRevocationMaxDelay` / `server.AuthRevocationMaxDelay`).

### API & Protocol Changes
- `POST /api/enroll`: first identity issues a token (201). Retry with the same `enroll_code` + `server_id` inside the handshake window returns the **same** token (lost-reply recovery). After the window or first ingest, a second enroll without `current_token` returns **409**. Rotation with `current_token` matching the active agent issues a new token and revokes the old one.
- `POST /api/admin/agents/rotate`: platform admin only. Body `{"server_id","tenant_id"}`. Issues a replacement `wma_` token without consuming an enroll code. Old token is revoked immediately on that Hub and within 5s on replicas.
- `DELETE /api/admin/agents`: still revokes; now also invalidates cached hashes and bumps `auth_epoch`.

### Operational & Migration Notes
- One active agent per `(tenant_id, server_id)` is enforced in SQL (`idx_api_keys_one_active_agent`). A lost enroll response is recoverable for 5 minutes or until the new token is first used for ingest.
- After that, re-issue requires the current machine token or operator rotate. Possession of only the shared enroll code cannot take over an enrolled server.
- Handshake rows store the plaintext token briefly for recovery. They are deleted on `TouchAPIKey` (first successful authenticated use) and ignored after `expires_at`.
- Existing `wma_`/`wmr_`/`wmk_` hashes stored under the old case-normalization may still need re-issue (P1.03 hashing change).
- Do not re-enable `downsampleMetrics`. Next ticket: P1.05 (tenant scope). Do not start P1.06–P1.10 until P1.05 acceptance exists.

---

## [2026-09-08] - P1.03: Separate identities and fail-closed authorization

### Summary
Hub authentication now yields a typed principal (tenant, bound agent/server ID, credential hash, kind, permissions, expiry). `legacy`/`all` cannot satisfy platform admin. Enrollment codes are handshake-only (`kind=enroll`, `scope=enroll`) and are rejected by `ResolveAPIKey`. Read tokens can list agents but cannot revoke. Agent ingest identity is taken from the principal: a token bound to server A cannot write B. `autoSeedHubKeys` no longer imports `clients_registry.csv` and never un-revokes an existing hash. Token hashing is exact bytes; enrollment codes use `HashEnrollCode`. Duplicate client display names never resolve identity. SQLite one-time opaque-tenant migration (`-migrate-opaque-tenants`) copies a backup then remaps plaintext `tenant_id` values in one transaction without clearing `revoked_at`. V07 remains contained, not closed. Phase 1 is not complete.

### Affected Components
- [`server/auth.go:L44-L51`](file:///c:/Users/markmv/Desktop/Zeus/server/auth.go#L44): `Principal` type.
- [`server/auth.go:L111-L129`](file:///c:/Users/markmv/Desktop/Zeus/server/auth.go#L111): `credentialPermits` (legacy/all ≠ admin; enroll never authenticates general routes).
- [`server/auth.go:L157`](file:///c:/Users/markmv/Desktop/Zeus/server/auth.go#L157): `authPrincipal`.
- [`server/server.go:L644-L695`](file:///c:/Users/markmv/Desktop/Zeus/server/server.go#L644): bound `ServerID` on ingest.
- [`server/enroll.go:L249`](file:///c:/Users/markmv/Desktop/Zeus/server/enroll.go#L249): enroll codes stored with `ScopeEnroll`; DELETE agents requires admin kind.
- [`storage/apikeys.go:L78-L85`](file:///c:/Users/markmv/Desktop/Zeus/storage/apikeys.go#L78): exact `HashAPIKey`; `HashEnrollCode`.
- [`storage/apikeys.go:L432`](file:///c:/Users/markmv/Desktop/Zeus/storage/apikeys.go#L432) / [`L736`](file:///c:/Users/markmv/Desktop/Zeus/storage/apikeys.go#L736): upsert does not reset `revoked_at`.
- [`storage/opaque_tenant.go:L30`](file:///c:/Users/markmv/Desktop/Zeus/storage/opaque_tenant.go#L30): `MigrateOpaqueTenants`.
- [`main.go:L1055`](file:///c:/Users/markmv/Desktop/Zeus/main.go#L1055): `autoSeedHubKeys` CSV import removed; insert-if-absent only.
- [`main.go:L1109`](file:///c:/Users/markmv/Desktop/Zeus/main.go#L1109): `-migrate-opaque-tenants` CLI.

### Added / Modified CLI Flags & Environment Variables
| Flag / Env Var | Type | Default | Description |
|---|---|---|---|
| `-migrate-opaque-tenants` | bool | `false` | One-time SQLite remap of plaintext `tenant_id` values to `t_<hex>`. Requires backup dir. Does not un-revoke. |
| `-opaque-tenant-backup-dir` | string | `""` | Directory for the pre-migration SQLite copy (`0600`). Required with `-migrate-opaque-tenants`. |
| `-import-clients` | string | `""` | Explicit CSV import only. Skips existing/revoked hashes. Assigns opaque tenant IDs (`KindLegacy`/`ScopeRead`). Never auto-run at hub start. |

`WMONITOR_API_KEY` / `WMONITOR_ADMIN_TOKEN` first-boot registration still occurs only when the hash is absent. A revoked configured hash stays revoked.

### API & Protocol Changes
- `POST /api/ingest`: agent tokens must match bound `server_id` (empty payload ID is filled from the principal; mismatch → 403).
- `POST /api/admin/enroll-codes`: optional `tenant_id`. Duplicate `client_name` across tenants → 409 unless `tenant_id` is given.
- `POST /api/admin/clients`: existing or ambiguous `client_name` → 409.
- `DELETE /api/admin/agents`: requires platform admin (`kind=admin` and `scope=admin`). Read tokens get 403 with no mutation. Platform admin may pass `tenant_id`.
- Enrollment codes presented as `X-API-Key` fail with 401 on every protected route. Handshake remains `POST /api/enroll` with `enroll_code` in the JSON body.

### Operational & Migration Notes
- Stop the Hub before `-migrate-opaque-tenants`. Restore the backup file to roll back. Do not downgrade into a tree that treats raw API keys as tenant IDs or that auto-imports CSV.
- Existing `wma_`/`wmr_`/`wmk_` hashes were previously case-normalized; exact hashing may require re-enrollment/re-issue of those tokens. Enrollment `WM-` codes still normalize via `HashEnrollCode`.
- PostgreSQL opaque-tenant migration is **not implemented** in this ticket (CLI refuses). Snapshot PG separately.
- Next ticket: P1.04 (atomic enrollment, rotation, auth-cache lifetime). Do not start P1.05–P1.10. Do not re-enable `downsampleMetrics`.

---

## [2026-09-07] - P1.02: Containment controls and safe export rendering

### Summary
Destructive SQLite downsampling and automatic 30-day purge are **disabled** until P2.03 (V07 containment, not closure). `/api/health` reports that state. A disk budget (`WMONITOR_DISK_BUDGET_BYTES`, default 10 GiB) returns `ErrDiskPressure` without deleting tenant/server rows. HTML assessment reports use `html/template` so hostnames/server IDs cannot inject markup (V08). Human CSV export prefixes formula-like names; `WriteCSVLossless` remains untransformed (V09). CHANGELOG Phase 13 no longer claims all security issues closed (L18). No credentials are rotated automatically.

### Affected Components
- [`retention/retention.go:L94-L116`](file:///c:/Users/markmv/Desktop/Zeus/retention/retention.go#L94): `Job.Run` skips downsample/purge; enforces disk budget.
- [`export/report.go:L72`](file:///c:/Users/markmv/Desktop/Zeus/export/report.go#L72): `html/template` rendering of untrusted names.
- [`export/export.go:L355-L375`](file:///c:/Users/markmv/Desktop/Zeus/export/export.go#L355): `SpreadsheetSafeCell`, spreadsheet-safe `WriteCSV`, lossless `WriteCSVLossless`.
- [`server/server.go:L108-L111`](file:///c:/Users/markmv/Desktop/Zeus/server/server.go#L108): retention status on `/api/health`.
- [`main.go`](file:///c:/Users/markmv/Desktop/Zeus/main.go): wires data path, disk budget env, health provider.

### Added / Modified CLI Flags & Environment Variables
| Flag / Env Var | Type | Default | Description |
|---|---|---|---|
| `WMONITOR_DISK_BUDGET_BYTES` | int64 | `10737418240` | SQLite file size cap. Over budget → explicit disk-pressure error; no row deletion. `0` disables the check. |

### API & Protocol Changes
- `GET /api/health` JSON includes `retention` (`downsampling_enabled`, `purge_enabled`, `reason`, `disk_pressure`, byte counts).
- `GET /api/export/csv` uses spreadsheet-safe CSV; write/flush errors return HTTP 500.

### Operational & Migration Notes
- **Not V07 closure.** Unsafe retention is contained. Do not re-enable `downsampleMetrics` without P2.03 typed rollups and backups.
- Verify backups before any future retention enablement. Rotate exposed secrets via an authorized operator process; the product does not auto-rotate.
- Next ticket: P1.03 (typed identities). Do not skip it.

---

## [2026-09-07] - P1.01: Isolate tests and establish a truthful baseline

### Summary
Credential, data, and spool tests no longer resolve or create OS-default production paths. Fixture roots are injected (`t.TempDir()` / `SetCredentialDir` / `SetDataDir` / `NewWithSpoolRoot`). A production-path guard refuses `/etc/wmonitor`, `%PROGRAMDATA%\wmonitor`, `%LOCALAPPDATA%\sysmon`, and `~/.local/share/sysmon`. CI now uses the patched Go 1.26.6 toolchain (module language remains `go 1.26.5`), pins scanners instead of `@latest`, records the module graph, and sets `WMONITOR_TEST_ISOLATION=1` so a forgotten fixture fails closed. `golang.org/x/text` is upgraded to `v0.39.0` (GO-2026-5970). V22 is addressed by these tests; V21/L17 are started, not closed.

### Affected Components
- [`internal/fsroot/fsroot.go:L129-L137`](file:///c:/Users/markmv/Desktop/Zeus/internal/fsroot/fsroot.go#L129): `RejectProductionPath` refuses live credential/data roots as test fixtures.
- [`internal/fsroot/fsroot.go:L223-L244`](file:///c:/Users/markmv/Desktop/Zeus/internal/fsroot/fsroot.go#L223): External canary file for byte-identity checks outside `t.TempDir()`.
- [`internal/testisolate/isolate.go`](file:///c:/Users/markmv/Desktop/Zeus/internal/testisolate/isolate.go): `TestMain` wrapper snapshots production files and plants a canary.
- [`agent/credstore.go:L43-L53`](file:///c:/Users/markmv/Desktop/Zeus/agent/credstore.go#L43): `SetCredentialDir` injects a non-production token directory.
- [`agent/credstore.go:L76-L96`](file:///c:/Users/markmv/Desktop/Zeus/agent/credstore.go#L76): `tokenFilePath` panics under isolation if the OS-default path would be used.
- [`agent/credstore_unix.go:L13-L21`](file:///c:/Users/markmv/Desktop/Zeus/agent/credstore_unix.go#L13): Default Unix path is computed without `MkdirAll`.
- [`agent/credstore_windows.go:L16-L21`](file:///c:/Users/markmv/Desktop/Zeus/agent/credstore_windows.go#L16): Default Windows path is computed without `MkdirAll`.
- [`agent/agent.go:L102-L110`](file:///c:/Users/markmv/Desktop/Zeus/agent/agent.go#L102): `NewWithSpoolRoot` refuses production spool roots.
- [`agent/spool.go:L46-L51`](file:///c:/Users/markmv/Desktop/Zeus/agent/spool.go#L46): `NewSpool` rejects production paths when isolation is on.
- [`storage/db.go:L76-L86`](file:///c:/Users/markmv/Desktop/Zeus/storage/db.go#L76): `SetDataDir` injects a fixture data directory.
- [`storage/db.go:L125-L147`](file:///c:/Users/markmv/Desktop/Zeus/storage/db.go#L125): `DataDir` no longer creates the OS-default tree under isolation.
- [`.github/workflows/ci.yml:L11-L16`](file:///c:/Users/markmv/Desktop/Zeus/.github/workflows/ci.yml#L11): `WMONITOR_TEST_ISOLATION=1`; pinned `staticcheck@v0.6.1` and `govulncheck@v1.1.4`.
- [`.github/workflows/ci.yml:L33`](file:///c:/Users/markmv/Desktop/Zeus/.github/workflows/ci.yml#L33) / [`.github/workflows/release.yml:L18`](file:///c:/Users/markmv/Desktop/Zeus/.github/workflows/release.yml#L18): `go-version: 1.26.6`.
- [`go.mod:L30`](file:///c:/Users/markmv/Desktop/Zeus/go.mod#L30): `golang.org/x/text v0.39.0`.

### Added / Modified CLI Flags & Environment Variables
| Flag / Env Var | Type | Default | Description |
|---|---|---|---|
| `WMONITOR_CREDENTIAL_DIR` | string | `""` | Directory for `token.json` / `token.dat`. Process `SetCredentialDir` override wins. Production paths are rejected when `WMONITOR_TEST_ISOLATION=1`. |
| `WMONITOR_DATA_DIR` | string | `""` | Data/spool/SQLite root. Process `SetDataDir` override wins. Same isolation rule. |
| `WMONITOR_TEST_ISOLATION` | string | unset (CI: `"1"`) | When `"1"`, resolving OS-default credential/data paths fails instead of creating/writing them. |

Precedence: process test override > environment variable > OS default. There is no CLI flag for these paths in this ticket.

### API & Protocol Changes
- None.

### Operational & Migration Notes
- Do **not** run `go test ./...` on an enrolled host without `WMONITOR_TEST_ISOLATION=1`. CI sets it. Existing production token files are snapshotted and must remain byte-identical.
- Path lookup no longer creates `/etc/wmonitor` or `%PROGRAMDATA%\wmonitor` as a side effect of `LoadCredentials`. Directories are created only on save.
- Module language version remains `go 1.26.5`. CI/release **builds** with Go 1.26.6. `govulncheck` on this runner: **6 stdlib findings on go1.26.5**, **none on go1.26.6**. GO-2026-5970 (`x/text` before v0.39.0) is not reported after the upgrade. V21 is not closed: binary/module gates remain a P5.07/P1.10 concern; reachability of remaining unused import advisories was not claimed.
- This workspace has no git binary; no implementation commit hash was recorded.
- Next ticket: P1.02 (retention containment and safe export rendering). Do not enable destructive downsampling.

---

## [2026-09-04] - Phase 13: Credential & Provisioning Overhaul

### Summary
Replaced the static baked-key model with a **hub-issued, per-agent token system**. Agents now auto-enroll on first run using a short-lived enrollment code, receive a scoped `wma_*` ingest-only token, and persist it via OS-native credential storage (DPAPI on Windows, `0600` file on Linux). The dashboard has a session cookie path; **do not treat the following table as verified closure** — the 2026-09-07 audit (V01–V12, L18) found several of these claims contradicted by the code. P1.01/P1.02 contain tests and exports; they do not close identity or session findings.


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
