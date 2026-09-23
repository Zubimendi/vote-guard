# VoteGuard

A fraud-resistant polling/voting system — single-use identity-bound tokens,
row-locked vote casting with a database `UNIQUE` backstop, an append-only
vote log, a live tally cache, and independent reconciliation.

## Stack

- Go HTTP API (`chi`)
- PostgreSQL 16 (schema, triggers, and constraints own the hard guarantees)

## Quick start

```bash
# 1. Start Postgres
docker-compose up -d

# 2. Run the API (applies migrations on startup)
export DATABASE_URL=postgres://voteguard:voteguard@localhost:5432/voteguard?sslmode=disable
export ADMIN_API_KEY=dev-admin-key-change-me
go run ./cmd/server

# 3. Import postman.json into Postman and run:
#    Admin → Create Poll → Issue Tokens → Vote → Cast Vote → Results
```

Default listen address: `http://localhost:8080`.

### Environment

| Variable | Default | Purpose |
|---|---|---|
| `DATABASE_URL` | local docker-compose DSN | Postgres connection |
| `ADMIN_API_KEY` | `dev-admin-key-change-me` | Value for `X-Admin-Key` on admin routes |
| `PORT` | `8080` | HTTP port |
| `RECONCILE_INTERVAL` | `30s` | Background reconciliation ticker |
| `MIGRATIONS_DIR` | `migrations` | SQL migration directory |

Copy `.env.example` for a starting point.

## API surface

Admin routes require header `X-Admin-Key: <ADMIN_API_KEY>`.

| Method | Path | Auth |
|---|---|---|
| `POST` | `/v1/polls` | admin |
| `POST` | `/v1/polls/:id/tokens` | admin |
| `POST` | `/v1/polls/:id/close` | admin |
| `POST` | `/v1/polls/:id/reconcile` | admin (MVP: run reconcile now) |
| `GET` | `/v1/polls/:id/reconciliation` | admin |
| `POST` | `/v1/polls/:id/vote` | none (token in body) |
| `GET` | `/v1/polls/:id/results/live` | none (`official: false`) |
| `GET` | `/v1/polls/:id/results/verified` | none (`official: true` when poll is `CLOSED`) |

Raw voting tokens are returned **once** from token issuance; only SHA-256
hashes are stored.

## Tests

```bash
docker-compose up -d
go test ./internal/...
DATABASE_URL=postgres://voteguard:voteguard@localhost:5432/voteguard?sslmode=disable go test ./test/ -count=1
```

Integration tests cover single-use tokens, concurrent double-cast (exactly
one vote), immutability trigger, forged tokens, cache/verified agreement,
and reconciliation drift detection.

## Rate limiting (GateKeeper)

VoteGuard does **not** implement request-rate throttling. Deploy the
vote-casting endpoint behind [GateKeeper](../gatekeeper) (or an equivalent
rate limiter) for per-token / per-IP protection. See
[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) §6.

## Documentation

- [`docs/PRD.md`](docs/PRD.md)
- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)
- [`docs/TESTING.md`](docs/TESTING.md)
- [`postman.json`](postman.json) — import into Postman

## License

MIT — see `LICENSE`.
