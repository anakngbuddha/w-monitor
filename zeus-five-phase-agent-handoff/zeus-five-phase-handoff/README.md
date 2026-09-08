# Zeus implementation handoff pack

**Repository:** https://github.com/anakngbuddha/w-monitor
**Planning baseline:** 02ef77cdc3b1c1cd27ae618eeeff93c761c062a9 (head checked on 2026-09-07).

## How to use this pack

1. Keep the revised audit beside this pack; it preserves every original finding and source citation.
2. Give Grok `00-SHARED-CONTRACT.md`, the current phase file, and the specific original findings relevant to **one ticket**.
3. Start with P1.01. Use the copy-paste prompt at the end of that phase file. Do not run a broad test suite against production credential paths.
4. Review the real test/commit/migration handoff before assigning the next ticket. Complete all tickets and the phase's G gate before advancing.
5. Keep `06-TRACEABILITY-AND-ROADMAP.md` updated with implementation proof, not optimistic completion notes.

The pack has exactly five phase files and 40 numbered execution tickets. Shared schemas/path names are specifications to implement, not claims that those files already exist. This pack contains no source changes, API credentials, external inference calls or customer cloud actions.

## Files

- `00-SHARED-CONTRACT.md`: corrections, architecture, identities/data/API contracts, criticality formula, AI/privacy/budget rules and coding-agent guardrails.
- `01-PHASE-1.md`: security and reliable foundations (10 tickets).
- `02-PHASE-2.md`: trustworthy infrastructure/application evidence (8 tickets).
- `03-PHASE-3.md`: dependency graph and deterministic assessment (7 tickets).
- `04-PHASE-4.md`: constrained AI, human review and reports (7 tickets).
- `05-PHASE-5.md`: Huawei scenarios, migration assurance and pilot (8 tickets).
- `06-TRACEABILITY-AND-ROADMAP.md`: all original V/L/F findings, added F20-F30 features, deferred scope and verification ledger.
- `SHA256SUMS`: checksums of the above Markdown files.

No phase is one giant prompt. A capable builder still needs small scopes, executable checks and a reviewer. The AI used inside Zeus is separate from the coding agent implementing Zeus.
