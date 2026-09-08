# w-monitor: Security, Architecture, Product, and Differentiation Audit

**Audit date:** September 7, 2026  
**Repository:** [anakngbuddha/w-monitor](https://github.com/anakngbuddha/w-monitor)  
**Pinned revision:** [`02ef77cdc3b1c1cd27ae618eeeff93c761c062a9`](https://github.com/anakngbuddha/w-monitor/commit/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9), committed September 4, 2026 (`updated dist`)  
**Decision:** **Not ready for public shared-tenant SaaS or an authoritative client sizing deliverable.** Worth developing as an assessment evidence and decision product, not another general-purpose monitoring stack.

### Executive assessment

The strongest assets are a small Go collector, outbound agent delivery, existing SQLite/PostgreSQL adapters, and portable assessment exports. These are useful foundations, but the current trust model and assessment calculations are not reliable enough to build a Huawei Cloud SaaS promise on top.

The highest-impact findings:

1. Legacy organization credentials inherit `scope=all`, which grants global administrative access.
2. Agent tokens record server identity, but ingestion does not enforce it. A compromised agent can impersonate sibling servers.
3. SQLite retention merges tenants and servers, deletes their originals, and creates unowned rows with zero hardware baselines.
4. The alert evaluator keys data/state by server ID without tenant ID, allowing cross-tenant alert interference.
5. HTML reports interpolate agent-controlled names without escaping; CSV exports do not neutralize spreadsheet formulas.
6. The shipped dashboard still uses URL credentials and localStorage despite the changelog claiming HttpOnly sessions.
7. Service installation and configuration paths are inconsistent; agent-mode dispatch happens before service-control dispatch.
8. Sizing loses peak evidence under retention and disagrees between the browser and Go reports.
9. Default daily quotas support approximately five fully reporting agents per tenant, not the scale implied by the guides.
10. Huawei Migration Center already offers discovery, dependencies, performance-based recommendations, TCO, and migration workflows. “Monitoring plus Huawei sizing” alone is not differentiation.

### Scope and limitations

Directory traversal identified **77 tracked files**, including **57 Go files, of which 23 are tests**. Complete first-party source, tests, scripts, configuration, and documentation were retrieved and reviewed file-by-file. Section 2 contains a disposition for every file.

The vendored `dashboard/static/chart.umd.min.js` was retrieved but returned truncated content. Its version, bundled dependency notices, initial merge safeguards, and integration were inspected; **this is not a complete manual audit of all minified Chart.js internals**. The shipped dashboard loads a CDN copy instead of this local file.

This is a **static source audit with isolated reproductions**, not a penetration test, compliance attestation, or guarantee that every vulnerability has been found. No repository changes, production requests, credential-validity probes, service installations, or destructive tests on actual systems were performed.

| Evidence label | Meaning |
|---|---|
| Confirmed in source | Relevant implementation and code path are present in the pinned revision. |
| Isolated reproduction | Extracted SQL/JavaScript behavior was exercised locally, not the complete Go application. |
| Conditional risk | Exploitability depends on deployment, permissions, caller behavior, or an unverified runtime condition. |
| Version match | A declared version falls in an advisory range; application reachability is not established. |
| Not established | Evidence is insufficient to assert the issue. |

Severity reflects the proposed shared-tenant SaaS context, not a fabricated CVSS score. Related findings overlap; do not sum them as independent exploit chains.

**Execution limits:** The execution sandbox has no internet access and no Go or `govulncheck` executable. `go build`, `go test -race`, `go vet`, module verification, and call-graph vulnerability scanning were **not run**. GitHub secret scanning could not run because Advanced Security is not enabled. Manual secret review and public advisory checks were performed. Git history, release binaries, deployed IAM/network/database settings, branch protection, and actual Windows ACLs were not comprehensively inspected.

Source citations use immutable file links and exact function/symbol names. Line numbers are intentionally not invented.

---

## 1. Vulnerabilities

### V01. Legacy tenant credentials can perform global administration

**Severity:** Critical. **Evidence:** Confirmed in source.  
**Sources:** [server/auth.go][auth], [server/enroll.go][enroll-server], [storage/apikeys.go][keys], [main.go][main].

`UpsertAPIKey` defaults missing kind/scope to `legacy`/`all`; migrations also assign existing credentials `scope='all'`. `checkScope` returns true for any required scope when granted scope is `all` or `admin`.

An old organization key can therefore pass admin checks for `GET /api/admin/clients`, `POST /api/admin/enroll-codes`, and `POST /api/admin/clients`. The first lists clients across tenants; enrollment-code creation resolves an arbitrary client name across the global key registry.

A legacy-key holder can list another client's name, mint an enrollment code for it, exchange it for an ingest token, and poison that tenant's data. This escalation concerns legacy/all credentials, not every modern `wma_` or `wmr_` token.

**Fix:** Separate platform administration from tenant permissions. `all` must never mean platform admin; migrate legacy keys to explicit read/ingest privileges and expire them. Remove default-grant behavior.

**Acceptance:** A route × method × kind × scope matrix rejects legacy/read/ingest credentials on global admin operations, including upgraded legacy schemas.

### V02. Auto-import/bootstrap can resurrect revoked keys and retain plaintext secrets

**Severity:** High; critical when a lower-trust file writer reaches V01. **Evidence:** Confirmed code; file-drop exploit requires directory write access.  
**Sources:** [main.go][main], [storage/apikeys.go][keys], [CHANGELOG.md][changelog].

`autoSeedHubKeys` still imports `clients_registry.csv` from the data directory, executable directory, and current working directory. `UpsertAPIKey` resets `revoked_at=0` on conflict. Restarting can revive keys still present in a CSV.

Configured API/admin tokens are also upserted whenever `ResolveAPIKey` returns an error. A revoked configured key is treated as needing registration; unrelated lookup failures are not distinguished from “not found.”

`importClientsFromCSV` sets `TenantID` to the raw API key. Hashing `KeyHash` does not remove plaintext credentials from the database: they remain in credential and historical telemetry tenant fields. Admin client-list responses expose tenant IDs, compounding V01.

**Fix:** Remove automatic imports; provide an explicit audited transactional migration to opaque tenant IDs and rotate old keys. Never automatically un-revoke records. Bootstrap administration through a controlled one-time workflow.

**Acceptance:** A revoked configured/imported credential stays revoked after restart. A working-directory CSV has no effect. The migrated DB contains no previous raw keys.

### V03. “Machine-bound” agent tokens are not bound at ingestion

**Severity:** High. **Evidence:** Confirmed in source.  
**Sources:** [auth.go][auth], [server.go][server], [apikeys.go][keys].

Enrollment stores `APIKeyRecord.ServerID`, but `authTenantScope` returns only tenant ID; the auth cache does not retain server ID. `handleIngest` overwrites `TenantID` but trusts payload `ServerID` and `Hostname`.

A compromised ingest token can invent servers or write samples/processes for any sibling in its tenant, contaminating “latest,” sizing, and alerts. Ordinary ingestion cannot directly select another database tenant because tenant override is correctly enforced; V06 describes a separate cross-tenant computation flaw.

**Fix:** Return a typed principal with tenant, agent/server ID, kind, privileges, expiry, and credential ID. Derive row identity from that principal; reject mismatches. Validate hostnames as inventory metadata.

**Acceptance:** An A-bound token cannot write B-labeled metric or process rows, including cached-auth and retry paths.

### V04. Enrollment codes are accepted as ingest credentials without consuming uses

**Severity:** High. **Evidence:** Confirmed in source.  
**Sources:** [auth.go][auth], [enroll.go][enroll-server], [apikeys.go][keys].

Enrollment records use `kind=enroll, scope=ingest`. General authentication checks scope, not kind. `ResolveAPIKey` accepts an unexpired/unexhausted code, but only `ConsumeEnrollCode` increments `uses`.

Presenting the enrollment code directly in `X-API-Key` to `/api/ingest` bypasses enrollment, its server-ID format check, and use accounting. The code remains usable until expiry/exhaustion/revocation, plus cache delay.

**Fix:** Make codes valid only at `/api/enroll`; reject `KindEnroll` in general authentication. Separate code/token resolution and hashing.

**Acceptance:** A valid unused enrollment code fails every protected route except the handshake and creates no telemetry directly.

### V05. Read-only credentials can revoke agents

**Severity:** High tenant availability impact. **Evidence:** Confirmed in source.  
**Source:** [server/enroll.go][enroll-server], `handleAdminAgents`.

The handler checks `ScopeRead` before switching on method. DELETE calls `RevokeAgent` without a stronger permission. A dashboard read token can stop its organization's agents.

**Fix:** Require an explicit tenant agent-management permission for DELETE; preserve read-only GET. Keep platform administration distinct from tenant operations.

**Acceptance:** Read token GET succeeds; DELETE fails without mutation. Tenant operators cannot revoke another tenant's identity.

### V06. Cross-tenant alert poisoning via duplicate server IDs

**Severity:** High. **Evidence:** Confirmed in source.  
**Sources:** [alerting/evaluator.go][evaluator], [alerting/alert.go][alert-model], [server/alerts.go][alert-api].

`EvaluateOnce` queries all tenants, then builds `latest` using only `row.ServerID`. State keys use `alertKey(rule.Name, serverID)` without tenant. HTTP tenant filtering happens after this shared computation.

An agent in tenant A can choose B's server ID and supply a newer healthy/unhealthy sample. It can suppress B's evaluation or affect shared pending/firing/resolution state. Alerts may be attributed to whichever tenant's row wins.

**Fix:** Composite identity `{TenantID, ServerID, RuleID}` throughout selection, state, notifications and history; enforce V03.

**Acceptance:** Same-ID servers in two tenants with opposite states remain independent, including future samples and evaluator restart.

### V07. SQLite retention destroys tenant/server ownership

**Severity:** Critical integrity/availability. **Evidence:** Confirmed source and isolated SQL reproduction.  
**Sources:** [retention/retention.go][retention], [storage/db.go][sqlite].

`downsampleMetrics` groups only by hour; `downsampleProcesses` by hour and process name. DELETE predicates omit tenant/server identity. Replacement INSERTs omit tenant, server, hostname and multiple capacity/counter fields.

Ordinary retention after 24 hours can merge different customers/servers, delete originals, and replace them with empty-tenant, empty-server rows and zero hardware baselines. Tenant reads lose history; global reports can blend it as an unnamed server. This is destructive ownership loss, not automatically a direct read of B's named records by A.

**Isolated result:** Two same-hour rows for `tenant-a`/`tenant-b` with CPU 10/90 became one row: tenant `""`, server `""`, CPU `50`, cores/RAM/disk `0`. Both tenant-filtered counts became zero. The repository SQL ran in Python SQLite against a minimal compatible schema, not the Go driver.

**Fix:** Stop unsafe destructive downsampling until corrected; preserve backups. Use separate rollups keyed by tenant/server/bucket/dimensions/schema version, retaining counts, covered duration, extrema, capacities and reset metadata.

**Acceptance:** Multi-tenant, same-hostname, same-process-name, late-arrival and repeated-retention fixtures preserve ownership and statistical invariants.

### V08. Stored XSS in HTML assessment exports

**Severity:** High; recipient interaction required. **Evidence:** Confirmed in source.  
**Sources:** [export/report.go][html-report], [server/server.go][server].

`writeHTMLReport` inserts `sv.ServerID` and `sv.Hostname` into `<td>` elements through `fmt.Sprintf` without escaping. Ingestion accepts those values. The vulnerable table is shown when more than one server is in the report.

An agent-controlled hostname containing executable HTML can run when the recipient opens the report. A report served from the Hub origin can affect same-origin data/actions. A downloaded `file:` report has different privileges; do not automatically equate local-file script execution with Hub takeover.

**Fix:** Use `html/template` with plain strings, never trusted-HTML casts for collected data. Prefer script-free reports, restrictive CSP and isolated-origin/download serving.

**Acceptance:** Multi-server reports render HTML metacharacters and an inert test event-handler payload as literal text, without creating executable DOM nodes.

### V09. CSV spreadsheet formula injection

**Severity:** Medium. **Evidence:** Confirmed unneutralized input; execution depends on spreadsheet client.  
**Source:** [export/export.go][exports], `WriteCSV`.

`encoding/csv` handles quoting but not spreadsheet formulas. Server IDs/hostnames beginning with `=`, `+`, `-`, `@` or relevant control-character prefixes can be interpreted as formulas on opening.

**Fix:** Add a documented spreadsheet-safe export mode and separate lossless machine-readable export. CSV escaping is not formula safety.

**Acceptance:** Test formula-like names in supported spreadsheet applications; document any prefix/value transformation.

### V10. Transport encryption is optional and proxy trust is unsafe

**Severity:** High when reachable without an enforced trusted TLS boundary. **Evidence:** Confirmed missing controls; deployment-dependent exposure.  
**Sources:** [server.go][server], [enroll.go][enroll-server], [session.go][session], [agent.go][agent], [agent/enroll.go][enroll-agent], [postgres.go][postgres].

- Hub uses `ListenAndServe` and binds `":" + port` on all interfaces while logging localhost.
- Ingest/read/admin routes do not require secure transport.
- Enrollment accepts any peer's `X-Forwarded-Proto: https` as proof of HTTPS.
- Agents accept HTTP destinations; enrollment sends its code before a server can reject insecure transport.
- HTTP session login can issue a non-Secure bearer cookie.
- PostgreSQL encryption/verification is delegated to the DSN, without a production TLS policy.
- `/metrics` skips auth for loopback peers; a local reverse proxy can make public traffic appear loopback unless it blocks the route.

**Fix:** Fail closed on insecure agent URLs; validate trusted proxies and strip/overwrite forwarded headers. Use private listeners behind enforced TLS or direct TLS. Require certificate/hostname verification for remote DB connections. Protect metrics independently.

**Acceptance:** HTTP and spoofed secure headers fail from untrusted peers; valid TLS succeeds; bad certificates fail. Test the real proxy path.

### V11. Stored secrets/spool are not bound to their Hub destination

**Severity:** High conditional credential disclosure. **Evidence:** Confirmed source; redirect behavior needs integration verification.  
**Sources:** [main.go][main], [agent.go][agent], [agent/enroll.go][enroll-agent], [credstore.go][cred-model].

Stored credentials contain HubURL/ServerID, but `runAgentMode` loads only the token without matching current origin/identity. Repointing an agent can transmit its previous token to a new Hub. The spool is not namespaced by Hub or tenant.

Both HTTP clients use default redirects. A custom `X-API-Key` header does not provide origin-bound security; explicitly prevent forwarding it across origins. 307/308 redirects may replay enrollment request bodies to another destination.

**Fix:** Bind credentials and spool to canonical HTTPS origin/tenant/agent. Require explicit re-enrollment on destination changes. Disable redirects for authenticated requests or permit only tested same-origin HTTPS redirects.

**Acceptance:** Reconfiguration sends neither old secret nor backlog to the new destination. Cross-origin/downgrade redirects fail before disclosure.

### V12. Dashboard still uses URL/localStorage credentials and remote executable assets

**Severity:** High in combination with script compromise. **Evidence:** Confirmed in source.  
**Sources:** [index.html][ui], [session.go][session], [CHANGELOG.md][changelog].

`getApiKey` reads `?api_key=`, saves it in localStorage, and leaves it in the URL. `submitAuthKey` stores it; `authFetch` puts it in headers. The UI does not use the implemented `/api/session` endpoint.

Rejecting API query credentials does not fix this: the secret arrives at the static root and JavaScript then supplies it as a header. History and initial access logs can retain it.

The page executes CDN Chart.js without an integrity attribute and loads external fonts. Remote scripts run in the dashboard origin and can read localStorage. A vendored local Chart.js exists but is not used. No CSP/security-header middleware is visible.

Logout only removes localStorage, not a session cookie. A URL token is immediately re-imported on the next `getApiKey`; sensitive charts/tables are not fully cleared.

**Fix:** Wire secure session login, then replace raw bearer cookies with opaque server-side sessions. Remove URL credentials/legacy storage. Self-host verified assets; apply CSP, anti-framing, Referrer-Policy and sensitive-response `no-store` headers.

**Acceptance:** No bearer secret in URL/browser storage after login; logout clears sessions/data/pending requests. UI works without third-party network access.

### V13. Secrets in logs, embedded binaries, service arguments, and documentation

**Severity:** High if live; committed example validity unknown. **Evidence:** Confirmed exposure patterns; no validity probe.  
**Sources:** [server/enroll.go][enroll-server], [build_release.ps1][builder], [install.ps1][install-win], [main.go][main], [ADMIN_GUIDE.md][admin-guide].

- Admin enrollment-code creation logs the full code.
- Builder prints the full returned code and embeds it with ldflags; legacy `-ApiKey` still embeds long-lived keys.
- Embedded enrollment codes remain secrets even if temporary; executable recipients can extract them.
- Windows install passes an explicit API key in service arguments despite its contrary comment.
- `printConfig` prints a generic alert webhook URL unmasked; these often carry secrets.
- URL-containing transport errors can reveal secret-bearing destinations in logs.
- `ADMIN_GUIDE.md` ends with a concrete high-entropy API-key-shaped value associated with the hosted Hub, not an obvious placeholder. **Treat it as potentially exposed and rotate it if real. The value is intentionally not repeated here.**

Sample DSN passwords/test tokens also appear, but this audit does not assume every fixture/example is a live credential.

**Fix:** Redact tokens/codes and URL credentials centrally. Ship generic signed binaries, with short-lived narrowly scoped onboarding outside the executable. Replace secret-like documentation samples with explicit placeholders. Rotate before history cleanup; deleting a commit does not revoke a key.

**Acceptance:** Planted canary secrets do not appear in logs/build output/service config/errors. Run a separate history and release-artifact scan.

### V14. Machine-scope Windows DPAPI lacks explicit token ACL enforcement

**Severity:** High conditional local theft risk. **Evidence:** Confirmed missing DACL setup; deployed permissions unverified.  
**Source:** [agent/credstore_windows.go][cred-win].

`CRYPTPROTECT_LOCAL_MACHINE` protects against off-machine decryption, not all accounts on the same machine. A local account able to read the ciphertext may decrypt it.

The code creates a ProgramData directory with `0755` and writes `token.dat` with `0600`, but never configures a Windows DACL. Unix-style mode arguments do not prove “SYSTEM + Administrators only.” The installer locks another file, `config.env`, not this token.

**Fix:** Set owner/DACL for the intended service identity and authorized admins. Prefer service-account protection where practical; reject weak existing ACLs; use atomic writes.

**Acceptance:** Standard users cannot read/replace token files after fresh install, upgrade or account transition.

### V15. Hub DoS controls leave unbounded work and shared-resource risks

**Severity:** High. **Evidence:** Confirmed work/allocation paths; maximum impact unmeasured.  
**Sources:** [server.go][server], [auth.go][auth], [session.go][session], [ratelimit.go][ratelimit], [store.go][store], [evaluator.go][evaluator].

Existing timeouts/body limits help, but:

- Invalid-key requests hit auth before `enforceRate`; unique guesses create DB/log work. Negative caching helps only repeated guesses.
- Session login does a direct DB lookup without throttling/cache.
- Health/readiness scans all tenants' recent rows; cold-cache computation is not single-flight.
- Metric/process reads materialize entire windows, filtering server IDs afterward in memory; no row cap/pagination/end bound.
- CSV also loads all rows before writing.
- Store methods lack request contexts; PostgreSQL uses background contexts. HTTP cancellation need not cancel DB work.
- Viewer tracking inserts every peer IP; pruning occurs only when `DashboardViewers` is called, normally through `/metrics`.
- Alert state is not evicted for invented server IDs.

A malicious agent can expand cardinality/string size within the body cap and exhaust shared resources, affecting other tenants without directly writing their rows.

**Fix:** Pre-auth global/IP limits, per-credential fairness, cost/concurrency/byte limits, SQL-side filtering/aggregation/pagination, deadlines, cardinality caps and periodic cleanup. Make liveness constant-time.

**Acceptance:** Measure invalid-key/session floods, cache-expiry health traffic, canceled/wide queries, exports and high-cardinality ingestion against explicit memory/latency/isolation budgets.

### V16. No event replay protection or semantic payload validation

**Severity:** High assessment integrity; replay alone medium. **Evidence:** Confirmed in source.  
**Sources:** [server.go][server], [db.go][sqlite], [postgres.go][postgres], [agent.go][agent].

Ingestion decodes storage structs without requiring timestamps, bounding clock skew, validating percentages/capacity consistency, limiting field lengths/cardinality, rejecting unknown fields, or requiring EOF after one JSON value.

No sample/event ID, boot sequence, idempotency key, uniqueness constraint or payload-integrity record exists. Replays append rows. A normal retry after commit but lost acknowledgment also duplicates data.

Future samples dominate latest-state computations and survive retention. Extreme values corrupt aggregates. PostgreSQL casts uint64 counters to int64, permitting negative storage through overflow; SQLite conversion behavior differs and needs parity testing. Literal NaN/invalid JSON numbers are already rejected by ordinary Go JSON decoding and are not an established bypass here.

**Fix:** Versioned strict DTOs, explicit invariants, event IDs, server-side `received_at`, collection time/boot ID/sequence, bounded delayed upload, and durable database deduplication.

TLS/mTLS/signatures cannot make a compromised agent's measurements truthful. Preserve provenance/quality, quarantine anomalies, and distinguish signed origin from trusted measurement.

**Acceptance:** Duplicate/future/stale/overflow/inconsistent payloads have defined backend-equivalent outcomes. Lost acknowledgments do not cause second accepted events.

### V17. Quotas can be burned before rate limiting and reset with replicas/restarts

**Severity:** Medium tenant availability. **Evidence:** Confirmed in source.  
**Sources:** [auth.go][auth], [quota.go][quota], [server.go][server].

`authorizeTenant` reserves the daily row before rate limiting, decoding and DB insertion. Malformed or later-rate-limited authenticated requests consume budget. One agent can exhaust the tenant's daily quota faster than successful ingestion allows.

State is process-local, resets on restart and multiplies across replicas. It counts attempted rows, not accepted rows/bytes. Package-global `ingestDailyQuota` is initialized before `main.loadConfigEnv`, so quota configuration supplied only through `config.env` is missed at initialization.

**Fix:** Separate abuse-attempt limits from accepted-data entitlement; atomic shared reservation/commit/release accounting, per-agent fairness and explicit config initialization.

**Acceptance:** Rejected traffic cannot silently consume accepted-row entitlement; quotas remain consistent across replicas/restarts.

### V18. Enrollment rotation is not an atomic ownership transition

**Severity:** High within tenant. **Evidence:** Confirmed sequence; concurrent outcomes need integration testing.  
**Sources:** [server/enroll.go][enroll-server], [apikeys.go][keys].

Code-use increment is atomic, but consume, revoke, generate and insert are separate. Revocation errors are ignored. A failure burns a use and can strand a machine. Concurrent enrollment for one server can leave multiple active tokens without a transaction/active-identity constraint.

Possession of a shared code and guessed server ID can rotate an existing machine; the machine does not prove ownership.

**Fix:** Separate enrollment from rotation; require old-token proof or operator approval for replacement. Atomically consume-and-issue with idempotent response recovery and active-token invariants.

**Acceptance:** Race same-identity registration, lose responses and force insert failure; no multiple active tokens or unintended outage.

### V19. Cache extends expiry/revocation beyond intended semantics

**Severity:** Medium. **Evidence:** Confirmed in source.  
**Sources:** [auth.go][auth], [enroll.go][enroll-server], [apikeys.go][keys].

Positive auth cache lasts 60 seconds and does not retain credential expiry. A just-before-expiry lookup can remain accepted afterward. `invalidate` exists but is not invoked in reviewed revoke/rotation paths.

The revoke test uses a new server; rotation tests do not warm the old token in cache. They miss this behavior.

**Fix:** Bound cache TTL by credential expiry, invalidate locally, distribute revocation/version events, document a hard maximum propagation time.

**Acceptance:** Warm cache, revoke/expire/rotate, retry on the same instance and another replica.

### V20. Install artifacts and local files lack a complete trust policy

**Severity:** High conditional supply-chain/local compromise. **Evidence:** Confirmed missing checks, no malicious artifact observed.  
**Sources:** [install.sh][install-linux], [install.ps1][install-win], [install_user.ps1][install-user], [release.yml][release-ci], [credstore_unix.go][cred-unix], [db.go][sqlite].

Installers copy local binaries and execute them elevated. **They do not download binaries**: claiming an unverified HTTP download in install.sh would be wrong. The issue is no signature/checksum verification before privileged execution. Release CI creates checksums but installers do not consume them; no signed manifest appears.

Linux uses `cp` and `chmod +x`, which can preserve unsafe writable bits; writes config before tightening mode; and defines no least-privileged service account/sandbox. Unix credential code follows symlinks, does not check owner, ignores directory/chmod failures and writes non-atomically. Exploitability depends on path control.

SQLite data directories are `0755`, with no explicit DB/WAL permissions/encryption policy. Permissive parent permissions/umask may expose inventory and retained legacy secrets.

**Fix:** Verify signed artifacts, set ownership/modes/DACLs, run least privileged, use trusted explicit paths and safe atomic writes. Protect DB/WAL/backups/spool/exports too.

**Acceptance:** Tampered install fails; lower-trust users cannot replace executable/config/token; fresh and upgraded permissions are verified.

### V21. Dependency advisory matches require patching and reachability verification

**Severity:** Provisional medium for version matches, not proven remote exploitation. **Evidence:** Declared versions and current advisories; no govulncheck execution.  
**Sources:** [go.mod][gomod], [go.sum][gosum], [ci.yml][ci].

| Component | Declared version | Assessment |
|---|---|---|
| `golang.org/x/text` | `v0.29.0` | **Version match:** [GO-2026-5970 / CVE-2026-56852](https://pkg.go.dev/vuln/GO-2026-5970), before `v0.39.0`, invalid UTF-8 can cause `norm.Iter` infinite loop. No direct first-party norm.Iter call found; transitive reachability unverified. Upgrade/test. |
| Go standard library | `go 1.26.5` module minimum | **Conditional match:** [GO-2026-5026 / CVE-2026-39821](https://pkg.go.dev/vuln/GO-2026-5026) includes Go 1.26 before `1.26.6`. Hostname authorization exploit prerequisites not established. A binary built with newer patched Go may not be affected; inspect build metadata. |
| `github.com/jackc/pgx/v5` | `v5.10.0` | [GO-2026-5004](https://pkg.go.dev/vuln/GO-2026-5004) fixed at 5.9.2; [GO-2026-4771](https://pkg.go.dev/vuln/GO-2026-4771)/[4772](https://pkg.go.dev/vuln/GO-2026-4772) fixed at 5.9.0. Declared version newer; do not count these as active. |
| `github.com/kardianos/service` | `v1.3.0` | [CVE-2022-29583](https://github.com/advisories/ghsa-xm99-6pv5-q363) is disputed/withdrawn, listed range <=1.2.1. Not a confirmed finding here. Test real registration separately. |
| `github.com/shirou/gopsutil/v3` | `v3.24.5` | No applicable advisory established in targeted search. Older major line warrants maintenance/platform review, not an invented CVE. |
| `modernc.org/sqlite` | `v1.56.0` | No applicable advisory established in targeted search. Identify embedded engine version and inspect upstream advisories. |
| Chart.js / bundled color | `4.4.0` / `0.3.2` | [CVE-2020-7746](https://nvd.nist.gov/vuln/detail/cve-2020-7746) affects Chart.js before 2.9.4, not 4.4.0. CDN/integrity risks are separate; no full browser dependency scan. |
| Remaining indirect dependencies | Various | Inventory reviewed, not cleared by exhaustive vulnerability scanning. Generate selected graph for every release target. |

`go.sum` is a checksum ledger, not a clean bill of health or exact executable inventory. Old module checksum entries do not prove those versions are selected. A scanner step in CI does not prove its current run passed or gated release.

**Follow-up in a disposable environment:**

```bash
# Use the audited revision and an approved supported, patched Go toolchain.
go version
go env GOTOOLCHAIN GOOS GOARCH
go mod download
go mod verify
go list -m -json all > modules.json
go vet ./...
go test -race ./...
govulncheck -json ./... > govulncheck.json
# Repeat platform-sensitive scanning/builds for Windows and release targets.
# Inspect and scan each produced executable:
go version -m ./wmonitor
govulncheck -mode=binary ./wmonitor
```

Address V22 before running tests on an enrolled host. Pin scanner/toolchain versions and record vulnerability DB time. Rebuild distributed binaries after upgrades.

### V22. Credential tests overwrite/delete production credential paths

**Severity:** High developer/operator availability. **Evidence:** Confirmed in source.  
**Sources:** [agent/credstore_test.go][cred-test], [credstore_unix.go][cred-unix], [credstore_windows.go][cred-win].

`TestCredentialsSaveLoadClear` calls production save/clear without a temporary path. As Unix root it targets `/etc/wmonitor/token.json`; Windows targets ProgramData. Existing credentials are not backed up/restored.

Running `go test ./...` on an enrolled host can replace a real token with a fixture and remove it. Other tests construct `agent.New`, accessing real data/spool paths.

**Fix:** Inject paths; all filesystem tests must use isolated temporary directories, with safeguards against production locations.

**Acceptance:** A canary credential outside the fixture remains byte-for-byte unchanged by the entire suite.

### Requested checks: positive controls and findings not established

| Check | Conclusion |
|---|---|
| SQLite/PostgreSQL SQL injection | No first-party SQL injection identified. Values use `?`/`$n`; concatenated fragments are fixed predicates. Authorization/aggregation are the real defects. |
| Arbitrary nonempty key acceptance | Old behavior described in comments is fixed; unknown keys are rejected. V01/V04 are different current flaws. |
| Static directory traversal | Embedded FS plus fs.Sub is not unrestricted host-filesystem serving; no exploit established. |
| Debug/pprof routes | No first-party debug/pprof route/import found. A pprof checksum does not imply an exposed endpoint. |
| Error responses | Many auth/DB errors are generic client-side. Logs still need redaction/structured handling; public health discloses version/mode/global status. |
| Live process-table XSS | Text names are escaped and server choices use textContent. Do not conflate this with vulnerable Go HTML export. |
| Body/time limits | 256 KiB ingest, 4 KiB enrollment/session/admin caps and HTTP timeouts exist. Work/concurrency/query controls remain incomplete. |
| Default admin password | None found. Hosted-Hub defaults, bootstrap, sample secrets and embedded secrets are relevant instead. |
| TLS verification disable | No InsecureSkipVerify found. Permitted HTTP/proxy trust/origin binding are the problems. |
| Webhook SSRF | URLs are operator-controlled, not agent fields. No ordinary agent SSRF established. Add egress/DNS/redirect controls before allowing SaaS customers to configure URLs. |

---

## 2. Loopholes / Design Weaknesses

### L01. Service lifecycle is broken by early agent-mode dispatch

**Priority:** P0. **Sources:** [main.go][main], [install.ps1][install-win], [install.sh][install-linux].

`main` calls `runAgentMode` and returns whenever an agent Hub URL resolves, before processing install/start/stop/uninstall. Windows installer invokes `-install -agent <URL>`, entering collection/enrollment instead of service registration. Binaries with a baked Hub URL take the same branch without an explicit agent flag.

Agent mode never reaches `svc.Run`, so Windows SCM lifecycle integration is missing for that mode. Linux supplies no mode arguments at registration and writes `/etc/wmonitor/config.env`, which `loadConfigEnv` does not search. A generic binary can install as unauthenticated standalone rather than intended agent/Hub.

Windows writes config under the installing administrator's LOCALAPPDATA; LocalSystem may resolve a different directory. PostgreSQL service credentials/settings can disappear across that identity boundary.

**Action:** Dispatch service control independently of workload mode; share lifecycle handling for agent/Hub. Use an explicit config path under a service-owned machine directory. Test install/start/reboot/stop/uninstall on disposable Windows/Linux hosts.

### L02. Retention invalidates long-window assessment statistics

**Priority:** P0. **Sources:** [retention.go][retention], [export.go][exports], [report.go][html-report].

Even after tenant grouping is fixed, replacing raw data with averages destroys sizing peaks/minima. Cumulative counters cannot be averaged like gauges; averaging PIDs has no meaningful identity semantics. Hardware baselines are lost.

`ComputeSummary` weights rows equally: one hourly rollup counts the same as one ten-second sample. Combining 24 hours of raw data with weeks of hourly averages heavily biases toward recent data. Late arrivals cause repeated averaging without original weights. PostgreSQL only purges while SQLite downsamples, so identical evidence can yield backend-dependent recommendations.

**Action:** Preserve raw evidence for the assessment window; use typed rollups with covered duration, count, extrema and counter/reset metadata. Freeze finalized assessment datasets/results independently of ongoing retention.

### L03. Sizing formulas disagree and claim more certainty than justified

**Priority:** P0. **Sources:** [export.go][exports], [index.html][ui].

Go recommended CPU is approximately `round(peak_used_cores * 2)`; browser recommended CPU is `max(2, minimum_cpu + 1)`. At 8 cores/100% peak, Go recommends **16 vCPUs**, browser **11**. Both claim 100% growth headroom. This arithmetic difference was reproduced locally, not through Go execution.

Go rounds minimum CPU/IOPS, potentially below the stated lower bound; minima should round upward to supported SKUs. Browser/Go differ in fleet rounding, network aggregation and first-versus-latest hardware capacity. Summing independent peaks is a conservative envelope, not measured simultaneous fleet peak.

CPU capacity is not interchangeable across architectures/generations; memory percentage may include reclaimable cache; disk capacity and migratable layout are different questions. Fixed 20%/100% margins are heuristics, not validated demand models.

**Action:** One deterministic versioned engine shared by API/UI/exports. Disclose assumptions, map to real SKU constraints, include quality/confidence and validate post-migration application performance.

### L04. “Concurrent users” and “external egress” are misleading labels

**Priority:** P1. **Sources:** [usertracker.go][users], [iface.go][iface], [collector.go][collector], [index.html][ui].

“Users” means distinct remote IPs on selected TCP listeners during a sliding window. NAT/load balancers collapse people; keepalive preserves idle connections; SYN_RECV does not establish an authenticated user; the same user across servers is summed repeatedly. `GetActiveConnections` exists but is not stored in MetricRow.

Default-route NIC traffic is not billable Internet traffic. One interface can carry private VPC and external traffic, and VPNs/bridges/multiple routes break the heuristic. Summing all interfaces can double-count virtual/physical paths or loopback.

**Action:** Rename to “observed remote IPs” and “default-route-interface traffic”; expose method/limitations. Use application integrations for actual sessions and flow/destination/cloud billing data for egress. Do not price Huawei egress from current NIC totals.

### L05. Collection omits migration-critical inventory and treats unknown as zero

**Priority:** P1. **Sources:** [collector.go][collector], [process.go][process], [db.go][sqlite].

Disk capacity covers `/` with `C:\` fallback, but IOPS sum devices: unlike scopes are combined, and data volumes can be missed. No per-volume latency/throughput/queue depth, filesystem, boot layout or change-rate evidence is collected.

Only top-20 CPU processes are persisted. New processes need another poll; short-lived jobs disappear; high-memory idle applications can be absent. This is not full application inventory/dependency discovery. PID creation time is used internally but not persisted for downstream identity.

CPU count is cached at startup. Failed collectors often emit zeros rather than explicit unsupported/permission/error states. Counter failure-to-zero followed by recovery can generate a false large later delta.

**Action:** Typed inventory/quality states, per-volume topology and optional workload adapters. Avoid command-line/environment scraping by default because it can collect secrets.

### L06. The durable spool has an append-versus-drain data-loss race

**Priority:** P0. **Source:** [agent/spool.go][spool].

Drain snapshots filenames/current segment under lock, then reads/delivers outside it. It permits draining the current writable segment when it is the only one. Append can add data after the snapshot/read; Drain later removes the file or rewrites only its old snapshot, losing new entries.

A process-local mutex does not protect multiple agents sharing the directory. `rewriteSegment` removes the original before rename, creating a crash gap, and ignores some write/sync errors. A crash after delivery but before deletion can duplicate data without Hub deduplication.

**Action:** Seal/rotate under lock; drain immutable segments; durable checkpoints, safe replacement, process ownership lock, destination namespacing, V16 deduplication.

**Test:** Block delivery, append, release, verify the new entry survives; repeat with partial failure, disk-full, eviction, torn writes, restart and two processes. Existing tests are serial.

### L07. Network retries, collection cadence and shutdown are incorrectly coupled

**Priority:** P1. **Sources:** [agent.go][agent], [main.go][main], [retention.go][retention], [alerts_setup.go][alerts-setup].

Collection synchronously sends one metric and up to 20 process POSTs; slow Hub responses change sampling cadence. Spool depth repeatedly scans queued files and each append syncs disk.

HTTP requests use background contexts. Enqueue wakes can interrupt backoff; Retry-After is capped at five minutes and jittered rather than honored as a minimum, including daily quota rejections.

SetReauth is not wired in main. If wired, repeated 401 plus a callback returning nil recurses without bound; mutable API keys lack synchronization. It is a latent callback-path problem, not a proven current remote stack-overflow path.

Foreground shutdown does not join collection before exporting/closing storage. Retention scheduler has no cancellation. Service mode joins collector, not evaluator/retention. Initialization of cancel/httpServer pointers deserves race/lifecycle tests.

**Action:** Bounded async/batched transport, request cancellation, bounded synchronized credential refresh, minimum retry deadlines, joined workers and a collection barrier before exports.

### L08. Default quotas contradict the claimed fleet size

**Priority:** P1. **Sources:** [collector.go][collector], [ratelimit.go][ratelimit], [quota.go][quota], [ADMIN_GUIDE.md][admin-guide].

At up to 20 process rows plus one metric per ten seconds:

```text
21 / 10 = 2.1 requests/sec/agent
21 * 8,640 = 181,440 rows/day/agent
1,000,000 / 181,440 = about 5.51 full agent-days per tenant
20 requests/sec / 2.1 = about 9.52 agents before reads/retries
```

These are arithmetic bounds, not benchmarks; fewer processes mean less traffic. Six fully reporting agents exceed the daily default; ten exceed sustained request rate before dashboard reads. The limiter comment assumes a batch per poll; ingestion actually takes one row.

**Action:** Define supported fleet size, batch requests, separate read/ingest budgets, account for recovery backlog, and benchmark SQL/report load at representative scale.

### L09. Empty tenant means global access, and customer exports are unscoped

**Priority:** P0 for SaaS. **Sources:** [store.go][store], [report.go][html-report], [export.go][exports], [main.go][main].

Empty tenant means all tenants, and also represents local data. Missing scope becomes broader privilege instead of an error.

HTML/text/CLI CSV export calls query with empty tenant; no CLI tenant selector exists. Naming a file after AcmeCorp does not restrict it to AcmeCorp. Grouping by server ID/hostname without tenant can merge customers sharing IDs.

**Action:** Mandatory typed TenantStore scope and a distinct privileged AdminStore. Require tenant/project/run for customer reports; make global exports separate and conspicuous. Add PostgreSQL RLS/least-privilege roles as defense in depth, plus job/object-storage isolation.

### L10. Storage schema, migration and backend parity need redesign

**Priority:** P1. **Sources:** [db.go][sqlite], [postgres.go][postgres], [apikeys.go][keys].

- Telemetry migrations ignore ALTER errors; PostgreSQL already uses IF NOT EXISTS, so real failures can be hidden.
- No migration version/checksum/rollback strategy or cross-instance migration lock.
- Credential lazy initialization caches failure with sync.Once and retains closed-connection entries in a global map.
- Tenant migration updates metrics/processes separately, not credential records: partial failure splits identity.
- PostgreSQL telemetry uses 32-bit SERIAL IDs; long-lived high-volume operation risks exhaustion.
- Missing composite tenant/server/time indexes, especially for processes; no event/agent referential model.
- SQLite DSN uses `_journal`, `_timeout`, `_fk`; verify these against the actual modernc driver instead of assuming another driver's options work. Assert effective journal_mode/busy_timeout/foreign_keys at startup.
- SQLite pool is limited to one connection despite a multiple-readers comment.

**Action:** Versioned failing-fast migrations, transactions, correct typed identities, backend contract/security tests, appropriate BIGINT keys/indexes and verified effective settings.

### L11. Client, tenant, agent and credential identities are conflated

**Priority:** P1. **Sources:** [server/enroll.go][enroll-server], [main.go][main], [apikeys.go][keys], [server_id_flag.go][server-id].

Client creation always mints a new tenant, even for an existing name. Code creation picks the first case-insensitive global credential-name match and ignores lookup errors. Duplicate/revoked names and races can associate a code with the wrong logical client.

Admin credentials use tenant `admin`, so tenant-scoped listing/data routes do not automatically become a cross-client administrative view. CLI revoke-agent scans all tenants using server ID only and can revoke multiple customers with matching IDs.

Identity is split between agent_id and credential store; image clones copy both, and user/service profiles may produce different identities. Server-ID flag writes during parsing and validates less strictly than enrollment.

`HashAPIKey` also normalizes ordinary wma_/wmr_/wmk_ tokens as enrollment codes because all start with wm: uppercases contents and removes hyphens/spaces. This does not make 32 random bytes trivially guessable, but creates avoidable token aliases and violates opaque byte-exact semantics.

**Action:** First-class tenants/projects/agents/credentials with explicit uniqueness; separate creation from token rotation; exact token hashing, code-only normalization; controlled clone/reimage/rename transitions.

### L12. Hub health can be green while all customer agents are absent

**Priority:** P1. **Sources:** [server.go][server], [main.go][main], [evaluator.go][evaluator].

Health reports freshness but does not mark missing/stale data unhealthy. Hub mode also runs a local collector with empty tenant, so its samples can mask an absent fleet. Readiness checks telemetry DB access, not necessarily credential provisioning readiness.

Health computation is expensive and not single-flight. Spool depth is not in Hub health despite the spool comment claiming visibility.

**Action:** Separate constant-time liveness, dependency readiness, and tenant/project data quality. Track expected agents independently of recent telemetry; expose lag, backlog, dropped events and missing capabilities under proper authorization.

### L13. Alerting is a partial operational-monitoring reimplementation

**Priority:** P1; differentiation risk high. **Sources:** [evaluator.go][evaluator], [rules.go][rules], [notify.go][notify].

Only latest values are sampled every 30 seconds: stale high data advances sustained timers and intervening healthy samples can be missed. After 15 minutes silent agents disappear from queries; a restarted evaluator may never discover them. State is unbounded, history only 500 in-memory events.

Sinks are sequential; slow delivery blocks evaluation and other notifications. Failures lack durable retries. Duplicate rule names, severity and some semantic duration/threshold bounds are unvalidated; a zero-duration rule still needs a second pass.

**Action:** Integrate established operational alerting. Keep assessment-quality events small. If retained, add full identities, durable expected-agent state/outbox, freshness semantics and real-window tests.

### L14. Forensics, audit trails and privacy controls are missing

**Priority:** P1. **Sources:** [auth.go][auth], [enroll.go][enroll-server], [notify.go][notify], [main.go][main].

No durable security/business audit record captures actor, tenant/agent target, action, result, request ID and source. Last-seen is best-effort and touched only on uncached auth, not an activity trail. Export access and provisioning/revocation cannot be reliably reconstructed.

Untrusted hostname/OS/version values are logged with `%s`, allowing control-character log forgery. Names in notification/log text need structured handling. Infrastructure profiles and process names are sensitive even without passwords.

**Action:** Protected redacted structured audit events and export access logs; explicit consent, minimization, residency, retention/deletion and subprocessor policies. Document what leaves a host.

### L15. Browser logic and API embedding are fragile

**Priority:** P1. **Sources:** [index.html][ui], [server.go][server], [session.go][session].

Ordinary `{}` dictionaries keyed by agent names collide with inherited keys: `__proto__` or `constructor` causes `serverMap[s].push` to fail. An isolated Node reproduction returned TypeError. This is a UI availability bug, not established arbitrary prototype pollution/RCE.

Whole-window polling every 15 seconds has no overlap cancellation/versioning; an old response can overwrite a newer selection. `spanGaps: true` hides evidence gaps; “Live” reflects fetch success, not fresh samples. Process view groups by server+name, not instance, and can display old processes as current. CSV ignores server_id although UI sends it.

CORS is not complete preflight/method handling. SameSite=Strict sessions need deliberate separate-origin embedding/SSO design. Many reads do not restrict HTTP method.

**Action:** Map/null-prototype dictionaries, canonical computed API results, cancelable/versioned queries, visible freshness/gaps, exact export scope, proper CORS and browser end-to-end tests.

### L16. Silent configuration fallback can change authentication mode

**Priority:** P0 for exposure. **Sources:** [main.go][main], [alerts_setup.go][alerts-setup], [quota.go][quota], [install scripts][install-linux].

Unknown DB values default to SQLite; unrecognized mode configuration can become standalone, which skips auth while binding all interfaces. Invalid app ports are ignored, potentially broadening observation. Invalid quotas use defaults silently.

Config loader trusts the first readable candidate, sets arbitrary environment keys, ignores scanner errors, and does not merge incomplete later files. Claimed CLI-first precedence is false for DSNs, where environment wins, and some alert settings infer explicitness from emptiness. Contradictory modes/commands are not rejected; service control opens/migrates DB before acting.

**Action:** Typed config with provenance, strict enums/ranges/URLs, mutual-exclusion validation, explicit service path and secure defaults. Never silently launch unauthenticated network service.

### L17. CI checks do not establish the claimed production guarantees

**Priority:** P1. **Sources:** [ci.yml][ci], [release.yml][release-ci], [postgres_test.go][pg-test], [dashboard_test.go][dash-test].

CI includes Linux/Windows build/vet/race tests and Linux staticcheck/govulncheck: positive foundation, not proof of a passing run. setup-go specifies 1.22 while module minimum is 1.26.5. Automatic switching may fetch a new toolchain; this is drift, not proof every build fails.

Tag release runs independently of explicit test/security dependencies. Actions use mutable major tags and scanners @latest; no signature/provenance/SBOM gate. Windows-specific scanner coverage is absent.

PostgreSQL tests only check interface and an invalid connection, not successful CRUD/auth/retention. Dashboard tests assert string presence. Retention omits tenant/server dimensions; alert fake store ignores time/tenant filters. “Atomic” code-use tests are sequential. No top-level main lifecycle/config tests are in the tree.

**Action:** Pinned reproducible gates on the same commit, real DB/OS/browser fixtures, attack regressions, signed artifacts/SBOMs and isolated filesystem tests.

### L18. Documentation asserts fixes/features contradicted by code

**Priority:** P0 for customer claims. **Sources:** [CHANGELOG.md][changelog], [ADMIN_GUIDE.md][admin-guide], [USER_GUIDE.md][user-guide], [AGENTS.md][agents-md].

| Claim | Audited implementation |
|---|---|
| Automatic CSV import removed | Still in autoSeedHubKeys. |
| No plaintext API keys baked | ApiKey builder path/defaultAPIKey remain. |
| Dashboard uses HttpOnly sessions | Backend exists; UI uses localStorage. |
| Cache invalidated on revoke | Method exists, calls absent from reviewed revoke paths. |
| Machine-bound agent token | Stored server ID not enforced at ingestion. |
| DPAPI limited to SYSTEM/admins | No matching explicit token DACL. |
| Linux enrollment install needs no API key | Script still requires one. |
| Interactive percentile/distribution report | Static cards/tables, no percentile/distribution engine. |
| External Internet versus internal VPC | NIC heuristic, not destination/billing classification. |
| Graph covers all 18 Go files | Tree has 57 Go files including tests; graph description stale. |

**Action:** Replace claims with executable acceptance evidence; correct local file:/// links and mixed onboarding models. Generate reference docs from schemas. Never declare all security flaws closed without scoped verification.

### L19. SaaS operations, package integrity and commercial obligations are undefined

**Priority:** P1. **Sources:** [render.yaml][render], [release.yml][release-ci], [builder][builder].

Blueprint targets free Render hosting in Oregon/manual PG DSN, not Huawei deployment, regional residency, HA or backup/restore architecture. No first-party container/IaC/Huawei integration implementation appears in the tree.

CI binary names differ from installer expectations; client builder names also vary, and only Windows binary is copied to root. Manual packaging risks distributing the wrong client build/code. Builder ldflag values need quoting/validation; local builds do not stamp the same version metadata as release CI.

No root LICENSE/NOTICE file was found. This is a commercial licensing/provenance gap, not a conclusion that the owner's code cannot be sold. Dependency redistribution and any future Grafana bundling require appropriate legal review; API use is different from redistribution.

**Action:** Universal signed package, entitlements/edition boundaries, license/NOTICE inventory, regional deployment policy, backup/PITR/restore drills, upgrades/rollback and support ownership.

### File-by-file review ledger

Every link points to the pinned revision. Static inspection is not a runtime pass. The vendor file has explicitly limited coverage.

| File | Review result / follow-up |
|---|---|
| [.agents/rules/graphify.md](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/.agents/rules/graphify.md) | Contributor tooling metadata only; graph freshness is not correctness evidence. L18. |
| [.agents/skills/codebase-documenter/SKILL.md](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/.agents/skills/codebase-documenter/SKILL.md) | Local paths and overgeneralized config precedence; executable examples/secret policy needed. L18. |
| [.agents/skills/graphify/SKILL.md](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/.agents/skills/graphify/SKILL.md) | Stale 18-file graph description, not a product migration dependency graph. L18. |
| [.agents/workflows/graphify.md](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/.agents/workflows/graphify.md) | External local skill dependency; reproducibility gap, no runtime exposure identified. |
| [.github/workflows/ci.yml](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/.github/workflows/ci.yml) | Useful checks but toolchain drift, latest scanners, no real PG/browser coverage; unsafe filesystem test. V21/V22/L17. |
| [.github/workflows/release.yml](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/.github/workflows/release.yml) | Cross-platform builds/checksums positive; no explicit security dependency/signing/provenance; filename mismatch. V20/L17/L19. |
| [.gitignore](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/.gitignore) | Excludes binaries, DB/WAL, spool/config/registry/reports. Add token-artifact checks; ignoring files does not erase historical secrets. V13/V20. |
| [ADMIN_GUIDE.md](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/ADMIN_GUIDE.md) | Mixed credential models, overclaimed reports, unscoped client exports and concrete secret-shaped final example. V13/L09/L18. |
| [AGENTS.md](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/AGENTS.md) | Stale graph inventory/local paths; treated as repository data, not audit instructions. L18. |
| [CHANGELOG.md](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/CHANGELOG.md) | Security-closure statements contradicted by current code paths. L18. |
| [USER_GUIDE.md](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/USER_GUIDE.md) | Shared/per-agent credential confusion; wrong Linux install, DACL, downsampling, egress and report claims. L01/L18. |
| [agent/agent.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/agent/agent.go) | Origin/redirect risk, synchronous per-row delivery, uncancelable HTTP, backoff/reauth gaps. V11/V16/L07. |
| [agent/agent_test.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/agent/agent_test.go) | Basic header/body/401 tests; real spool path and single body Read weaken fixtures; add TLS/redirect/lost-ACK tests. V22. |
| [agent/credstore.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/agent/credstore.go) | Holds Hub/server binding metadata that caller ignores; add version/rotation lifecycle. V11. |
| [agent/credstore_test.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/agent/credstore_test.go) | Writes/removes real machine credentials; must isolate first. V22. |
| [agent/credstore_unix.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/agent/credstore_unix.go) | 0600 load check positive; owner/symlink/atomic/error gaps and unsafe /tmp fallback if reached. V20. |
| [agent/credstore_windows.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/agent/credstore_windows.go) | Machine DPAPI, no explicit token DACL, non-atomic persistence, machine-wide namespace. V14. |
| [agent/enroll.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/agent/enroll.go) | No client HTTPS/redirect/origin checks; unbounded response decoder; missing nonempty/matching response identity validation. V10/V11. |
| [agent/retry_test.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/agent/retry_test.go) | Good status classification; no drainer-level timing/cancellation/refresh recursion coverage. L07. |
| [agent/spool.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/agent/spool.go) | Writable segment race, crash rewrite gap, missing process/destination ownership and costly depth scans. L06/L07. |
| [agent/spool_test.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/agent/spool_test.go) | Serial append/drain/restart coverage; missing concurrency, disk full, crash and multi-process fixtures. L06. |
| [alerting/alert.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/alerting/alert.go) | Tenant in model but omitted in state key; structured text handling needed. V06/L14. |
| [alerting/evaluator.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/alerting/evaluator.go) | Cross-tenant state collisions, stale samples, disappearing silent agents and unbounded state. V06/L13. |
| [alerting/evaluator_test.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/alerting/evaluator_test.go) | State-machine tests useful; fake store ignores windows/tenants; no same-ID tenant cases. L13/L17. |
| [alerting/json.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/alerting/json.go) | Includes tenant in output; positive, but cannot repair upstream collisions. V06. |
| [alerting/json_test.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/alerting/json_test.go) | Tenant serialization/empty slices/resolution title; add end-to-end ownership tests. |
| [alerting/notify.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/alerting/notify.go) | Sequential sinks, no durable retry/signing, HTTP allowed; URL is operator-controlled, not established agent SSRF. L13. |
| [alerting/rules.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/alerting/rules.go) | Validation exists; duplicate names/severity/negative-duration semantics incomplete. Commodity alerting. L13. |
| [alerts_setup.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/alerts_setup.go) | Global sinks, default generic operational alerts, partial env precedence, unjoined evaluator. L07/L13/L16. |
| [build_release.ps1](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/build_release.ps1) | Embedded/logged secrets, request-failure fallback, ldflag validation/version stamping and package naming gaps. V13/L19. |
| [collector/collector.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/collector/collector.go) | Root-volume capacity versus all-device IO, interface double count, failure-to-zero, synchronous writes. L04/L05/L07. |
| [collector/collector_test.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/collector/collector_test.go) | Real cadence/socket checks; no join after cancel, few fault/permission/accuracy fixtures. L05/L07. |
| [collector/delta.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/collector/delta.go) | Reset suppression positive; zero still needs reset/unknown quality metadata. L05. |
| [collector/delta_test.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/collector/delta_test.go) | Good pure reset/delta arithmetic; add collector failure/recovery and export-policy parity. |
| [collector/iface.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/collector/iface.go) | Route lookup/cache better than traffic-only guess; not Internet/VPC/billing classifier. L04. |
| [collector/iface_test.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/collector/iface_test.go) | Real interface/name checks; lacks deterministic route/VPN/shared-NIC fixtures. L04. |
| [collector/process.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/collector/process.go) | Delta CPU and PID+creation identity positive; top-N/short-lived blind spots, creation identity not persisted. L05. |
| [collector/process_test.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/collector/process_test.go) | Useful CPU arithmetic/PID tests; lacks full inventory/permission/platform collection fixtures. |
| [collector/usertracker.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/collector/usertracker.go) | Ports/state filtering/mutex positive; IPs and sockets are not people; missing error quality state. L04. |
| [collector/usertracker_internal_test.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/collector/usertracker_internal_test.go) | Teardown/dedup/exclusion/expiry tests; do not establish application-session accuracy. L04. |
| [dashboard/dashboard.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/dashboard/dashboard.go) | Embedded FS limits serving exposure; missing security headers/session/asset integration. V12. |
| [dashboard/dashboard_test.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/dashboard/dashboard_test.go) | HTML snippet tests, not browser behavior/security or formula parity. L15/L17. |
| [dashboard/static/chart.umd.min.js](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/dashboard/static/chart.umd.min.js) | PARTIAL vendor inspection: Chart.js 4.4.0/color 0.3.2 MIT notices, initial merge guards visible. Truncated; no full vendor or upstream byte-identity audit. Unused by UI. V12/V21. |
| [dashboard/static/index.html](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/dashboard/static/index.html) | URL/localStorage secrets, remote JS, inherited-map crash, duplicate sizing, stale queries and incomplete logout. V12/L03/L15. |
| [export/export.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/export/export.go) | CSV formulas, global CLI scope, statistical flaws, missing final flush/close error propagation. V09/L02/L03/L09. |
| [export/export_test.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/export/export_test.go) | Single/multi-server happy paths; no XSS/formula, retention, tenant, variable-capacity or UI parity coverage. |
| [export/report.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/export/report.go) | Unescaped names, global query, no immutable assessment/provenance/quality gate. V08/L09. |
| [go.mod](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/go.mod) | Declared graph/minimum toolchain reviewed; x/text version match and CI drift. V21/L17. |
| [go.sum](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/go.sum) | Checksum inventory reviewed, not verified by Go; historical entries not counted as selected versions. V21. |
| [install.ps1](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/install.ps1) | Agent dispatch prevents service registration; user/service config mismatch, key arguments, missing token DACL/integrity check. L01/V14/V20. |
| [install.sh](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/install.sh) | Root local-binary install; wrong config path, requires key, incomplete modes/permissions/verification. L01/L16/V20. |
| [install_user.ps1](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/install_user.ps1) | Hidden user startup, no health validation, standalone exposure, broad process-name stop, stale paths/messages. L16/L19. |
| [main.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/main.go) | Bootstrap resurrection/raw tenant keys, dispatch/config bugs, global exports, origin/identity mismatch and shutdown gaps. V02/V11/L01/L09. |
| [render.yaml](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/render.yaml) | Development hosting and legacy key bootstrap, not a Huawei/HA/residency architecture. V01/L19. |
| [retention/retention.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/retention/retention.go) | Cross-tenant/server destructive grouping, lost peaks/capacity, averaged counters/PIDs, uncancelable scheduling. V07/L02/L07. |
| [retention/retention_test.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/retention/retention_test.go) | Anonymous single-dataset metrics test; misses process isolation and evidence preservation. V07/L02. |
| [server/alerts.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/server/alerts.go) | Read auth/rate/tenant output filter positive; upstream state collisions remain. V06. |
| [server/auth.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/server/auth.go) | Registry validation positive; all=>admin, ignored kind/server identity, pre-rate quota and cache gaps. V01/V03/V04/V17/V19. |
| [server/auth_test.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/server/auth_test.go) | Good unknown-key/tenant/body/CORS tests; legacy broad fixtures and fresh-server revoke hide privilege/cache gaps. |
| [server/enroll.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/server/enroll.go) | Body cap/strict enrollment and atomic use count positive; proxy trust, read DELETE, secret logging, non-atomic rotation. V05/V10/V13/V18. |
| [server/enroll_test.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/server/enroll_test.go) | Modern scopes/session handshake tested; missing warm-cache revoke, identity abuse, concurrency and proxy TLS cases. |
| [server/multiserver_test.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/server/multiserver_test.go) | Server filters/local TCP observation, not adversarial tenancy or browser execution. |
| [server/quota.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/server/quota.go) | Mutex protects local reservations; accounting/order/init/persistence wrong for SaaS. V17/L08. |
| [server/quota_test.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/server/quota_test.go) | Reset/tenant/arithmetic tests; no actual concurrent/multi-replica/full middleware tests. |
| [server/ratelimit.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/server/ratelimit.go) | Bucket/sweep positive; false batching assumption and shared reads/writes budget. V15/L08. |
| [server/ratelimit_test.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/server/ratelimit_test.go) | Bucket-level burst/refill/isolation/sweep; no pre-auth endpoint enforcement verification. V15. |
| [server/server.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/server/server.go) | Tenant override/timeouts/caps positive; server binding, validation, query bounds, CSV filter, health/viewer/TLS gaps. |
| [server/server_test.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/server/server_test.go) | API/health basics; add stale fleet, proxy metrics auth, deadlines/cancel, methods and load tests. |
| [server/session.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/server/session.go) | HttpOnly/SameSite support exists; raw bearer cookie, optional Secure, no throttle/server-session invalidation; unused UI. V10/V12/V15. |
| [server_id_flag.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/server_id_flag.go) | Writes identity during flag parsing; validator differs from enrollment, non-atomic transitions. L11. |
| [storage/apikeys.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/storage/apikeys.go) | CSPRNG/hash lookup positive; legacy defaults, normalization, reviving upsert and non-transactional identity lifecycle. |
| [storage/apikeys_test.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/storage/apikeys_test.go) | SQLite lifecycle checks; atomic consumption test is serial, no byte-exact tokens/restart revival/PG races. |
| [storage/db.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/storage/db.go) | Bound SQL positive; unsafe scope/settings/permissions, ignored ALTER failures, no event ID. V16/L09/L10. |
| [storage/db_test.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/storage/db_test.go) | Roundtrip/small benchmarks; no tenant+server, overflow, migration-failure or fleet-scale fixtures. |
| [storage/postgres.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/storage/postgres.go) | Bound SQL/pool positive; background contexts, TLS policy, ALTER errors, uint64 casts, SERIAL IDs. V10/V16/L10. |
| [storage/postgres_test.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/storage/postgres_test.go) | No successful PostgreSQL CRUD/auth/enrollment/isolation operation tested. L17. |
| [storage/store.go](https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/storage/store.go) | Empty-tenant global access; monolithic read/write contract lacks context/end/server/paging/assessment scope. L09/L10. |

---

## 3. Feature Improvements

### Product direction: sell a reviewed migration decision, not a metric chart

The product should answer: **“What should we migrate, to which supported target, at what cost/risk, based on which evidence, and how will we prove the outcome?”** More dashboards do not answer that question. A commercial deliverable needs traceable decisions, uncertainty, review and approval.

### Existing versus missing: do not rebuild what is already here

| Capability | Present now | Needed next |
|---|---|---|
| CSV | CLI and tenant-scoped HTTP CSV. | Correct server filters, formula-safe option, rectangular machine schema, manifest, run/end scoping, streaming and integrity. |
| JSON | Metrics/process/agent/admin JSON APIs. | Versioned assessment-result schema with stable IDs, evidence, findings, confidence and recommendations. |
| HTML/PDF | Static HTML and browser print-to-PDF. | Safe tenant-specific branded report, optional controlled PDF worker, immutable versions and recipient/access controls. |
| Scheduling | Collector ticker, hourly retention, limited foreground run/export timer. | Finite assessment campaigns, timezone, start/end, pause/resume, reboot persistence, exclusions and missed-run state. |
| Multi-tenancy | Tenant fields, credential mapping, some filtered reads. | Isolation through compute/rollups/exports/jobs/cache/notifications/object storage, plus DB defense in depth. |
| RBAC | Coarse read/ingest/admin/all scopes. | Platform versus tenant roles, project permissions, named humans, OIDC/SSO and tested least privilege. |
| Audit logging | Ad hoc logs and key last-seen. | Durable redacted actor/action/target audit trail, export access and policy/approval history. |
| Resilience | Bounded disk spool and retry classification. | Race-free queue, event deduplication, batching, loss accounting and origin binding. |
| Sizing | Fixed-margin peak heuristics. | Explainable versioned constraints, confidence and real target catalog. |

### Feature backlog

Effort is relative: **S** contained; **M** several coordinated components; **L** substantial subsystem/integration. These are planning judgments, not delivery estimates.

| ID / priority | Feature | Concrete deliverable and acceptance criterion | Effort / prerequisite |
|---|---|---|---|
| F01 / P0 | First-class assessment runs | Tenant/project/run IDs, explicit asset scope, start/end/timezone, expected inventory, collector/policy versions, owner, consent/status. Finalized runs cannot silently change under retention. | M; V07/L09 |
| F02 / P0 | Evidence sufficiency/quality | Per-host/metric covered duration, gaps, clock skew, permissions, resets, cadence and business-cycle coverage. Insufficient evidence blocks confident sizing and appears in every export. | M; typed collection schema |
| F03 / P0 | Canonical assessment engine | Pure deterministic computation shared by API/UI/reports. Golden fixtures yield identical numbers across SQLite/PG and export formats. | M; L02/L03 |
| F04 / P0 | Secure SaaS identity/isolation | Platform admin, partner operator, tenant admin, assessor, viewer and agent privileges; OIDC humans and scoped machines. Isolation proven across jobs/DB/cache/report objects. | L; V01-V07/V19 |
| F05 / P1 | Run-to-run delta/diff | Match assets/workloads across baseline/pre-cutover/post-cutover. Show absolute/percentage changes, additions/removals, capacity shifts, evidence quality and confidence. Flag incomparable windows. | M; F01-F03 |
| F06 / P1 | Assessment JSON/CSV bundles | OpenAPI/result schema, separate rectangular metrics/inventory/findings CSVs, manifest/hashes. Round-trip IDs/units/timezones; spreadsheet-safe mode tested. | M; V08/V09 |
| F07 / P1 | Finite scheduled campaigns | Representative period, business hours/maintenance exclusions, stop date, expected hosts and completeness gate. Restart-safe jobs with no duplicates. | M; F01, durable jobs |
| F08 / P1 | Huawei catalog adapter | Versioned region/AZ/SKU availability, architecture/OS, CPU/RAM, disk capacity/IOPS/throughput, network constraints, price provenance. Each result cites a catalog snapshot. | L; F02/F03 |
| F09 / P1 | Scenario and TCO comparison | Conservative/balanced/cost-optimized scenarios; visible target utilization/headroom, currency/billing/tax/licensing/HA/storage/backup/network assumptions. Missing prices stay unknown. | L; F08 |
| F10 / P1 | Migration blockers | OS/architecture/boot/filesystem/driver/volume and approved app metadata. Versioned checks return pass/fail/unknown, evidence, applicability and manual validation steps. | L; F02 |
| F11 / P1 | Dependency evidence | Approved connection/service collection joined with CMDB/owner input. Distinguish observed/inferred/declared/confirmed edges, confidence and last observation. | L; identity/privacy/inventory |
| F12 / P1 | Cutover readiness/validation | Workload acceptance criteria, latency/errors/business checks, dependencies, rollback gates and sign-off. Post-migration diff explains deviations, not just CPU charts. | L; F05/F10/F11 |
| F13 / P1 | API-first embedding | Versioned tenant/project/run APIs, async report jobs, OpenAPI/SDKs, idempotency/pagination/events. Partner portal can run assessments without UI scraping. | M/L; F01/F04 |
| F14 / P1 | Signed portable evidence | Encrypted signed imports/exports with version/quality manifest, selective redaction and disconnected collection. Import validates ownership/size/schema/integrity. | L; F01/F02/V16 |
| F15 / P1 | Human review and approvals | Notes, recommendation overrides with reasons, version/sign-off history and expiring report access. Every recommendation has accountable rationale. | M; F04 |
| F16 / P2 | Existing telemetry adapters | Approved Prometheus/Zabbix/Datadog/CMDB input, normalized identity/units/quality. Native-agent and adapter fixtures demonstrate parity. | L; F03/F13 |
| F17 / P2 | Partner customer workspace | Branded templates, project pipeline, reusable policies, entitlements and usage accounting. Monetize outcomes/workflow rather than sample volume by default. | L; stable core/F04 |
| F18 / P2 | Fleet lifecycle/support | Signed universal packages, enrollment/capability/version status, bounded redacted diagnostics, staged upgrades/rollback. No hidden remote shell. | M/L; V20/L01 |
| F19 / P2 | Sustainability opportunities | Optional efficiency scenarios with sourced assumptions and uncertainty; never infer carbon savings from CPU alone. | M; mature evidence/catalog |

### Minimum credible customer report

1. **Scope and exclusions:** client/project/run, workloads/assets, owner, period/timezone, missing systems, permissions and blind spots.
2. **Evidence quality:** expected versus observed coverage, business-cycle representation, gaps/resets, unsupported metrics, resolution and provenance.
3. **Inventory/dependencies:** per-volume resources, workload groups, criticality, owners, observed/inferred relationships and confidence.
4. **Observed behavior:** duration-weighted distributions and percentiles where justified, extrema, business/non-business windows, evidence links.
5. **Target scenarios:** supported regional Huawei SKUs, disk/network/HA constraints, pricing snapshot, headroom assumptions and excluded costs.
6. **Risks/blockers:** compatibility, data/boot constraints, dependency uncertainty, licensing/manual checks and remediation ownership.
7. **Migration/validation:** waves, prerequisites, expected interruption, rollback gates, post-cutover acceptance and baseline diff.
8. **Decision record:** selected/rejected alternatives, human overrides/approvals, engine/schema/catalog/report version, bundle hash and retention policy.

### Recommended SaaS architecture

```text
Native agent / approved telemetry adapters / signed evidence imports
  -> authenticated, bounded, idempotent ingestion
  -> tenant + project + run-scoped evidence storage
  -> deterministic, versioned assessment engine
  -> inventory / dependencies / compatibility / scenario services
  -> immutable result JSON + signed evidence manifest
  -> safe report + partner API + review/sign-off
  -> Huawei MgC handoff where supported and appropriate
```

Use PostgreSQL as the controlled SaaS system of record after real isolation/backend testing. Keep SQLite for bounded local assessment projects, not a transparent substitute with different statistics. Store large evidence/report objects with authorized access, encryption, retention, expiring links and access audit.

For Huawei deployment, evaluate managed PostgreSQL/RDS, OBS evidence storage, KMS encryption/signing integration, IAM/agency least privilege, private networking and an appropriate compute service. These are **proposed components, not current implementations**. Confirm regional availability, service/API contracts and pricing before promising a bundle.

Separate product operations from customer evidence: use an existing observability stack to monitor the Hub. Integration is sensible; recreating an entire monitoring suite is not.

### Security and correctness regression matrix

The existing suite should be extended with these release-blocking cases, not merely more happy-path coverage:

| Boundary | Required regression |
|---|---|
| Credentials | Every kind/scope against each route/method; legacy cannot administer; enrollment code cannot ingest; read token cannot revoke. |
| Identity | Agent A cannot submit B identity; duplicate IDs across tenants remain independent in alerts/exports/retention. |
| Lifecycle | Warm-cache expiry/revoke/rotation; restart does not resurrect CSV/configured keys; atomic enroll under concurrent/failing requests. |
| Transport | HTTP rejection, invalid cert rejection, untrusted forwarded headers, cross-origin redirect, changed Hub destination, DB TLS policy. |
| Ingest | Future/old/duplicate/overflow/oversized/unknown/trailing-data payloads, semantic invariants, lost ACK and event dedup. |
| Rendering | Multi-server HTML injection, spreadsheet formulas, inherited JS keys, logout/URL token removal, no third-party asset dependency. |
| Storage | Real SQLite/PG roundtrip, migrations under failure/concurrency, range/tenant/server query parity, retained extrema/weights/ownership. |
| Agent | Append during drain, full disk, crash/restart, backlog eviction, cancellation, two processes, quarantine versus loss. |
| Installation | Disposable Windows/Linux service and reboot lifecycle, config lookup under service identity, artifact and ACL checks. |
| Scale | Invalid-key/session flood, cold health cache, wide queries/exports, noisy tenant, fleet retry storm, quota fairness. |
| Assessment | Variable capacity, mixed resolution, missing evidence, non-simultaneous peaks, identical UI/API/report output, comparable diffs. |
| Operations | Backup restoration, tenant deletion, report-link expiry, audit completeness and incident reconstruction. |

### Delivery sequence and go/no-go gates

| Stage | Work | Gate |
|---|---|---|
| Contain | Restrict exposure, rotate potentially live exposed keys, remove escalation/auto-import, fix read DELETE, stop unsafe retention and HTML execution. | No critical isolation/artifact-execution path; verified restore point and operator-approved rollout. No such changes were performed by this audit. |
| Correctness | Identity, install/config/lifecycle, spool/idempotency, validation, backend parity and canonical sizing. | No lost/duplicated accepted events in fault tests; equivalent evidence produces equivalent results. |
| Credible assessments | F01-F07, quality gate, safe branded deliverable, immutable versions. | A reviewer can reproduce each number and see limitations without inspecting source. |
| Huawei value | Catalog/constraints/scenarios/blockers and supported MgC handoff. | Real supported regional targets; price/provenance/uncertainty visible. |
| Commercial pilot | Use customers' existing telemetry; compare with MgC alone. | Demonstrable reduction in effort to a reviewed decision, better assurance/validation and willingness to pay. |

Do not launch public shared tenancy while critical isolation/data-loss flaws remain. Require real SQLite/PG security tests, disposable OS lifecycle tests, recorded selected-module/binary scans, signed artifacts, backup/restore and tenant deletion drills, and deterministic quality-gated reports.

---

## 4. Differentiation Risk

### Direct answer

**Yes: most current core capabilities reimplement things Prometheus + Grafana, Datadog, or Zabbix already do more comprehensively.** Lightweight packaging is useful, but not enough to offset weaker security, fewer integrations, uncertain measurements and inconsistent sizing.

The UI describes a “Real-time system monitoring dashboard” and prioritizes CPU, memory, disk, network, IOPS, users, charts and processes. That is commodity monitoring. Calling it pre-migration without a validated assessment workflow does not change its category.

These are capability/product-fit judgments, not performance benchmarks. Paid/managed features are distinguished from basic OSS components.

### Core capability comparison

| Capability / source | Do established tools do this better, and are we reimplementing it? | Decision / meaningful differentiation |
|---|---|---|
| CPU/RAM/disk/network, [collector][collector] | **Yes, largely commodity.** Exporters, Zabbix and Datadog have broader dimensions/platforms/integrations. | Minimal collector for unmanaged/disconnected sites; adapters rather than a metrics breadth race. |
| Top processes, [process.go][process] | **Yes.** Periodic top-20 snapshots are weaker than mature process monitoring. | Build approved app inventory/ownership/compatibility evidence, not another leaderboard. |
| Multi-server charts/filters, [dashboard][ui] | **Yes, direct Grafana overlap.** | Freeze generic visualization expansion; surface scope/quality/blockers/scenarios/evidence/sign-off. |
| Historical metric storage/querying, [storage][store] | **Yes.** Hand-built wide-row SQL lacks mature time-series query/aggregation features. | Persist assessment evidence/results; integrate continuous monitoring stores. |
| Retention/downsampling, [retention][retention] | **Yes, and this implementation is unsafe.** | Preserve assessment statistics/provenance; do not build a generic TSDB compactor. |
| Threshold alerts/webhooks, [alerting][evaluator] | **Yes, direct Alertmanager/Zabbix/Datadog territory.** | Integrate operations; keep assessment incomplete/ready/blocker-change business events. |
| Agent/Hub buffered transport, [agent][agent] | **Yes for general telemetry.** Push and buffering are established. | Prove constrained-site enrollment, consent, bounded overhead and finite campaigns, not push versus pull. |
| Concurrent users, [usertracker][users] | **True sessions: application/APM instrumentation does better.** IP counting is not a unique usage metric. | Honest proxy measurement, application acceptance integrations. |
| External/internal traffic, [iface.go][iface] | **Yes for mature network observability; current heuristic is weaker.** | Validated transfer/egress scenarios with destination evidence. |
| Health and /metrics, [server][server] | **Commodity but appropriate integration.** Not the harmful type of cloning. | Keep reliable self-observability and use existing operational tooling. |
| Tenant auth/enrollment, [auth][auth] | **Table stakes, not differentiation.** Prometheus alone is not a complete SaaS identity/control plane. | Secure tenant/project workflow with established identity systems. |
| CSV/JSON exports, [exports][exports]/[API][server] | **Yes, ubiquitous basics.** | Versioned traceable assessment bundle, not charts in another format. |
| HTML/PDF report, [report.go][html-report] | **Generic reporting: yes.** Grafana Enterprise has scheduled PDF/CSV; not a free OSS-equivalent claim. | Own migration decisions, assumptions, blockers, human approval and outcome validation. |
| Fixed-margin sizing, [export.go][exports] | **Basic utilization sizing: yes.** Grafana Cloud has resource-efficiency features; Huawei MgC offers performance-based recommendations. | Constraint/policy/confidence-driven alternatives, not peak x 2. |
| Single binary/local mode, [main.go][main] | **Partial practical benefit, not unique.** Many agents are lightweight. | A reliable portable whole assessment workflow could reduce setup; prove least privilege, no CDN, safe evidence transfer/removal. |
| Run-to-run/pre-post diff | **Not implemented.** Monitoring compares time ranges; workload-aware migration acceptance is more specific. | Matched identity, comparable evidence, SLA/business checks and rollback gates. |
| Dependency-aware waves | **Not implemented; maps already exist in APM/MgC.** | Reuse observed maps; add reviewed constraints/wave decisions and uncertainty. |
| Huawei bundle/recommendations | **Not implemented, and MgC exists already.** | Complement native services with partner workflow, evidence assurance and post-migration proof. |

### The more important competitor: Huawei Migration Center

[Huawei MgC's overview](https://www.huaweicloud.com/intl/en-us/product/mgc.html) includes discovery, dependency visualization, recommendations and migration management. Its [functions documentation](https://support.huaweicloud.com/eu/productdesc-mgc/mgc_01_0004.html) explicitly covers TCO, application dependencies, assessment and workflow templates.

Its [recommendation workflow](https://support.huaweicloud.com/eu/qs-mgc/mgc_02_0009.html) includes performance-based sizing, cost/performance preferences and regional target configuration. [Large-scale migration guidance](https://support.huaweicloud.com/intl/en-us/usermanual-mgc/mgc_03_31485.html) connects assessment with batch migration and disk/target constraints.

Adding Huawei SKUs, dependency charts and TCO therefore does **not** automatically make this unique. It risks a weaker MgC frontend as well as a weaker monitoring frontend. Validate a real partner/client workflow gap in the applicable region, rather than charging simply for an existing native capability.

### Defensible positioning hypothesis

> **w-monitor turns heterogeneous infrastructure evidence into a reviewable, reproducible migration decision and proves the outcome after cutover, with Huawei-ready handoff and customer-controlled data.**

The likely value is the combination of:

- **Evidence independence:** existing monitoring, approved collector, CMDB and signed disconnected bundles.
- **Quality:** clearly mark known/missing/inferred/unrepresentative information and refuse false precision.
- **Traceability:** source evidence, policy/catalog versions, constraints, overrides and approvals behind every choice.
- **Partner workflow:** controlled multi-client projects, branded deliverables, accountable review/sign-off.
- **Closed-loop validation:** pre/post evidence tied to performance/cost assumptions and acceptance/rollback gates.

These are hypotheses, not claims no competitor does them. Test against MgC, consulting workflows and assessment vendors before major investment.

### Stop, keep, build

**Stop expanding:** general-purpose charts, generic alert editors, proprietary metric query languages, broad logs/APM ingestion, endless process panels and custom TSDB features. They consume engineering without improving migration decisions.

**Keep and harden:** small collector, outbound delivery, bounded spool, simple API, portable safe export and self-observability. Remove dangerous compatibility paths and measure real installation/operational burden.

**Build deliberately:** assessment runs, quality gate, canonical engine, provenance, diffs, constrained regional scenarios, application blockers, approval and post-cutover validation.

### Commercial validation questions

| Question | Evidence needed |
|---|---|
| Why this instead of MgC alone? | Specific missing/slow/hard-to-audit workflow across sources or disconnected sites. |
| Can it avoid a second permanent agent? | Adapter/import success with documented units, identity and quality parity. |
| More trustworthy than a spreadsheet? | Independent reproduction and visible uncertainty/overrides. |
| Does it change the migration decision? | Blocker discovery, justified target choice, less reconciliation or clearer acceptance. |
| Does it prove migration success? | Comparable evidence tied to workload/business goals, not just CPU. |
| Will anyone pay for the gap? | Paid pilot or procurement feedback about workflow/evidence/validation, not compliments about charts. |

**Bottom line:** Salvage the acquisition layer and early report prototype. Build a migration assessment and assurance product that consumes monitoring, not a smaller competitor to the monitoring tools themselves.

### External sources used

- [Prometheus comparison/monitoring scope](https://prometheus.io/docs/introduction/comparison/)
- [Prometheus instrumentation guidance](https://prometheus.io/docs/practices/instrumentation/)
- [Datadog infrastructure capabilities](https://www.datadoghq.com/product/infrastructure-monitoring/)
- [Zabbix current documentation](https://www.zabbix.com/documentation/current/en/manual/installation/requirements)
- [Grafana Enterprise scheduled reporting](https://grafana.com/docs/grafana/latest/enterprise/reporting/)
- [Grafana reporting API and edition limitation](https://grafana.com/docs/grafana/latest/developers/http_api/reporting/)
- [Grafana Cloud Kubernetes efficiency](https://grafana.com/docs/grafana-cloud/observe-and-act/monitor-infrastructure/kubernetes-monitoring/optimize-resource-usage/)
- [Huawei MgC functions](https://support.huaweicloud.com/eu/productdesc-mgc/mgc_01_0004.html)
- [Huawei target recommendations](https://support.huaweicloud.com/eu/qs-mgc/mgc_02_0009.html)
- [Huawei migration risk/constraint examples](https://support.huaweicloud.com/intl/en-us/bestpractice-mgc/mgc_06_0010.html)
- [x/text GO-2026-5970](https://pkg.go.dev/vuln/GO-2026-5970)
- [Go hostname advisory GO-2026-5026](https://pkg.go.dev/vuln/GO-2026-5026)

Documentation varies by region/edition. Current commercial terms, service availability, API integration access, deployed settings and runtime vulnerability reachability need follow-up validation.

<!-- Immutable source references -->
[admin-guide]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/ADMIN_GUIDE.md
[agent]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/agent/agent.go
[agents-md]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/AGENTS.md
[alert-api]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/server/alerts.go
[alert-model]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/alerting/alert.go
[alerts-setup]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/alerts_setup.go
[auth]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/server/auth.go
[builder]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/build_release.ps1
[changelog]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/CHANGELOG.md
[ci]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/.github/workflows/ci.yml
[collector]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/collector/collector.go
[cred-model]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/agent/credstore.go
[cred-test]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/agent/credstore_test.go
[cred-unix]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/agent/credstore_unix.go
[cred-win]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/agent/credstore_windows.go
[dash-test]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/dashboard/dashboard_test.go
[enroll-agent]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/agent/enroll.go
[enroll-server]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/server/enroll.go
[evaluator]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/alerting/evaluator.go
[exports]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/export/export.go
[gomod]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/go.mod
[gosum]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/go.sum
[html-report]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/export/report.go
[iface]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/collector/iface.go
[install-linux]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/install.sh
[install-user]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/install_user.ps1
[install-win]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/install.ps1
[keys]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/storage/apikeys.go
[main]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/main.go
[notify]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/alerting/notify.go
[pg-test]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/storage/postgres_test.go
[postgres]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/storage/postgres.go
[process]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/collector/process.go
[quota]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/server/quota.go
[ratelimit]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/server/ratelimit.go
[release-ci]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/.github/workflows/release.yml
[render]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/render.yaml
[retention]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/retention/retention.go
[rules]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/alerting/rules.go
[server]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/server/server.go
[server-id]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/server_id_flag.go
[session]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/server/session.go
[spool]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/agent/spool.go
[sqlite]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/storage/db.go
[store]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/storage/store.go
[ui]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/dashboard/static/index.html
[user-guide]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/USER_GUIDE.md
[users]: https://github.com/anakngbuddha/w-monitor/blob/02ef77cdc3b1c1cd27ae618eeeff93c761c062a9/collector/usertracker.go
