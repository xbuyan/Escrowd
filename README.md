# escrowd

A cryptographic escrow platform for peer-to-peer trades — Discord bot,
HTTP API, and web frontend on top of a shared Go backend. Two parties can
lock funds, deliver goods, and release payment without a middleman, with
disputes, evidence, and admin resolution built in.

Originally built for small trades on Discord (game skins, gift cards,
freelance work) where PayPal fees are too high and "trust me bro" fails
often enough to matter — it has since grown into a full deal-lifecycle
platform: authenticated accounts, Stellar-backed fund custody, M-Pesa and
Paystack payment rails, and an admin dispute-resolution console.

## How a deal works

1. Sender creates a deal (via the bot or the API) — funds are locked into
   a **Stellar claimable balance**, not held by escrowd itself
2. Receiver is invited (via a token link) and joins the deal
3. Receiver delivers the goods
4. Sender releases — the claimable balance's condition is satisfied and
   the receiver claims the funds directly on Stellar
5. Either party can **raise a dispute** instead, which freezes the deal
   and opens it to evidence submission and admin resolution
6. Unclaimed deals **auto-expire**, protecting the sender

Deal states: `locked → claimed | refunded | disputed → resolved`

## Running it

```bash
go build -o escrowd .

./escrowd bot    # Discord bot only
./escrowd api    # HTTP API only
./escrowd both   # both, concurrently (this is what production runs)
```

### Required environment variables

| Variable | Purpose |
|---|---|
| `DATABASE_URL` | PostgreSQL connection string |
| `JWT_SECRET` | Signs/verifies API auth tokens |
| `DISCORD_TOKEN` | Discord bot login |
| `ESCROWD_DB_KEY` | 32-byte hex key encrypting each deal's Stellar wallet secret before it's stored (generate with `openssl rand -hex 32`) |
| `STELLAR_NETWORK` | `testnet` or `public` |
| `STELLAR_MASTER_SECRET` | Escrow-controlling Stellar account |
| `SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASS` | Email verification (registration works without these; email sending fails gracefully and is logged) |
| `MPESA_CONSUMER_KEY`, `MPESA_CONSUMER_SECRET`, `MPESA_SHORTCODE`, `MPESA_PASSKEY` | M-Pesa STK push funding |
| `PAYSTACK_SECRET_KEY` | Paystack payment confirmation (priority dispute resolution) |
| `ESCROWD_ADMIN_EMAIL` | Grants admin privileges on the API |
| `ESCROWD_ADMIN_DISCORD_ID` | Discord user ID allowed to run `!escrow resolve` and `!escrow backup` |
| `API_PORT` | HTTP server port (defaults if unset) |
| `FRONTEND_URL` | Used in generated invite/verification links |

See [`.env.example`](.env.example) for a template (currently out of date —
see note below).

## HTTP API

All `/api/deals*` and `/api/admin/*` routes require a Bearer JWT
(`Authorization: Bearer <token>`), obtained via `/api/auth/login`.

```
POST   /api/auth/register              Create an account
POST   /api/auth/login                 Get a JWT
POST   /api/auth/verify-email          Confirm email via token
POST   /api/auth/resend-verification   Re-send verification email

GET    /api/deals                      List your deals
POST   /api/deals                      Create a deal
GET    /api/deals/:id                  View a deal (participants only)
POST   /api/deals/:id/fund             Fund the Stellar claimable balance
POST   /api/deals/:id/claim            Claim released funds
POST   /api/deals/:id/refund           Refund a locked deal (sender only)
POST   /api/deals/:id/dispute          Raise a dispute
POST   /api/deals/:id/evidence         Submit evidence for a dispute

GET    /api/invites/:token             View an invite
POST   /api/invites/:token/accept      Join a deal via invite

GET    /api/admin/deals                List all deals        (admin only)
POST   /api/admin/resolve/:id          Resolve a dispute      (admin only)
GET    /api/admin/audit                View the audit log     (admin only)

GET    /health                         Health check
```

## Discord bot

[Add escrowd to your Discord server](https://discord.com/oauth2/authorize?client_id=1489985642094919883&permissions=274877975552&integration_type=0&scope=bot)

```
!escrow lock <receiver> <amount>      Create a new escrow deal
!escrow claim <id> <secret>           Claim a deal with your secret
!escrow refund <id>                   Refund a locked deal (sender only)
!escrow status <id>                   Check deal status and integrity
!escrow balance <id>                  Check Stellar wallet balance
!escrow setaddr <id> <stellar-addr>   Set your Stellar address for payout
!escrow dispute <id> <reason>         Raise a dispute — freezes the deal
!escrow evidence <id> <link>          Submit evidence for a dispute
!escrow pay <id> <phone>              Fund a deal via M-Pesa STK push
!escrow mpesastatus <id> <checkout>   Check M-Pesa payment status
!escrow paid <id> <reference>         Confirm Paystack payment for priority resolution
!escrow history <id>                  View the full audit trail
!escrow forget                        Delete your personal data (GDPR)
!escrow help                          Show command list
```

Free tier includes basic escrow with 24h dispute resolution; a small
Paystack payment (currently KES 60) unlocks priority resolution in ~15
minutes.

## Security model

- **Auth:** JWT-based sessions; passwords hashed with **Argon2id**, never
  stored in plaintext
- **Funds:** held as **Stellar claimable balances**, not custodied by the
  application itself — escrowd controls the release condition, not the
  money
- **Rate limiting** and a **brute-force shield** on auth endpoints
- **Append-only audit log** for every deal-affecting action, independently
  queryable by admins (`/api/admin/audit`)
- **Account-enumeration resistant**: login failures and resend-verification
  responses are identical whether or not an email is actually registered
- Automated **backups** and a background **watcher** process for deal
  expiry handling

## Architecture

```
main.go                  — entry point: bot | api | both
internal/
  api/                   — HTTP handlers, JWT middleware, admin console
  auth/                  — JWT issuing/verification, Argon2id hashing
  bot/                   — Discord command handling
  escrow/                — deal state machine
  store/                 — PostgreSQL persistence (deals, users, audit)
  stellar/               — claimable balance creation/funding/claiming
  crypto/                — hashing primitives
  audit/                 — audit log recording
  bruteforce/            — login attempt shielding
  ratelimit/             — request rate limiting
  validator/             — input validation
  email/                 — SMTP verification emails
  mpesa/                 — M-Pesa STK push integration
  payment/               — Paystack payment confirmation
  backup/                — automated data backups
  watcher/               — background deal-expiry handling
docs/                    — GitHub Pages landing page
```

## Tech stack

- **Language:** Go
- **Database:** PostgreSQL (`pgx`)
- **Ledger:** Stellar (claimable balances)
- **Auth:** JWT + Argon2id
- **Bot:** Discord (`discordgo`)
- **Payments:** M-Pesa (Safaricom Daraja API), Paystack
- **Deployment:** Fly.io (`fly.toml`, `Dockerfile`)

## Build & test

```bash
go build ./...
go vet ./...
go test ./...

# Integration tests (store, api) need a real Postgres instance:
TEST_DATABASE_URL="postgres://user:pass@localhost:5432/escrowd_test?sslmode=disable" go test ./... -v
```

Test coverage currently covers `auth`, `store`, `crypto`, `escrow`,
`ratelimit`, `stellar`, `validator`, the `api` package's middleware and
auth handlers, and the bot's admin-check logic (`bot.isAdmin`). Not yet
covered: `deals.go` (the largest handler file), `admin`, most of `bot`
(the Discord command handlers themselves), `audit`, `backup`, `email`,
`mpesa`, `payment`, `watcher`.

## Known gaps

- Deal-lifecycle handlers (`deals.go`) and most of the Discord bot layer
  have no automated tests yet.

## License

MIT
