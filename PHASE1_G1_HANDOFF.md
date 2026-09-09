# Phase 1 / G1 handoff

## Scope

This handoff covers the remaining Phase 1 tickets P1.07 through P1.10 against the `main` baseline at commit `26938797f46385a0105072673f58e10837e9e9e0`.

## Implemented foundation

- Browser sessions use opaque server-side cookies with `Secure`, `HttpOnly`, `SameSite=Strict`, expiry, revocation checks, origin validation, login throttling, no-store responses, and logout cleanup.
- Dashboard authentication no longer persists bearer credentials in URLs, browser storage, or reusable request headers. Dashboard assets are self-hosted and CSP/anti-framing/referrer protections are applied.
- Service-control flags are dispatched before workload startup. Configuration uses an explicit protected machine path, strict mode/URL/port validation, safe service arguments, joined workers, and shutdown barriers.
- Spool delivery seals segments before draining, uses process ownership, immutable segments, atomic checkpoints/replacement, destination binding, bounded backpressure, quarantine, loss accounting, batch IDs, event IDs, boot/sequence identity, semantic validation, and durable ingest deduplication.
- Ingest budgets are configured at server startup and committed with accepted rows/bytes by the storage backend. Pre-auth and per-agent rate limits are separate from accepted-data entitlements. Health, readiness, metrics authorization, bounded reads, cancellation, audit events, and retention containment are wired into the Hub boundary.

## G1 verification status

No test commands were run by request. G1 is therefore **implementation-complete but not verification-complete**. The release gate still requires real SQLite/PostgreSQL security integration, browser checks, native service/ACL checks, spool fault tests, cancellation/abuse tests, selected-module and binary scans, and qualified review before the phase can be called verified.

## Rollback

Rollback must use the schema compatibility matrix and a verified restore/forward-repair path. Do not downgrade into legacy credential defaults or destructive retention. Unsafe retention remains disabled until the Phase 2 retention repair is verified.
