# Phase 3 of 5: dependency evidence and deterministic assessment

**Outcome:** a reproducible assessment without an LLM. **Runtime layers:** Layer 3 and Layer 4, using repaired Layer 1/2. **Entry:** G2 approved. **Exit:** G3. No AI calls; no invented cloud prices or compatibility.

## P3.01: connection snapshot collectors
- Depends on: G2. New `collector/connections/`, connection DTOs and per-platform fixtures.
- Native metadata-only snapshots with protocol/family/endpoints/state/process-start identity, caps, completeness and time; configurable jittered 60-second interval. Use approved ports/exclusions; no packet payloads, arbitrary shell interpolation or host probing.
- Accept: native Windows/Linux IPv4/IPv6, TCP/UDP limitations, permission denied, PID reuse, loopback and short-lived sessions between samples. Lost samples visibly reduce coverage. Collector cannot claim bytes, latency or request counts it did not measure.

## P3.02: deterministic tenant-scoped graph aggregation
- Depends on: P3.01. New `assessment/dependencies/`, observation/edge/endpoint-ownership tables and bounded rebuild job.
- Scope resolution by tenant/project/network zone/time; preserve unknown endpoints, NAT/LB ambiguity and process/workload attribution. Deduplicate two-sided snapshots; store method, first/last seen, observed/expected windows, direction certainty and evidence IDs. Keep observed/inferred/declared/confirmed separate; stale edges remain historical, not current facts.
- Accept: overlapping private IPs, same hostnames across tenants, same-project zones, IP reuse and NAT do not merge unrelated assets. Rebuilding/retrying the same snapshot produces identical sorted edges/hash without duplicate counts. F11.

## P3.03: owner context and criticality signals
- Depends on: P3.02. New `assessment/criticality.go`, owner-context API/UI and policy fixtures.
- Implement the shared contract's versioned technical score, separate operational-risk flags, owner-approved impact/RTO/RPO and independent evidence quality. Preserve raw inputs/weights and complete/partial/null outcomes. Backup capability and recovery verification are distinct; classify no unknown as low risk.
- Accept: golden 74-point example, missing-activity null/partial example, bounds/unknowns, equal-degree determinism; unknown business owner cannot produce high-confidence business criticality. Overrides preserve the original score and reviewer reason. Layer 4, F15 foundations.

## P3.04: canonical sizing and quality engine
- Depends on: P3.03 and P2.03. New `assessment/sizing.go`, `assessment/quality.go`; replace duplicate Go/browser calculation paths incrementally.
- Pure deterministic inputs/outputs, duration-weighted distributions where supported, capacity-change segments, upwards resource/SKU lower-bound rounding, explicit target utilization/headroom policy. Separate simultaneous fleet demand from sum-of-independent-peaks. Unknown/unsupported statistics remain unknown. Catalog matching is a Phase 5 step, not invented here.
- Accept: same fixtures through SQLite/PG/API/UI/export yield identical numbers; original 8-core/100%-peak mismatch disappears under one documented policy. Test mixed resolutions, no data, nonrepresentative windows, memory definitions and counter resets. F02/F03, L02/L03/L15.

## P3.05: compatibility findings and permissible strategies
- Depends on: P3.04. New `assessment/rules/`, versioned rule fixtures and result DTOs.
- Rules return ID/version/applicability/pass-fail-unknown/evidence/remediation. Compute eligible strategy sets, not an unjustified single answer. OS/architecture/boot/filesystem/runtime constraints need sourced checks; unknown cloud-specific rules defer to Phase 5. Retire/repurchase/relocate/refactor have explicit evidence and owner-approval prerequisites; insufficient evidence permits retain/undetermined with rationale.
- Accept: low-utilization critical DB is not retired; runtime inventory alone cannot establish refactor viability; failed backup check cannot be inferred from absent software; unsupported target stays unknown. Every risk flag references evidence/rule. F10.

## P3.06: immutable canonical results and safe deterministic reports
- Depends on: P3.05. New `assessment/result.go`, result/manifest repositories; existing HTML/CSV export and dashboard consumers.
- Persist scope/window, source manifest, rule/engine/graph versions, evidence quality, inventory/dependency summaries, scores and candidates. Freeze result hashes and authorized report versions. Serve one result model to API/UI/HTML/text/CSV; expensive render jobs are bounded and restart-safe. No AI dependency.
- Accept: a reviewer reproduces all numeric values from the frozen evidence/policies; HTML/CSV safety regressions remain green; regeneration after retention is identical except explicitly separate render metadata. Cross-tenant result/download tests pass. F03/F06/F13 foundation.

## P3.07: graph review and curated evidence imports
- Depends on: P3.06. New limited CMDB/owner CSV importer and graph review endpoints/UI.
- Versioned minimal application/asset/association schema, explicit identity resolution preview, bounds and provenance. No arbitrary connector platform yet. Reviewer can confirm/reject/group edges with audit history, view unresolved endpoints and correct workload mappings without editing original observations. Review overlays are versioned inputs to new results.
- Accept: conflicting CMDB/observed edges both remain visible; imported IP/name collisions require explicit resolution; CSV formula/size/schema/foreign-tenant records are rejected or safely rendered; frozen results do not silently change. F11/F15, limited F16.

## G3 exit gate and handoff

Freeze a multi-workload fixture containing both complete and insufficient evidence, and export a readable decision packet with **AI disabled**. Every deterministic number, flag, graph edge and quality claim has an evidence/policy reference; backend/API/UI/report parity and tenant separation pass. A migration assessor validates rule applicability and the score's limitations. Unknown business criticality and unresolved dependencies cannot disappear from the executive view.

Rollback: retain immutable prior engine versions and result snapshots; a new engine bug can fall back to rendering a prior approved result with its original version label, never rebrand it as a new computation. Review changes produce a new result version.

### Copy-paste start prompt

> Read 00-SHARED-CONTRACT.md, this phase file and the G2 handoff. Implement only P3.01. Keep connections metadata-only and preserve denied/unsupported states. Use real native OS tests and synthetic identity fixtures; never substitute cross-compilation for collection tests. Report results and stop before graph aggregation.
