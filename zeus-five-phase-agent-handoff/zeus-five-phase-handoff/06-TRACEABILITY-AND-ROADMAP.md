# Traceability, scope and release ledger

This is an implementation ledger, not a claim that the original audit has been remediated. There are **40 execution tickets** across exactly five phases: 10 + 8 + 7 + 7 + 8. Any large ticket is split into reviewed sub-PRs; do not ask a coding agent to implement a whole phase at once.

## Five-phase overview

| Phase | Tickets | Shippable milestone | Hard exit gate |
|---|---:|---|---|
| 1. Secure and stabilize | 10 | Isolated tests, typed identities, scoped storage, safe transport/rendering, reliable delivery | G1: real security/lifecycle/fault regressions, no critical trust/data-loss path |
| 2. Evidence and inventory | 8 | Finite campaign, repaired metrics, full bounded inventory, opt-in safe config references | G2: consented Windows/Linux evidence with explicit quality and backend parity |
| 3. Graph and deterministic assessment | 7 | Observed graph, separated criticality, one sizing engine, reproducible no-AI report | G3: every result/edge/score reproducible and limitations visible |
| 4. Constrained AI and reviewed report | 7 | One-run AI job, durable quotas/cache, strict validation, review, HTML plus new DOCX | G4: privacy/budget/failure tests and AI-disabled completion |
| 5. Huawei assurance and pilot | 8 | Sourced scenarios/TCO, reviewed waves, handoff, pre/post proof, trusted packaging | G5: deployment/security/restore gates plus measured customer value |

These are substantial milestones, not five quick prompts. No calendar estimate is defensible without actual engineer capacity, supported OS matrix, target fleet, cloud region and integration access. Grok 4.6 is the user's requested coding agent; this plan does not assert benchmarked ability or authorize it to bypass human gates.

## Expanded feature register

Retain original F01-F19; new IDs extend rather than overwrite them.

| ID | Addition / revised deliverable | Primary tickets | Completion evidence |
|---|---|---|---|
| F20 | Application inventory: processes/services/listeners/software/runtimes/IIS, capability-aware | P2.05-P2.06 | Native platform fixtures, bounded complete/partial paging and PID identity |
| F21 | Consent-based config reference discovery and lifecycle flags | P2.07 | No raw credentials in any sink; sourced versioned lifecycle decisions |
| F22 | Metadata connection snapshots and evidence-preserving graph | P3.01-P3.02, P3.07 | NAT/IP/PID/tenant ambiguity and repeated rebuild tests |
| F23 | Explainable technical importance, separate business impact/operational risk | P3.03 | Raw signal/weight provenance, golden score and unknown-input tests |
| F24 | Workload-aware permitted migration strategy taxonomy | P3.05 | Eight defined states, evidence prerequisites and no forced retirement/refactor |
| F25 | Optional one-run AI classification/narrative/criticality explanation | P4.01-P4.05 | Strict schema/semantics, cited evidence, complete asset coverage, adversarial eval |
| F26 | Durable explicit-trigger jobs, shared quotas, content-addressed cache | P4.04/P4.06 | One normal dispatch, cross-replica dedup, bounded retry and crash ambiguity |
| F27 | AI consent/redaction/provider-routing controls and deterministic fallback | P4.02-P4.06 | Secrets absent, no unapproved egress/paid fallback, complete AI-disabled report |
| F28 | New DOCX exporter and reviewed Migration Strategy section | P4.07 | Same canonical facts, literal untrusted text, no active external relationships |
| F29 | Reviewed dependency-aware migration wave plans | P5.03 | Cycles/prerequisites/windows/owners and explicit readiness/rollback gates |
| F30 | MgC comparison and evidence-quality/analyst-effort scorecard | P5.08 | Measured representative engagements, published failures and honest claim scope |

## Original security-finding ownership

| Findings | Primary implementation and verification |
|---|---|
| V01, V02, V03, V04, V05 | P1.03 typed identity/permissions/bootstrap; P1.04 lifecycle; full route matrix |
| V06 | P1.05 scoped selection/state; verify graph/result scope again at G3 |
| V07 | P1.02 containment, P2.03 actual repair, G2 parity/retention tests |
| V08, V09 | P1.02 render/CSV safety; repeat for AI/DOCX at P4.07 |
| V10, V11 | P1.06 TLS/origin/proxy/DB boundary; P4.03 inference boundary |
| V12 | P1.07 sessions/assets/browser tests |
| V13 | P1.02 operator containment guidance, P1.06 redaction, P1.08 secret-free install; P5.07 signed universal distribution |
| V14 | P1.06 explicit DACL; P1.08/P5.07 native install/upgrade verification |
| V15 | P1.05 query/context limits, P1.07 login, P1.10 fairness; scale verification P5.08 |
| V16 | P1.09 strict DTOs/idempotency; P2.01 contract/backend parity |
| V17 | P1.10 shared accepted-data accounting; independent AI accounting P4.04 |
| V18, V19 | P1.04 transactions/ownership/cache; G1 concurrency and warmed-cache tests |
| V20 | P1.06 file boundaries/P1.08 install hygiene; P5.07 signed artifacts/installer verification and operations |
| V21 | P1.01 supported patched toolchain/scanning; P5.07 release binary/module gates; source-version matches not assumed exploitable |
| V22 | P1.01 before any broad filesystem test suite |

## Original design-weakness ownership

| Findings | Primary tickets |
|---|---|
| L01 | P1.08 native service lifecycle/config paths |
| L02, L03 | P2.03 typed retention and P3.04 canonical sizing/parity |
| L04, L05 | P2.04 honest scopes/quality, P2.05-P2.07 inventory; P3.01 connection limitations |
| L06, L07 | P1.09 immutable spool/async delivery; P1.08 joined lifecycle |
| L08 | P1.10 budgets, P5.08 measured fleet envelope |
| L09, L10, L11 | P1.03-P1.05 typed identities/scope, P2.01 migrations, P2.02 assessment assignments |
| L12 | P1.10 health separation, P2.08 campaign completeness |
| L13 | P1.05 tenant/freshness fixes and P1.10 state bounds; de-scope generic alert expansion and integrate existing operations tooling in P5.07 |
| L14 | P1.10 audit foundations; P2.07 consent; P4.06/P5.06 review/access history; P5.07 retention/deletion |
| L15 | P1.07 browser correctness, P3.04 canonical results, P4.07 export parity |
| L16 | P1.08 strict configuration and secure defaults |
| L17 | P1.01/P1.10 baseline/regressions; P2.01 real backends; P5.07 same-commit signed release gates |
| L18 | P1.02 remove incorrect claims; every ticket updates only verified behavior; final claim review P5.08 |
| L19 | P1.08 packaging paths; P5.06 edition boundaries; P5.07 deployment/license/restore; P5.08 support/scale evidence |

## Original feature-backlog disposition

| Original feature | Delivery / explicit boundary |
|---|---|
| F01 runs | P2.02/P2.08; immutable results P3.06 |
| F02 quality | P2.03-P2.04/P2.08/P3.04 |
| F03 canonical engine | P3.04-P3.06 |
| F04 secure identity/isolation | P1.03-P1.07; production OIDC/partner roles P5.06 |
| F05 deltas | P5.04 |
| F06 export bundles | P3.06/P4.07; signed portable package P5.05 |
| F07 finite campaigns | P2.02/P2.08 |
| F08 Huawei catalog | P5.01 |
| F09 scenarios/TCO | P5.02 |
| F10 blockers | P2.07/P3.05/P5.01 |
| F11 dependencies | P3.01-P3.03/P3.07/P5.03 |
| F12 cutover validation | P5.03-P5.04; execution remains with authorized migration tooling |
| F13 API-first | Versioned contracts throughout; bounded async jobs P3.06/P4.04; supported handoff P5.03. Broad SDK/webhook catalog deferred. |
| F14 signed evidence | P5.05 |
| F15 review | P3.03/P3.07/P4.06/P5.06 |
| F16 telemetry adapters | Minimal CMDB/owner import P3.07 and bundle input P5.05. Full Prometheus/Zabbix/Datadog connectors explicitly deferred pending pilot demand. |
| F17 partner workspace | Minimal project/branding/policy/usage P5.06. Advanced billing/reseller automation deferred. |
| F18 fleet lifecycle | P1.08/P5.07; staged manual rollout/rollback and safe diagnostics, not a remote-control system. Autonomous fleet updater deferred. |
| F19 sustainability | Deferred; requires credible workload/energy/region data and sourced uncertainty, not CPU-derived carbon claims. |

Deferred work is retained in the backlog, not silently removed, and is not a hidden sixth phase. Do not claim complete F13/F16/F17/F18/F19 breadth at the end of this five-phase plan. Reprioritize it only after the pilot and a new approved scope.

## Implementation proof register template

Store a row per original finding and ticket: `id | status (open/in_progress/contained/verified/deferred) | source commit | implementation commit | test command | runner/OS/DB/tool versions | actual result | artifact/hash | reviewer | limitations | rollback`.

A feature's presence does not close a vulnerability. A disabled unsafe feature is contained, not repaired. A passing mocked test does not prove a real DB/OS/provider/cloud integration. Historical audit source references remain pinned to the old commit; new closure evidence cites the implementation commit separately.

### Ledger (updated 2026-09-07)

| id | status | source commit | implementation commit | test command | runner | actual result | limitations | rollback |
|---|---|---|---|---|---|---|---|---|
| V22 / P1.01 | verified (this runner) | 02ef77cdc3b1c1cd27ae618eeeff93c761c062a9 | NOT RECORDED (no git) | `go test -count=1 ./internal/fsroot ./agent ./storage` and canary `-run TestCredentialFixtureDoesNotTouchProductionCanary` | Windows amd64, go1.26.5 (tests), go1.26.6 (govulncheck) | PASS; production token/data files unchanged; production paths rejected | Linux native `/etc/wmonitor` tests NOT RUN; full `go test -race ./...` NOT RUN here | revert P1.01 files; no schema |
| V21 / P1.01 | in_progress | same | NOT RECORDED | `govulncheck@v1.1.4 ./...` | go1.26.5: 6 stdlib vulns; go1.26.6: none; `x/text v0.39.0` | GO-2026-5970 not reported after upgrade | CI run on GitHub not observed; binary release gates remain P5.07 | keep `x/text v0.39.0`; CI 1.26.6 |
| L17 / P1.01 | in_progress | same | NOT RECORDED | CI file review + local `go mod verify` / `go list -m all` | CI go-version 1.22→1.26.6; scanners pinned | workflow edited; no GitHub Actions result | no PG/browser/service fixtures; action tags not SHAs | restore old workflows |

| V07 / P1.02 | contained | same | NOT RECORDED | `go test -count=1 ./retention` | Windows amd64 | tenant-a/tenant-b same-hour rows survive `Run`; disk pressure explicit | not V07 closure; no PG purge fixture | revert; do not re-enable downsample |
| V08 / P1.02 | verified (HTML export) | same | NOT RECORDED | `go test ./export -run TestHTMLEscapesHostileHostnames` | Windows amd64 | PASS escaped markup | not a browser/Hub-origin test | revert report.go |
| V09 / P1.02 | verified (string prefix) | same | NOT RECORDED | `go test ./export -run TestSpreadsheetSafeCSVPrefixesFormulas` | Windows amd64 | PASS; Excel/Sheets NOT RUN | document prefix | revert export.go |



## Final product promise

Zeus produces an independently reviewable, reproducible migration assessment and validates agreed post-migration outcomes, with Huawei-ready targets and controlled customer evidence. AI improves explanation and highlights uncertainty; it neither owns the evidence nor authorizes migration. Prove that this reduces effort or risk compared with MgC alone before calling it superior.
