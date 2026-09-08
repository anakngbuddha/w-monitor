# Phase 4 of 5: one-run AI analysis and reviewed reporting

**Outcome:** optional, privacy-approved narrative/classification; deterministic reports remain independently usable. **Runtime layer:** Layer 5. **Entry:** G3 approved. **Exit:** G4. Default model preference: `minimax/minimax-m3:free`, configurable and subject to verification. No production-data calls until consent/routing gates pass.

## P4.01: AI contract, approved fixtures and local validators
- Depends on: G3. New `schemas/ai-input-v1.schema.json`, `schemas/ai-output-v1.schema.json`, `assessment/ai/validate.go`, `testdata/ai/`.
- Translate the shared specification into strict bounded schemas, Go DTOs and semantic validators. Exact asset-set, scope/evidence references, candidate strategies, workload agreement, required executive summary and no writable computed/approval fields. Separate ordinal model confidence from deterministic evidence quality. Treat all output as plain text.
- Accept: malformed/duplicate-key/trailing JSON, extra/missing/foreign IDs, invented evidence, invalid enums, contradictory workloads, overlong responses and forbidden strategies cannot publish. No paid or free inference needed for these tests.

## P4.02: frozen payload assembler and privacy budget preflight
- Depends on: P4.01. New `assessment/ai/payload.go`, redaction/canonicalization and size-budget tests.
- Assemble one tenant/project/run/snapshot with pseudonymous IDs, graph summary, approved context, raw-score summaries, eligible strategies and evidence index. Strip secrets and private identity before any provider dispatch. Deterministically compact and account for every included/omitted item; output budget reserves every required server response. Oversize defaults to deterministic-only, not silent truncation.
- Accept: input hashes stable for identical evidence; altered context/policy changes hash; planted secrets never reach the mock transport; over-budget input or projected output makes zero inference calls. Graph boundary risks survive compaction. F20/F21/F22/F23/F24/F25/F26/F27 as defined in traceability.

## P4.03: OpenRouter adapter and explicit configuration
- Depends on: P4.02. New `assessment/ai/openrouter.go`, typed server config and mock HTTP integration tests.
- Provider-neutral interface; verified HTTPS endpoint, server-side key, no credential redirects, deadline/response cap. Cache capability metadata; request JSON-object mode only when supported, never assume JSON Schema enforcement. Pin permitted routes/subprocessors and record actual model/provider response identity. Paid fallback and automatic random free routing disabled.
- Accept: mocked valid success, auth/credit/permission failure, 429, unavailable route, context error, HTML error body, oversized body, malformed/truncated success and timeout. No retry occurs in the low-level adapter; durable job logic owns retry policy. Live smoke test uses synthetic data and explicit operator budget.

## P4.04: durable jobs, cache and shared attempt budgets
- Depends on: P4.03. New assessment-job/attempt repositories and worker; unique keys, leases and fencing.
- Implement the shared job machine, complete cache key, explicit action trigger and account-wide durable budget ledger. Default one attempt; optional total two with prior approval. Persist dispatch before sending; ambiguous crash becomes outcome_unknown, not duplicate delivery. Enforce tenant fairness, Retry-After minimum/deadlines and local quota ceilings across replicas/restarts.
- Accept: 50 simultaneous identical requests across two replicas produce one job/normal dispatch; changing snapshot/model/prompt/context invalidates cache; branding/export does not. Crash after dispatch never auto-resends. Retry/repair/fallback share the same cap. Daily exhaustion, cancellation and lease fencing tests pass. Provider counting remains conservatively estimated, not falsely guaranteed. F26/F27.

## P4.05: prompt, strategy explanation and adversarial evaluation
- Depends on: P4.04. New versioned prompt and reviewed golden/adversarial corpus.
- Lean prompt with no tools, explicit distrust of evidence text, strict result schema, citation/unknown requirements and eligible strategies. Explain technical scores without overwriting them. Aggregate every server under its workload and produce executive limitations, risk flags, missing information and criticality review.
- Accept: injected instructions in hostnames/services/context cannot change output authority, expose secrets or initiate actions; unsupported savings/SKUs/recovery claims never publish. Local mock corpus must pass; record real synthetic smoke results separately. Proposed evaluation minimum: 30 reviewed varied engagements/fixtures, including incomplete inventories and cyclic dependencies; zero invalid/uncited critical claims in accepted outputs. Human review still required. F25.

## P4.06: explicit generation, review and approval workflow
- Depends on: P4.05. Targets: Hub UI, proposed AI-job/review routes, audit events and report access.
- Separate Finalize Evidence, Generate AI Analysis, Review, Approve, and Export actions. Show outbound-data preview, consent, model/route, budget estimate, job state and AI-disabled option. Reviewer sees deterministic result and model suggestion side by side; can approve/override with reason. Store actor/time/version; new evidence/context invalidates applicability of old approval. AI never signs off.
- Accept: viewer cannot generate/approve; tenant/project roles and CSRF enforced; double-click reuses job; no model call on agent check-in/schedule/export; failures still permit deterministic report. Reviewer edits never overwrite raw evidence. F15/F26/F27.

## P4.07: safe HTML and new DOCX export integration
- Depends on: P4.06. Targets: existing `export/report.go`, proposed `export/docx.go`, report version/manifests and browser/download tests.
- Add Migration Strategy section with evidence refs, confidence type, missing information, rule score vs AI review, executive summary and human approval. HTML first, then explicitly new DOCX backend from the same canonical model. Use an approved maintained DOCX library with license review; no dependency choice by guesswork. Script-free exports, no active external links/relationships/macros; images/fonts self-contained where needed.
- Accept: identical scoped facts across HTML/DOCX/JSON/CSV; hostile names/model text remain literal; DOCX opens in supported Word/LibreOffice versions and has no unexpected external relationship. A cache-only report re-render causes zero inference calls. Print-to-PDF remains optional, not confused with a implemented server PDF engine. F06/F13/F25.

## G4 exit gate and handoff

Run the complete assessment with valid AI, AI disabled, denied consent, 429, depleted budget, unavailable model and malformed output. Every path yields an honest result/status, never an approved fabricated report. Prove replica job behavior, semantic validation, privacy controls and scoped access. A reviewer approves provider terms, data routing and model evaluation before customer data leaves the Hub. Free-tier numeric assumptions must be reverified in the actual account. No free-model uptime promise.

Rollback: disable AI generation by feature flag; render immutable validated prior results with original metadata and review status, or deterministic-only new results. No silent model substitution or automatic paid fallback. Previously generated artifacts follow the same retention/deletion controls as evidence.

### Copy-paste start prompt

> Read 00-SHARED-CONTRACT.md, this phase file and the G3 handoff. Implement only P4.01, using synthetic fixtures and no external inference. Create strict schemas and semantic tests first. Do not put provider credentials in code, weaken exact asset/evidence validation or introduce retries. Return the ticket handoff and stop.
