# Testing — VoteGuard

**Status: this is a test plan, not a report of tests that have been
run.** No implementation code exists yet — see `docs/CURSOR_CONTEXT.md`
for the full build spec. This document is what the eventual test suite
must satisfy, written first and deliberately, the same way
`docs/ARCHITECTURE.md` was written before any code, so "what would
prove this works" is decided before "how do I make it pass" is a
question anyone's answering.

## Unit tests (no database required)

Pure logic that doesn't need Postgres running:

- Token generation produces sufficiently random, non-guessable values
  (entropy check, not a cryptographic proof, but a sanity floor).
- Rule/eligibility-adjacent validation logic (if any exists outside the
  DB layer) rejects malformed input before it ever reaches a query.

Given how much of this project's actual guarantee lives in the
database layer (see `docs/ARCHITECTURE.md` §2 and §5), the unit-test
surface here is intentionally small — the meaningful tests are
integration tests against a real Postgres instance, not logic that can
be faked out with mocks.

## Integration tests (require Postgres)

- **Single-use token, single-threaded:** cast a vote with a valid
  token; assert the token is now marked redeemed and a vote row exists.
  Attempt to cast again with the same token; assert it's rejected and
  no second vote row was inserted.

- **Double-voting under real concurrency (the test that matters most):**
  fire N concurrent vote-casting requests, all presenting the *same*
  token, against a running instance. Assert exactly one succeeds and
  exactly one vote row exists afterward — not "usually one," exactly
  one, every run. This is the test that would catch a regression from
  optimistic-locking-without-a-backstop, or from removing the `UNIQUE`
  constraint on `votes.voter_token_id` and relying on application-layer
  locking alone.

- **Database-level immutability, bypassing the application entirely:**
  attempt a direct `UPDATE` and a direct `DELETE` against a row in
  `votes` via raw SQL — not through any application code path — and
  assert both are rejected by the database trigger. This is the same
  style of proof LedgerLine's and VaultKeep's `TESTING.md` files use:
  the guarantee has to hold even when the application layer is
  deliberately skipped, or it isn't really a database-level guarantee.

- **Unissued or forged token rejected:** attempt to cast a vote with a
  token value that was never issued; assert rejection and that no vote
  row was created.

- **Cache/verified-tally agreement after normal operation:** cast a
  batch of votes across a poll's options, then assert
  `poll_result_cache` matches a direct `COUNT(*) ... GROUP BY option_id`
  recount from `votes`.

- **Reconciliation catches deliberately introduced drift:** directly
  corrupt a row in `poll_result_cache` (simulating a bug, a bad manual
  fix, or a direct edit) and run the reconciliation job; assert it
  detects and records the drift, and that the "verified results"
  endpoint — sourced from `votes` directly, not the cache — is
  unaffected by the corruption and still reports the true count.

- **Rate limiting is delegated, not reimplemented:** confirm the
  vote-casting endpoint is deployed behind GateKeeper (or documented as
  requiring it), rather than VoteGuard silently allowing unlimited
  request volume in its absence. This is a deployment/integration check
  more than a unit assertion — see `docs/CURSOR_CONTEXT.md` for how
  it's expected to be wired.

## Manual demonstration (once built)

The same "watch the database enforce it yourself" style used in
LedgerLine and VaultKeep's `TESTING.md`:

1. Cast a vote via the API with a valid token; confirm success.
2. Try casting again with the same token via the API; confirm
   rejection.
3. Connect directly via `psql` and attempt `UPDATE votes SET
   option_id = ... WHERE id = ...`; watch it fail with the
   immutability trigger's error, not an application-level error.
4. Fire a small concurrent burst (e.g. `xargs -P` with `curl`, or a
   short script) of vote-casting requests all using the same token;
   count the resulting rows in `votes` and confirm it's exactly one.
5. Manually edit a `poll_result_cache` row via `psql`; wait for the
   next reconciliation run; confirm it's flagged and that the
   verified-results endpoint was never wrong in the meantime.

## What this plan deliberately doesn't cover

- Load/throughput testing beyond the concurrency-correctness burst in
  step 4 above — this plan proves correctness under contention, not
  performance at scale.
- Testing the identity-verification/token-issuance process itself,
  since it's explicitly out of scope for VoteGuard (see `docs/PRD.md`).
- End-to-end UI testing — there is no UI in scope.
