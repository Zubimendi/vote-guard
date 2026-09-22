# VoteGuard

A fraud-resistant polling/voting system — the project in this backend
engineering portfolio built around the question every voting system
has to answer with total confidence: **can this be gamed, and can the
displayed result be trusted?**

**Status: architecture and documentation only.** This repository is a
scaffold, not a working codebase yet — see
[`docs/CURSOR_CONTEXT.md`](docs/CURSOR_CONTEXT.md) for the full build
spec and what's left to implement. Nothing in `src/`, `migrations/`,
or `test/` exists beyond an empty placeholder.

## What makes this different from "a poll with a submit button"

Double-voting prevention is usually a check-then-act flag with an
unguarded race in it. VoteGuard fuses the voting credential and the
anti-replay mechanism into one artifact — a single-use, identity-bound
token whose redemption and the vote it authorizes happen in one
transaction, locked and backstopped by a database constraint that
holds even if the application-layer logic has a bug. The full reasoning,
including which mechanism is reused from which earlier project in this
portfolio, is in `docs/ARCHITECTURE.md`.

## What it's designed to do

- Single-use, identity-bound voting tokens — the eligibility proof
  and the replay guard, fused.
- Double-voting prevention proven under real concurrency, not just a
  single-threaded test: row-level locking plus a database-level
  `UNIQUE` constraint as an unconditional backstop.
- An append-only vote log, immutable at the database level — no code
  path, including a direct SQL edit, can alter a cast vote.
- A fast, materialized live-tally cache, checked against an
  independently verified recomputation by a scheduled reconciliation
  job — the official result always comes from the verified path.
- Rate limiting on the vote-casting endpoint delegated to GateKeeper
  rather than reimplemented.

## Documentation

- [`docs/PRD.md`](docs/PRD.md) — the problem, users, and scope.
- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — every design
  decision mapped to the principle it demonstrates, and to the earlier
  project in this portfolio each mechanism is reused from.
- [`docs/TESTING.md`](docs/TESTING.md) — the test plan: what has to
  hold before this project is considered done, written before any code.
- [`docs/STORY.md`](docs/STORY.md) — narrative for LinkedIn/Medium and
  interview talking points.
- [`docs/CURSOR_CONTEXT.md`](docs/CURSOR_CONTEXT.md) — the full build
  spec: schema, endpoints, implementation order, and the one open
  decision (implementation stack) that needs to be made before code
  gets written.

## Repo layout

```
docs/           PRD, ARCHITECTURE, TESTING, STORY, CURSOR_CONTEXT
migrations/     empty — 0001_init.sql to be written per CURSOR_CONTEXT.md
src/            empty — implementation, stack TBD
test/           empty — unit + integration tests per TESTING.md
```

## License

MIT — see `LICENSE`.
