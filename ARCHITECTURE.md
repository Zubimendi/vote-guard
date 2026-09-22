# Architecture: principles → design

Same format as every project in this portfolio: each section names a
principle, what it means for a fraud-resistant polling/voting system
specifically, and the concrete mechanism that implements it. Where a
mechanism reuses a technique already established elsewhere in this
portfolio, that's named explicitly — recognizing a solved shape
reapplied is as much the point as the mechanism itself.

## System shape

```
   eligible    ┌──────────────────┐
   voter   ───▶│ voting token        │  pre-issued, single-use,
              │ (identity-bound)    │  bound to a verified voter
              └────────┬─────────────┘  identity - can't be manufactured
                        │ redeem + cast, ONE transaction
                        ▼
              ┌──────────────────┐
              │ votes (append-only) │◀── the source of truth,
              │ + poll_result_cache  │    never mutated after insert
              │ (materialized tally) │
              └────────┬─────────────┘
                        │ scheduled
                        ▼
              ┌──────────────────┐
              │ reconciliation job   │  cache tally vs. COUNT(*) FROM
              │                      │  votes - proves the live number
              └──────────────────┘  shown to viewers is actually true
```

---

## 1. The idempotency key IS the credential — not a separate, client-chosen value

**How this differs from every other idempotent-write pattern in this
portfolio:** LedgerLine, GateKeeper, and SlotForge all use an
`Idempotency-Key` the *client* generates and supplies alongside an
otherwise-independently-authorized request — the key's only job is
"don't do this twice if you see it twice." VoteGuard's `VotingToken` does
double duty: it's simultaneously the proof that this voter is eligible to
vote at all, *and* the mechanism that prevents them from voting twice.
Redeeming it (marking it used, in the same transaction as inserting the
vote) is both the authorization check and the idempotency guarantee,
fused into one artifact rather than two. This matters because it's what
makes ballot-stuffing and double-voting the *same* solved problem instead
of two separate ones: an attacker can't manufacture extra votes without
also manufacturing extra valid tokens, which requires compromising
whatever verified-identity process issued them in the first place — a
much higher bar than exploiting a vote-counting bug.

## 2. Preventing double-voting under real concurrency, with two layers

**The race this defends against:** two simultaneous vote requests
presenting the *same* token. If both read "this token is unused," both
proceed to insert a vote and mark the token used — a naive check-then-
act sequence has an unguarded window between the check and the write,
and under real concurrent load (a voter double-clicking, a retried
request from a flaky connection, or a deliberate attempt to race the
system) that window is exploitable.

**Layer one — pessimistic locking:** the vote-casting transaction locks
the token's row (`SELECT ... FOR UPDATE`) before checking whether it's
already been redeemed, and marks it redeemed in the same transaction
that inserts the vote. A second concurrent request presenting the same
token blocks on that lock until the first transaction commits (or rolls
back), and then correctly observes the token as already used — the same
row-locking discipline used for LedgerLine's account balances and
PyDataRex's leader-election lease row, applied here to a single-use
credential specifically.

**Layer two — a database constraint as the backstop:** a `UNIQUE`
constraint on `votes.voter_token_id` means that even if some future code
path bypassed the row-locking discipline above (a bug, a direct script,
a migration run by hand), the database itself refuses a second vote row
for the same token — the same "the application layer's care is real but
the database constraint is the actual, unconditional guarantee"
philosophy behind LedgerLine's balanced-transaction trigger and
SlotForge's exclusion constraint. Two layers, not because one isn't
enough in theory, but because defense in depth means a mistake in one
layer doesn't silently become a live fraud vector.

## 3. Real-time tallies via a materialized cache, verified independently

**Where:** `poll_result_cache` holds a live, fast-to-read vote count per
option, incremented transactionally in the same commit as each vote
insert — a viewer-facing "results so far" dashboard should never have to
run `COUNT(*) ... GROUP BY option` over a potentially large `votes` table
on every page load. This is the fifth appearance in this portfolio of the
same shape: a fast materialized read next to an authoritative, append-
only source of truth, previously seen in LedgerLine (account balances),
SplitStack (group balances), ShipTrace (shipment status), and FlagForge
(config version) — recognized here again rather than treated as a novel
problem each time.

**Why it still has to be independently verified, not just trusted:** a
cache that's "updated correctly in the same transaction" is a claim about
the code, not a permanent guarantee about the data — a future bug, a
manual fix during an incident, or a direct database edit could all
silently desync the cache from the append-only vote log. This is
*especially* unacceptable for a voting system specifically: a wrong
live tally isn't just a display bug, it's the kind of thing that
undermines trust in the result itself. See §4.

## 4. Reconciliation as the mechanism that makes the tally trustworthy, not just fast

**Where:** a scheduled reconciliation job recomputes each option's true
vote count directly from the append-only `votes` table (`COUNT(*) ...
GROUP BY option_id`) and compares it against `poll_result_cache`,
flagging and persisting any disagreement — the same reconciliation
discipline used by Dispatcher (delivery vs. billing), LedgerLine (cached
vs. verified account balance), and SearchCraft (Postgres vs. Meilisearch
document count), applied here to a public-facing vote tally, arguably
the domain where "prove the number is actually right, don't just assert
it" matters most in this entire portfolio. A poll's final, official
result should be reported from this verified recomputation — not from
the cache — precisely because the cache's whole purpose is speed, not
being the thing anyone's trust rests on.

## 5. The vote log is append-only — a tampering-resistance property, not just an audit-trail nicety

**Where:** `votes` rows are never updated or deleted after insertion,
enforced by a database trigger — the same technique used for
LedgerLine's ledger, VaultKeep's audit log, FlagForge's audit log, and
ShipTrace's event log, its fifth appearance in this portfolio. For a
voting system specifically, this property is not merely convenient for
debugging — it's foundational to the entire premise of "fraud-resistant."
A system where a vote record *could* be quietly edited after the fact
has no real basis for anyone to trust its output, regardless of how
sound its front-door fraud prevention (§1, §2) is. Append-only-by-
database-constraint is what makes "nobody, including an administrator
with direct database access, edited the historical record" a claim that
holds even under a compromised or malicious operator, not just a
well-intentioned one.

## 6. Per-identity rate limiting via existing infrastructure, not reimplemented

**Where:** VoteGuard's own concern is correctness of the vote-casting
transaction (§1–§2); protecting the vote-casting endpoint from abusive
request volume — a script hammering the endpoint with stolen or
brute-forced tokens, for instance — is rate limiting, a problem this
portfolio already solved deeply in **GateKeeper** (Project 2), including
the specific atomicity concerns (Lua-script-based, race-free counters)
that a naive rate limiter gets wrong. VoteGuard should sit behind
GateKeeper (or an equivalent already-built rate limiter) for per-token
and per-source-IP throttling on the vote-casting endpoint, rather than
building a second, likely-worse rate limiter inside this project. This
is the same "recognize infrastructure you already built rather than
re-solving it" discipline ShipTrace applied to webhook delivery and
PyDataRex applied to job execution, now applied a third time to rate
limiting specifically.

## What's out of scope, and why

- **Ranked-choice, multi-select, or weighted voting.** v1 is single-
  choice-per-token, which is sufficient to demonstrate the correctness
  properties this project exists to prove; more sophisticated ballot
  types are a real, separate data-modeling problem that would dilute
  this project's actual focus.
- **The verified-identity issuance process itself** (how a voter proves
  who they are before receiving a token — email verification, SSO,
  government ID checks) is treated as an external input to this system,
  not something VoteGuard implements. VoteGuard's guarantee starts at
  "given a set of tokens issued to verified identities," not at identity
  verification itself, which is a large, distinct problem space.
- **Poll creation/management UI and moderation tooling.** Real product
  surface, deliberately out of scope for a project about the vote-
  casting and tallying correctness core.
