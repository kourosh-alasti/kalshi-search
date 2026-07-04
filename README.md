# Kalshi SMS Alert Bot

A Go service that continuously scans [Kalshi](https://kalshi.com) markets for contracts with low implied odds (default: YES buyable at ≤ 30¢, i.e. ≤ 30% implied probability with a ~3.3x+ payout), texts you each opportunity via [Telnyx](https://telnyx.com), and learns from which suggestions you act on to send more trades like the ones you take.

No web UI — everything is configured through environment variables and controlled by texting the bot back.

## How it works

- **Scanner loop** polls Kalshi's Trade API v2 (read-only, RSA-PSS signed requests) on `POLL_INTERVAL`, filters open markets by price, volume, open interest, close time, and your enabled categories, ranks the survivors with a preference model, and texts the top picks as one digest SMS with `kalshi.com` links that open the Kalshi app on your phone.
- **SMS commands**: the first text you receive is a numbered category menu. Reply `1,3` to enable categories, `TOOK 12` / `PASS 12` to give feedback on suggestion #12, `LIST`, `STATUS`, `PAUSE`, `RESUME`, or `ALL`.
- **Learning**: every suggestion decomposes into features (category, series, price band, close-time bucket, volume bucket) with accept/reject counts per user. Feedback comes from your `TOOK`/`PASS` replies and automatically from your portfolio — if a new position appears in a suggested market, it counts as accepted. All suggestions and labels are stored in libSQL for future ML training (see the plan's "Future: ML-based learning").
- **Dedup**: each market alerts at most once per user; it re-alerts only if the price drops by `REALERT_DROP_CENTS`. `PASS`ed markets never alert again.
- **Persistence**: all state is stored in [libSQL](https://github.com/tursodatabase/libsql) keyed by phone number. Each alert recipient has independent categories, preferences, and suggestion history.

## Setup

### 1. Kalshi API key
Create an API key at kalshi.com → Profile Settings → API Keys. Set `KALSHI_API_KEY_ID` and either paste the PEM into `KALSHI_API_PRIVATE_KEY` or point `KALSHI_PRIVATE_KEY_PATH` at the file (locally it defaults to `certificates/kalshi-private-key.pem`). Read access is sufficient.

### 2. Telnyx
1. Create an API key in the [Telnyx Mission Control Portal](https://portal.telnyx.com) → API Keys → `TELNYX_API_KEY`.
2. Buy an SMS-capable number and assign it to a Messaging Profile → `TELNYX_FROM_NUMBER` (E.164, `+1...`).
3. On that Messaging Profile, set the inbound webhook URL to `https://<your-app>/webhooks/telnyx` (API v2 format).
4. Copy your account public key (Account Settings → Keys & Credentials → Public Key) → `TELNYX_PUBLIC_KEY`; it's used to verify webhook signatures.
5. Set `ALERT_PHONE_NUMBER` to the phone number(s) to alert — a comma-separated list, either bare 10-digit US numbers (`4155551234,3105556789`) or E.164 (`+14155551234`). Alerts go to every number; commands are only accepted from these numbers, and replies go back to whoever sent them.

### 3. Run locally

Start a local libSQL server:

```bash
docker compose up -d
```

Then configure and run the app:

```bash
cp .env.example .env   # fill in values; LIBSQL_URL defaults to http://127.0.0.1:8080
set -a && source .env && set +a
go run ./cmd/kalshi-alerts
```

To receive webhook replies locally you'll need a tunnel (e.g. `ngrok http 8080`) registered as the webhook URL on your Telnyx Messaging Profile.

### 4. Deploy to Railway
1. Create a Railway project from this repo — the included `Dockerfile` and `railway.json` are picked up automatically (health check on `GET /healthz`).
2. Add a **libSQL** database (Railway libSQL template or Turso) and set `LIBSQL_URL` and `LIBSQL_AUTH_TOKEN` on the app service. Railway can inject these when you link the database.
3. Set all required env vars (see `.env.example`). Paste the private key PEM directly into `KALSHI_API_PRIVATE_KEY` (Railway handles multiline values).
4. After the first deploy, set `https://<app>.up.railway.app/webhooks/telnyx` as the webhook URL on your Telnyx Messaging Profile.

On first boot the bot texts you the category menu; reply with numbers to start receiving alerts.

## Configuration reference

| Variable | Default | Description |
|---|---|---|
| `KALSHI_API_KEY_ID` | — | Kalshi API key ID (required) |
| `KALSHI_API_PRIVATE_KEY` | — | PEM contents; or use `KALSHI_PRIVATE_KEY_PATH` |
| `TELNYX_API_KEY` | — | Telnyx API key (required) |
| `TELNYX_FROM_NUMBER` | — | Your Telnyx SMS number, E.164 (required) |
| `TELNYX_PUBLIC_KEY` | — | Base64 Ed25519 key for webhook verification |
| `ALERT_PHONE_NUMBER` | — | Comma-separated numbers, 10-digit US or E.164 (required) |
| `MAX_PRICE_CENTS` | `30` | Alert when YES ask ≤ this (implied odds %) |
| `MIN_VOLUME` | `1000` | Minimum lifetime contract volume |
| `MIN_OPEN_INTEREST` | `500` | Minimum open interest |
| `CLOSE_WITHIN_HOURS` | `72` | Only markets closing within this window |
| `POLL_INTERVAL` | `60s` | Scan frequency (min `5s`) |
| `REALERT_DROP_CENTS` | `5` | Price drop required to re-alert a market |
| `MAX_ALERTS_PER_MESSAGE` | `5` | Digest size cap per scan cycle |
| `MIN_SUGGESTION_SCORE` | `0` | Preference-score cutoff (0 = off) |
| `LEARN_EXPAND_CATEGORIES` | `false` | Let well-liked series bypass category filter |
| `LIBSQL_URL` | — | libSQL connection URL (required) |
| `LIBSQL_AUTH_TOKEN` | — | Auth token for remote libSQL (Turso/Railway) |
| `PORT` | `8080` | HTTP port (webhook + healthz) |
| `LOG_LEVEL` | `info` | `debug` logs per-market filter/score decisions |

## Logging

Structured JSON logs on stdout via `log/slog`: every scan cycle (events/markets fetched, matches, per-reason filter counts, duration), every Kalshi and Telnyx HTTP call (status, latency), every alert, every inbound command, and all errors with context. Set `LOG_LEVEL=debug` to see why each individual market was filtered or how each suggestion was scored.
