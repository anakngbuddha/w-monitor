# Phase 5 of 5: Huawei scenarios, migration assurance and pilot release

**Outcome:** a Huawei-ready, reviewed decision and outcome-validation product with measured differentiation. **Entry:** G4 approved; no implication that AI is mandatory. **Exit:** G5. Catalog/region/commercial inputs must be real and sourced; no region, account access or migration authority is guessed.

## P5.01: versioned Huawei target catalog and compatibility
- Depends on: G4 and P3.05. New `assessment/catalog/`, reviewed catalog imports, validators and scenario APIs.
- Start with a curated versioned import for the explicitly selected region and resource families; use a live adapter only against verified authorized APIs. Include SKU/architecture/OS/CPU/RAM, storage capacity/IOPS/throughput, network/HA constraints, availability, source URL and retrieval time. Separate advertised availability from actual quota/reservation. Track lifecycle/boot/driver/volume constraints and manual prerequisites.
- Accept: every eligible target cites a catalog snapshot; unsupported region/family/stale availability is unknown or blocked, never fabricated. Boundary/capacity/incompatibility fixtures reject infeasible targets. Schema mocks do not count as proof of live Huawei support. F08/F10.

## P5.02: deterministic scenarios and TCO
- Depends on: P5.01. New `assessment/scenarios/`, pricing schema and cost golden fixtures.
- Conservative/balanced/cost-optimized configurations from canonical sizing and constrained catalog. Code, not AI, calculates quantities/costs with region/currency/billing period/discount/tax/license/support/HA/backup/storage/network assumptions. Separate one-time migration effort from recurring cost. Compare AWS/Azure only if equivalent sourced catalogs/quotes are supplied; otherwise report them not assessed.
- Accept: no missing price becomes zero; no default-route NIC bytes become billed egress; mixed currencies/time horizons and stale rates cannot silently compare. All costs and alternatives reproducible; uncertain inputs produce ranges/sensitivity notes with disclosed assumptions. Freeze new snapshots; cached Phase 4 analysis cannot silently inherit changed prices. F08/F09.

## P5.03: dependency-aware waves and supported MgC handoff
- Depends on: P5.02 and reviewed P3 graph. New `assessment/waves/`, wave constraints/review and export mapper.
- Collapse strongly connected components into reviewed migration groups; respect dependency prerequisites, shared services, freeze windows, maintenance limits, RTO/RPO and app-owner constraints. Unresolved/cyclic conflicts require review, never arbitrary ordering. Each wave contains prechecks, owner, readiness blockers and rollback plan. Use versioned supported MgC import templates/APIs only after verifying access and regional contract; otherwise export a neutral evidence bundle with clearly manual handoff.
- Accept: cyclic apps stay grouped or explicitly reviewed; unmet prerequisites block readiness; no automatic customer changes or migration execution. Validate a handoff against an actual supported template in a test project, or label the connector unavailable and exclude it from the release claim. F11/F12/F13, F29.

## P5.04: pre/post migration assurance and comparable diffs
- Depends on: P5.03. New `assessment/comparison/`, acceptance-check definitions and sign-off view.
- Explicit source-to-target workload/asset mapping; match business windows, measurement methods, load, capacities and quality. Show added/removed assets, absolute/percentage changes and incomparable metrics. Combine infrastructure changes with owner-defined application latency/error/business transaction checks via controlled imported results; missing application checks remain unverified. Define breach/rollback gates, review owners and validation deadlines.
- Accept: mismatched periods cannot claim improvement; absent metric is not zero; baseline zero avoids false percentage math; lower CPU does not prove successful migration. Readiness, acceptance and rollback are recommendations/sign-offs, not automatic cloud actions. F05/F12.

## P5.05: signed portable evidence and controlled disconnected imports
- Depends on: P5.04 and Phase 1 identity controls. New `evidencebundle/`, manifest/version/signing/encryption interfaces and import quarantine.
- Portable bundle includes scoped pseudonymous identities, schemas, source/quality/engine/catalog versions and content hashes. Encrypt to authorized recipient, sign with trusted managed key and record key ID. Validate signature/trust/schema/size/compression ratios/path safety/ownership before import; authorize explicit tenant/project mapping, never trust bundled tenant IDs. Offline provenance differs from authenticated online ingestion.
- Accept: tampering, wrong recipient/signer/tenant, expired or revoked trust, zip traversal/bomb and duplicate import are rejected or quarantined safely. Trust is configured, not granted by an embedded public key. Disconnected collection produces the same canonical findings for equivalent evidence. F14.

## P5.06: named-human identity, partner workflow and audit completeness
- Depends on: P5.05; Phase 4 reviews initially use explicit named local assessors, never anonymous shared tokens.
- Implement production OIDC/SSO with a maintained library; tenant/project role mapping and audience/issuer/session security. Roles: platform admin, partner operator, tenant admin, assessor, viewer and machine agent; partner access requires explicit tenant assignments. Minimal project pipeline, reusable policies/branding, usage records and approval history. Expiring authenticated downloads with access audit; no anonymous bearer-only indefinite report links.
- Accept: wrong issuer/audience/tenant, expired roles and logout/revocation fail; every decision/download/provisioning action has an accountable actor and scoped target. Test cross-tenant job/object/cache access, not just UI hiding. Advanced billing/reseller portal is deferred, not claimed complete. F04/F15, minimal F17.

## P5.07: trusted packaging, deployment and operations
- Depends on: P5.06. Targets: install/build/release workflows, deployment templates, license/NOTICE and operational runbooks.
- Universal signed binaries/manifests verified before elevated install; stable filenames/version/capabilities, SBOM/provenance and same-commit release gates. Bounded redacted diagnostics, explicit consent, staged rollout/rollback, no remote shell. Deploy using verified region-appropriate compute/PostgreSQL/object storage/KMS/private networking choices; do not assume Render config is Huawei infrastructure. Backups/PITR, migration lock, restore procedure, encryption/access/retention/deletion policy and support ownership.
- Accept: tampered install refused; lower-trust users cannot replace binaries/config; actual native OS upgrade/rollback/reboot tests pass. Restore and tenant deletion drills prove no cross-tenant exposure or deleted-data revival. Review dependency licenses and unverified local ACLs. F18, V20/V21, L17/L19.

## P5.08: performance, security and commercial pilot comparison
- Depends on: P5.07 and all prior gates. Targets: load/fault/e2e harness, evidence register and pilot scorecard.
- Measure agreed fleet size/resource envelope including inventory/connection bursts, recovery backlog, multi-tenant isolation, deterministic report compute, AI quota starvation and large payload refusal. Run one end-to-end synthetic/consented dataset through collection, finalization, graph, decisions, optional AI, review, report, handoff and pre/post comparison. Evaluate three consented representative engagements against region-specific MgC baseline/workflow.
- Accept: every earlier regression/release gate has real recorded evidence; benchmark hypotheses are reported as passed/failed, not aspirational claims. A qualified security/migration reviewer approves release; unresolved critical/high risks block public shared tenancy unless actually remediated and verified. Pilot must support the stated analyst-effort/traceability/approval-quality value, or product claims and scope are revised. F30.

## G5 exit gate and launch decision

Produce one reproducible release evidence packet: tested commit/build/SBOM/signatures; V/L closure evidence and remaining risks; both DB/OS/browser results; scoped evidence/result/report manifests; reviewed privacy/routing terms; real regional catalog/price sources; supported handoff proof or explicit exclusion; backup/restore/deletion results; measured fleet/agent overhead; AI-disabled failure paths; and signed pilot scorecard. Do not market unimplemented connectors, paid-model guarantees, commercial edition features or universal superiority over MgC.

Rollback: retain supported old signed packages and schema compatibility matrix; feature flags disable new catalog/AI/import features without corrupting frozen results. Operator-approved restore/forward repair for destructive changes; no downgrade that reinstates old credential escalation or unsafe retention.

### Copy-paste start prompt

> Read 00-SHARED-CONTRACT.md, this phase file and the G4 handoff. Implement only P5.01. Inspect available verified catalog sources; if target region/source/API access is not supplied, implement and test the schema/curated import with clearly synthetic fixtures, then stop before claiming Huawei integration complete. Do not invent SKUs/prices or call real customer cloud APIs.
