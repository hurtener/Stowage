# Phase 15 — Grants & team sharing

- **Status:** done
- **Owning subsystem(s):** `internal/grants` (new), `internal/store`
  (grant-aware read paths), `internal/api` (grants admin endpoints)
- **RFC sections:** §5.3 (grants — the fleet primitive), D-016, OQ-7
- **Depends on phases:** 11 (read paths to extend), 14
- **Informing briefs:** 01 (federation-graph sprawl as the anti-goal), 03
  (scope primitives)

## Goal

Team sharing without federation: a **grant** gives a named group `read` or
`contribute` access to a slice of an owner scope (topic/kind filterable),
capped by a privacy-zone ceiling, enforced **in the store layer** like scopes
(P3). One agent's memory becomes a teammate's retrievable context — within
the tenant, never across.

## Design

- Tables exist since 0001: `groups`, `group_members`, `grants`. Domain in
  `internal/grants`: Group CRUD, membership, Grant{owner-scope cols, group,
  access read|contribute, topic_filter, kind_filter, zone_ceiling,
  redaction_profile (stub string, applied later), revoked_at}.
- **Read enforcement**: retrieval identifies the caller's user (scope.User);
  store read paths gain a grant-aware variant — `effective scopes` =
  caller's own scope + scopes granted to groups the caller belongs to
  (resolved ONCE per request via `Grants().EffectiveScopes(ctx, scope)`,
  cached per-request). Lanes/GetMany accept the resolved scope set
  (signature: `[]identity.Scope`; single-element slice for the common case —
  measure no hot-path regression with the bench). Zone ceiling applied as an
  extra predicate on granted scopes only (`privacy_zone <= ceiling` ordering
  public<work<personal<intimate; granted reads NEVER return rows above
  ceiling — and personal/intimate are never grantable, validated at grant
  creation).
- **Contribute mode** (OQ-7 resolved): a contributor's candidates commit into
  the pool owner's scope; the POOL OWNER's trust gates govern supersedes
  (contributors are `agent_suggested` at most — never user_stated in someone
  else's pool). Wire: ingest accepts `target_scope` only when a contribute
  grant covers it (403 otherwise).
- **Admin/API**: `GET/PUT /v1/scopes/grants` (list/create), `POST
  /v1/grants/{id}/revoke`; group CRUD under `/v1/admin/groups` (admin role);
  every change evented. Revocation effective on next request (EffectiveScopes
  reads live).
- **Isolation invariants (conformance, both drivers)**: cross-TENANT grants
  unconstructible (validation + FK paths); zone ceiling never crossed;
  revocation immediate; non-member sees nothing; cross-user isolation
  unchanged when no grants exist (regression guard on every existing
  isolation case).

## Acceptance criteria (binding)

1. Member of granted group retrieves pool memories ≤ ceiling (both drivers);
   `personal`/`intimate` rows never cross even if mis-stored (defense test).
2. Grant creation validates: same tenant only; ceiling ∈ {public, work};
   personal+ ceilings rejected 400.
3. Revocation: effective next retrieve (test toggles grant mid-test).
4. Contribute: covered contributor commits into pool with pool-owner trust
   gates (parked when superseding high-trust pool memory — test); uncovered
   contributor 403.
5. No-grants regression: full existing conformance isolation suite green
   unchanged.
6. EffectiveScopes resolution ≤1 extra query per retrieve (bench guard);
   eval-ci scores unchanged.
7. Events for grant/revoke/group changes; smoke drives grant→retrieve→
   revoke→denied end-to-end.
8. Coverage ≥85 grants; race ×3; smokes 01–15.

## Files added or changed

```text
internal/grants/{grants.go, grants_test.go}
internal/store/{store.go grants methods, both drivers, conformance}
internal/retrieval (EffectiveScopes wiring), internal/api/grants_handler.go
scripts/{coverage.json, smoke/phase-15.sh}
```

## Decisions filed

- D-059: contribute-mode trust = pool owner's gates; contributor content
  enters as ≤ agent_suggested (OQ-7 resolved).
- D-060: granted reads resolve effective scopes per-request (no
  materialization); zone ceilings validated at creation AND enforced at read.
