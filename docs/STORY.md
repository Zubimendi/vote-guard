# The story — VoteGuard

## Short version (for a post/bio line)

Designed a fraud-resistant voting system where the anti-replay
credential and the eligibility proof are the same artifact — so
ballot-stuffing and double-voting collapse into one solved problem
instead of two — with double-voting prevention proven under real
concurrency, not just a single-threaded happy path, and an append-only
vote log that even a direct database edit can't tamper with.

## Longer version (for LinkedIn / a Medium-style post)

Most "prevent duplicate votes" implementations check a flag before
inserting a row: has this person voted yet? If not, insert and set the
flag. That check-then-act sequence has an unguarded window in it, and
under real concurrent load — two tabs, a retried request, someone
deliberately racing the endpoint — that window is exactly where
ballot-stuffing bugs live.

VoteGuard's design collapses two problems that are usually solved
separately. The voting token isn't just a "don't process this twice"
idempotency key sitting alongside a separately-authorized request —
it's the eligibility proof *and* the replay guard, fused into one
artifact. Redeeming the token and inserting the vote happen in the
same transaction, with the token's row locked first. An attacker can't
manufacture extra votes without also manufacturing extra valid tokens,
which means compromising whatever verified-identity process issued
them — a much higher bar than finding a race condition in a counting
function.

And because "the application layer checks correctly" is a claim about
code, not a guarantee that survives every future code path, there's a
second layer underneath it: a database-level `UNIQUE` constraint on the
token reference in `votes`, so even a bug, a hand-run migration, or a
direct script can't produce a second vote for the same token. The same
two-layer discipline — careful application logic, backed by a
constraint the database enforces unconditionally — shows up for the
vote log's immutability too: `votes` rows can't be updated or deleted
by anyone, including someone with direct database access, because a
trigger says no, not because the application politely declines to
expose that operation.

The part I think is most worth explaining in an interview isn't any
single mechanism, though — it's that this project didn't invent new
solutions to any of its problems. The materialized-cache-plus-
independent-reconciliation pattern is the same one LedgerLine uses for
account balances; the idempotent-write-under-concurrency pattern is the
same one LedgerLine uses for posting transactions; the append-only,
trigger-enforced history is the same discipline as VaultKeep's and
FlagForge's audit logs; rate limiting on the vote-casting endpoint is
explicitly delegated to GateKeeper rather than rebuilt. Recognizing
"I've already solved a version of this" and reapplying it correctly is
a different, and arguably more senior, skill than solving each problem
from scratch every time it shows up in a new shape.

## Interview talking points

- **"Walk me through a design decision you'd defend under pushback."**
  Fusing the eligibility token and the idempotency key into one
  artifact instead of keeping them separate. It's less flexible — a
  system that wanted to let one verified identity vote in multiple
  independent polls with one credential would need a different design
  — but for a single-poll voting system specifically, collapsing the
  two into one means there's exactly one thing that has to be hard to
  forge, not two things that both have to be, independently, hard to
  forge and kept consistent with each other.

- **"How do you know the double-voting prevention actually holds under
  concurrency, not just in a test you wrote to pass?"** The plan (see
  `docs/TESTING.md`) is specifically to fire concurrent requests at a
  real running instance with the same token and count the resulting
  rows — not to unit-test the locking logic in isolation, which can
  pass while the actual race still exists. Correctness-under-concurrency
  claims need a test that actually creates concurrency.

- **"Why does the database enforce immutability instead of just not
  building an update/delete endpoint?"** Because "we didn't build that
  endpoint" is a statement about today's code, not a guarantee — it
  holds until someone adds one, or until someone with database access
  runs a script. A trigger that rejects the operation unconditionally
  is a guarantee that holds regardless of what future code, or a
  future person with the right credentials, tries to do.

- **"What's explicitly not done?"** Ranked-choice/multi-select voting,
  the identity-verification process itself, and any poll
  creation/moderation UI — all listed in `docs/PRD.md` as deliberate
  scope cuts, and all code work — including which stack decision is
  still open — listed in `docs/CURSOR_CONTEXT.md`, since this project
  is at the documentation-and-architecture stage, not implemented yet.
