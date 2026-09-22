# PRD — VoteGuard

## Problem

Most lightweight polling tools — the kind a community, a classroom, or
an internal team reaches for — are trivially gameable: nothing stops
one person submitting the same response repeatedly, nothing proves the
live tally shown to viewers is actually correct, and "we don't allow
duplicate votes" is usually an unenforced claim rather than a
mechanism. VoteGuard exists to take "fraud-resistant" from a phrase on
a landing page to a set of specific, testable guarantees: a vote can't
be cast without a pre-issued credential tied to a verified identity,
that credential can't be used twice — even under concurrent requests
racing to use it — and the tally anyone sees is either the live,
fast-read cache or the independently verified recomputation it's
checked against, never an unverified number taken on faith.

This is also the point in the portfolio where several previously
solved problems — idempotent writes under concurrency (LedgerLine),
append-only history (LedgerLine, VaultKeep, FlagForge), a fast cache
checked against a verified source of truth (LedgerLine, and others),
and rate limiting (GateKeeper) — compose into a single system, rather
than each being demonstrated in isolation. See `docs/ARCHITECTURE.md`
for exactly where each reused pattern shows up and how it's adapted to
voting specifically.

## Users

Portfolio / reference project, not a product with real users. The
intended "users" are:

- **Me**, as the concrete artifact for interviews where correctness
  under concurrency and fraud-resistance are the properties being
  evaluated — not just "can you build a CRUD app with a nice UI."
- **A reader/reviewer** assessing whether the double-voting defense,
  the append-only guarantee, and the reconciliation story actually
  hold up, not just whether the pitch sounds right.

## Scope

**In scope** (see `docs/ARCHITECTURE.md` for the mechanism behind each):

- Single-use, identity-bound voting tokens, pre-issued to verified
  voters. A token is both the eligibility proof and the anti-replay
  mechanism — redeeming it and casting the vote happen in one
  transaction.
- Double-voting prevention under real concurrency: row-level locking
  on the token at cast time, backed by a database-level `UNIQUE`
  constraint as an unconditional backstop independent of the
  application code's correctness.
- An append-only `votes` table — no code path, including a direct
  database edit, can update or delete a cast vote after the fact,
  enforced at the database level.
- A fast, materialized `poll_result_cache` for live tally reads,
  updated transactionally alongside each vote.
- A scheduled reconciliation job that recomputes each option's true
  count directly from `votes` and compares it against the cache,
  flagging and persisting any drift.
- Delegating request-rate throttling on the vote-casting endpoint to
  GateKeeper (or an equivalent already-built rate limiter), rather
  than reimplementing one.

**Explicitly out of scope:**

- Ranked-choice, multi-select, or weighted voting — v1 is single-choice-
  per-token, which is sufficient to demonstrate the correctness
  properties this project exists to prove.
- The verified-identity issuance process itself (how a voter proves who
  they are before receiving a token). Treated as an external input;
  VoteGuard's guarantee starts at "given tokens issued to verified
  identities," not at identity verification.
- Poll creation/management UI and moderation tooling — real product
  surface, deliberately out of scope for a project about vote-casting
  and tallying correctness specifically.

## Success criteria

- Two concurrent vote-casting requests presenting the same token result
  in exactly one recorded vote, under load, not just in a
  single-threaded test.
- A vote cannot be inserted, updated, or deleted outside the
  vote-casting transaction path — including via a direct SQL statement
  bypassing the application entirely — demonstrated the same way
  LedgerLine's and VaultKeep's immutability guarantees are: by trying
  to break it directly and watching the database refuse.
- `poll_result_cache` and a from-scratch recount from `votes` agree
  after normal operation, and the reconciliation job catches and
  records drift if the cache is deliberately corrupted out-of-band.
- A poll's official, final result is always sourced from the verified
  recomputation, not the cache, and that distinction is visible in the
  API surface (a fast "live" endpoint and a slower "verified" one, not
  one endpoint quietly serving both from the same unverified source).
- No unissued or reused token can produce a recorded vote, under any
  tested attempt to do so.
