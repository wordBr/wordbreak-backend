# WordBreak — Backend

Go service that is both the **game engine** and the **referee**. It validates words, generates
racks, scores rounds, tracks the daily leaderboard, and — critically — signs the EIP-712
settlement results that `WordBreakPools` verifies on-chain before paying out.

## Why Go

Fast in-memory dictionary lookups (370k words), trivial concurrency for the API, and
`go-ethereum` gives native EIP-712 signing that matches the contract byte-for-byte (proven by
`internal/signer` tests against the contract's own digest).

## Layout

```
cmd/server            entrypoint (env config, graceful shutdown)
internal/dictionary   embedded English word list + validation / word-finding
internal/rack         known-good rack generation (solo random, daily deterministic)
internal/game         server-authoritative scoring (the referee's judgment)
internal/signer       EIP-712 settlement signing (matches WordBreakPools)
internal/store        in-memory daily rounds + leaderboards (swap for Postgres later)
internal/api          HTTP handlers + routing
```

## Run

```bash
# Game only (no signing):
go run ./cmd/server

# With the referee configured (enables settlement signing):
REFEREE_PRIVATE_KEY=0x...   \
POOLS_CONTRACT=0x...        \
CHAIN_ID=42220              \
ADMIN_TOKEN=change-me       \
go run ./cmd/server
```

### Environment

| Var | Default | Purpose |
|---|---|---|
| `PORT` | `8080` | HTTP port |
| `REFEREE_PRIVATE_KEY` | — | Referee signer key. Omit to run the game without signing. |
| `POOLS_CONTRACT` | — | Deployed `WordBreakPools` address (EIP-712 `verifyingContract`). Required if signing. |
| `CHAIN_ID` | `42220` | 42220 = Celo mainnet, 11142220 = Celo Sepolia. |
| `CHAIN_RPC_URL` | — | Read-only RPC to verify on-chain entry. Required for paid dailies. |
| `ADMIN_TOKEN` | — | Protects `/api/admin/*`. Omit to disable admin routes. |
| `SOLO_RACK_SIZE` | `6` | Solo rack size (4–8). |
| `DAILY_RACK_SIZE` | `6` | Daily rack size (4–8). |

> **Security:** the referee key controls who gets paid. In production keep it in a secrets
> manager / KMS, never in the repo, and put the `/api/admin/*` routes behind network controls.

## API

| Method | Path | Notes |
|---|---|---|
| GET | `/health` | liveness + dictionary size |
| GET | `/api/referee` | referee address (to configure the contract) |
| GET | `/api/solo/rack?size=6` | fresh solo rack (answers withheld) |
| POST | `/api/solo/score` | `{letters, words[]}` → scored result |
| GET | `/api/daily` | today's shared rack (deterministic per date) |
| POST | `/api/daily/submit` | `{address, words[]}` → scores vs the server's rack. **For a paid round, gated on on-chain `hasEntered` and rejected after `endTime`.** |
| GET | `/api/daily/leaderboard?date=YYYY-MM-DD` | ranked standings |
| POST | `/api/admin/daily/open` | `{roundId, endTime, dateKey?}` → register today's on-chain round so paid submissions are gated (needs `X-Admin-Token`) |
| POST | `/api/admin/sign-settlement` | `{roundId, winners[], amounts[]}` → referee signature (needs `X-Admin-Token`) |

### Fund safety

Paid submissions are **gated on on-chain entry**: `/api/daily/submit` refuses any address that
didn't call `enter()`, and refuses submissions after `endTime`. Without this, an unpaid address
could be scored, top the leaderboard, and be signed as a winner — draining the honest pot.
`sign-settlement` re-checks `hasEntered` for every winner as defense in depth.

### The operator round lifecycle

1. **Open on-chain** — run the `CreateRound` forge script (`contracts/script/CreateRound.s.sol`)
   with `roundId` = a `YYYYMMDD` date key, an entry fee, and an `endTime`.
2. **Register with the backend** — `POST /api/admin/daily/open` with the same `roundId` + `endTime`.
   Now paid submissions are gated.
3. **Players** enter (approve + `enter`), play, submit scores (gated).
4. **Settle** — after `endTime`: compute winners + amounts from the leaderboard,
   `POST /api/admin/sign-settlement` to get the referee signature, then submit
   `pool.settle(roundId, winners, amounts, signature)` on-chain. Winners `claim()`.

## Anti-cheat status (MVP)

Scoring is server-authoritative (the client can't self-report a win). The **open risk** noted in
the concept doc still stands: a paid mode that rewards raw word count is beatable by an anagram
solver. Before enabling real-money daily pools, add speed/typing-cadence signals, small capped
stakes, and/or account staking. Tracked, not yet built.

## Dictionary note

Uses the full `words_alpha` English list (~370k words), which includes obscure entries. A
frequency-filtered "common words" pass is a good follow-up so players don't lose to words nobody
knows.

## Test

```bash
go test ./...
```

The load-bearing test is `internal/signer.TestDigestMatchesContract`: it asserts the Go referee
produces the **exact** digest the Solidity contract computes for identical inputs.
