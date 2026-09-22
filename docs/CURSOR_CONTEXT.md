# CURSOR_CONTEXT — VoteGuard

**Status: architecture and documentation only. Zero implementation
code exists in this repository.** `src/`, `migrations/`, and `test/`
are empty scaffold directories with a `.gitkeep` placeholder each.
Every instruction below is what needs to be written, not a description
of what's already there — read this as a build spec, not a handoff
from working code the way LedgerLine's and VaultKeep's
`CURSOR_CONTEXT.md` files are.

## Where this sits in the portfolio

VoteGuard follows GateKeeper (rate limiting, referenced in
`docs/ARCHITECTURE.md` §6) and reuses patterns established in
LedgerLine (idempotent writes under concurrency, cache-vs-verified
reconciliation), VaultKeep and FlagForge (append-only, trigger-enforced
audit/event logs), and SlotForge (exclusion-constraint-style
database-level backstops). Read `docs/ARCHITECTURE.md` in full before
writing anything — it names exactly which prior project's mechanism
each part of VoteGuard reuses and why, and code that reimplements one
of those patterns differently from how it was solved before should
have a specific reason, not just be a fresh take on an already-solved
shape.

## Open decision: implementation stack — NOT yet chosen

Every other project in this portfolio came with an explicit language
and framework choice before any scaffolding started (LedgerLine:
Go+Postgres; VaultKeep: Python/FastAPI+Postgres; FlagForge:
NestJS+Redis+Postgres). **This request didn't specify one for
VoteGuard.** Do not default silently to whatever feels natural —
confirm the choice explicitly before writing code, because it decides
the shape of everything below (migration tooling, how the deferred/
constraint-trigger logic in §2 and §5 of `ARCHITECTURE.md` gets
applied, test framework, project layout).

What's fixed regardless of language choice, because `ARCHITECTURE.md`
specifies it at the database level, not the application level:
- **PostgreSQL** is the database — the core guarantees (§2, §4, §5)
  are implemented as row locks, a `UNIQUE` constraint, and a trigger,
  which is Postgres-specific the same way LedgerLine's balance
  invariant is.
- Whatever language is chosen should be able to run a raw SQL
  migration file cleanly (matching the `migrations/0001_init.sql`
  convention established in LedgerLine and VaultKeep) rather than
  relying solely on an ORM's auto-migration for the trigger and
  constraint definitions, since those need to be exact, reviewed SQL —
  not generated.

If picking a default without further input: match FlagForge
(NestJS/TypeScript) for portfolio stack diversity reasons, or Go to
mirror LedgerLine's precedent for "the guarantee lives in the
database, the app layer is thin." Either is defensible — this is
explicitly a decision to make with the project owner, not one to
infer.

## Schema to implement (`migrations/0001_init.sql`)

Described here in prose/structure, not as runnable SQL — the actual
migration file is code and hasn't been written. Whoever writes it
should treat this section as the spec, and `docs/ARCHITECTURE.md` as
the reasoning behind each constraint.

**`polls`** — one row per poll. Fields: id, title, description,
status (e.g. `DRAFT` / `OPEN` / `CLOSED`), opens_at, closes_at,
created_at.

**`poll_options`** — the choices for a poll. Fields: id, poll_id (fk),
label, display_order.

**`voting_tokens`** — pre-issued, single-use, identity-bound
credentials (see `ARCHITECTURE.md` §1). Fields: id, poll_id (fk),
token_hash (the raw token is never stored, same discipline as
VaultKeep's access tokens — hash it, store the hash, compare hashes),
issued_to_identity_ref (an opaque reference to whatever external
identity-verification process issued it — VoteGuard doesn't model
identity itself, see PRD scope), redeemed_at (nullable — null means
unused), created_at. A `UNIQUE` constraint alone isn't the anti-replay
mechanism here; `redeemed_at` being set, checked under a row lock at
cast time, is — see §2.

**`votes`** — append-only, per `ARCHITECTURE.md` §5. Fields: id,
poll_id (fk), option_id (fk), voter_token_id (fk, **`UNIQUE`** — this
is the database-level backstop from §2), cast_at. No `updated_at` —
that column existing at all would be a signal this table is meant to
be mutated, which it structurally is not.

Needs a `BEFORE UPDATE OR DELETE` trigger rejecting both operations
unconditionally, same shape as LedgerLine's `reject_mutation()` and
VaultKeep's discipline (though VaultKeep enforces this at the
application layer, not the database — see VaultKeep's
`ARCHITECTURE.md` §7 for that distinction; VoteGuard follows
LedgerLine's database-level version instead, per `ARCHITECTURE.md` §5
here).

**`poll_result_cache`** — materialized per-option tally. Fields:
poll_id (fk), option_id (fk), vote_count, updated_at. Updated
transactionally in the same commit as each `votes` insert — same
shape as LedgerLine's `cached_balance_minor` update inside
`PostTransaction`.

**`reconciliation_runs`** — same shape as LedgerLine's table of the
same name. Fields: id, poll_id (fk), option_id (fk), cached_count,
verified_count, drift, checked_at.

**`audit_log`** (if included — confirm scope; `ARCHITECTURE.md`
doesn't explicitly call for one beyond the vote log itself being the
audit trail, unlike VaultKeep/FlagForge which have a separate audit
table). Default to *not* building a separate audit_log table unless a
concrete need for one beyond `votes` and `reconciliation_runs`
surfaces — don't add it speculatively.

## Core logic to implement

**Vote casting (the transaction at the center of this project):**
1. Begin transaction.
2. `SELECT ... FOR UPDATE` the `voting_tokens` row by token hash.
3. If `redeemed_at` is already set: rollback, return "already used."
4. If the token doesn't exist or doesn't match the poll being voted
   on: rollback, return "invalid token."
5. Insert into `votes`.
6. Update `poll_result_cache` for the chosen option (`vote_count + 1`).
7. Set `voting_tokens.redeemed_at`.
8. Commit.

This ordering matters — see `ARCHITECTURE.md` §2 for why the lock has
to be acquired before the check, not just before the write.

**Reconciliation job (scheduled, same shape as LedgerLine's
`internal/reconcile`):** for every open or recently-closed poll,
recompute `COUNT(*) FROM votes WHERE poll_id = ? AND option_id = ?
GROUP BY option_id` and compare against `poll_result_cache`. Record
every run to `reconciliation_runs` regardless of whether drift was
found — LedgerLine's reconciler does this too, and the reasoning is
the same: a clean history of "checked and found nothing" runs is
itself part of the trustworthiness claim, not just the failures.

**Two read paths, not one** (per PRD success criteria): a fast "live
tally" endpoint reading `poll_result_cache`, and a "verified results"
endpoint that recomputes from `votes` directly. A poll's official
result, once closed, should be sourced from the verified path — decide
and document explicitly whether the live endpoint is disabled or
merely deprioritized after a poll closes.

## API surface to implement

- `POST /v1/polls` — create a poll + options (admin).
- `POST /v1/polls/:id/tokens` — issue voting tokens for a poll (admin;
  takes a list of identity references, returns raw tokens — same
  "returned exactly once" discipline as VaultKeep's token issuance).
- `POST /v1/polls/:id/vote` — cast a vote (the core transaction above).
  Deploy behind GateKeeper or document the requirement clearly if
  GateKeeper isn't wired into this repo directly.
- `GET /v1/polls/:id/results/live` — fast, cache-backed tally.
- `GET /v1/polls/:id/results/verified` — recomputed-from-`votes` tally.
- `GET /v1/polls/:id/reconciliation` — reconciliation run history
  (admin).

## Suggested order of implementation

1. Migration (`migrations/0001_init.sql`) — schema, the immutability
   trigger, the `UNIQUE` constraint on `voter_token_id`. Get this
   right first; everything else depends on it holding.
2. Vote-casting transaction logic + its concurrency test (the one from
   `docs/TESTING.md` that fires concurrent requests at the same
   token) — this is the test that proves the hardest guarantee in the
   project, so it should exist and pass before building anything else
   on top.
3. Token issuance.
4. Live-tally and verified-tally endpoints.
5. Reconciliation job + its drift-detection test.
6. Poll/option creation admin endpoints — comparatively low-risk
   CRUD, deliberately last.
7. Wire GateKeeper (or document the deployment requirement) in front
   of the vote-casting endpoint.

## Decisions not to relitigate without a new reason

- **Token = eligibility proof + replay guard, fused.** See
  `ARCHITECTURE.md` §1 and `docs/STORY.md`'s interview answer on this
  exact question. Don't split these into two separate mechanisms
  without a concrete requirement driving it (e.g. one identity voting
  across multiple independent polls with one credential).
- **Row lock + `UNIQUE` constraint, both, not one or the other.** See
  `ARCHITECTURE.md` §2. The lock alone is a discipline; the constraint
  alone would still allow a check-then-act race before the failed
  insert; both together is the actual guarantee.
- **Database-level immutability trigger on `votes`, matching
  LedgerLine's approach rather than VaultKeep's application-level
  one.** See `ARCHITECTURE.md` §5 for why this project follows
  LedgerLine's precedent specifically here.
- **Official/final results come from the verified endpoint, not the
  cache.** See `ARCHITECTURE.md` §3–§4 and PRD success criteria.
- **Rate limiting is GateKeeper's job, not reimplemented here.** See
  `ARCHITECTURE.md` §6.

## Priority order for next steps

1. Confirm the implementation stack (see "Open decision" above) —
   blocking everything else.
2. Write the migration and the concurrency test for vote-casting
   first, per "Suggested order of implementation" — these two prove
   the project's central claim; everything else is comparatively
   routine CRUD once they're solid.
3. Only after both pass against a real Postgres instance: build out
   the rest of the API surface and the reconciliation job.
4. Wire or document the GateKeeper dependency last, once the
   vote-casting endpoint it protects actually exists.
