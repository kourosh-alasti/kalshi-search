# Kalshi SMS Alert Bot

A Go service that continuously scans [Kalshi](https://kalshi.com) markets for contracts with low implied odds (default: YES buyable at ≤ 30¢, i.e. ≤ 30% implied probability with a ~3.3x+ payout), texts you each opportunity via [Telnyx](https://telnyx.com) or [Twilio](https://twilio.com), and learns from which suggestions you act on to send more trades like the ones you take.

No web UI — everything is configured through environment variables and controlled by texting the bot back.

## How it works

- **Scanner loop** polls Kalshi's Trade API v2 (read-only, RSA-PSS signed requests) on `POLL_INTERVAL`, filters open markets by price, volume, open interest, close time, and your enabled categories, ranks the survivors with a preference model, and texts the top picks as one digest SMS with `kalshi.com` links that open the Kalshi app on your phone.
- **SMS commands**: the first message you receive includes a short-lived link to choose categories and subcategories. Reply `TOOK 12` / `PASS 12` to give feedback on suggestion #12, `STATUS`, `PAUSE`, or `RESUME`. Use `PREFS` for a fresh preferences link, `WATCH TICKER` / `UNWATCH TICKER` for ticker alerts, `QUIET 22-8` with `ENABLE`/`DISABLE` to control quiet hours, and `WHY 12` to see why a pick was suggested.
- **Learning**: every suggestion decomposes into features (category, series, price band, close-time bucket, volume bucket) with accept/reject counts per user. Feedback comes from your `TOOK`/`PASS` replies and automatically from your portfolio — if a new position appears in a suggested market, it counts as accepted. All suggestions and labels are stored in libSQL for future ML training (see the plan's "Future: ML-based learning").
- **Dedup**: each market alerts at most once per user; it re-alerts only if the price drops by `REALERT_DROP_CENTS`. `PASS`ed markets never alert again.
- **Persistence**: all state is stored in [libSQL](https://github.com/tursodatabase/libsql) keyed by phone number. Each alert recipient has independent categories, preferences, and suggestion history.

## Setup

### 1. Kalshi API key
Create an API key at kalshi.com → Profile Settings → API Keys. Set `KALSHI_API_KEY_ID` and either paste the PEM into `KALSHI_API_PRIVATE_KEY` or point `KALSHI_PRIVATE_KEY_PATH` at the file (locally it defaults to `certificates/kalshi-private-key.pem`). Read access is sufficient.

### 2. Notification channel

Set `NOTIFY_CHANNEL` to `sms` (default) or `email`. Only one channel can be active — set either `ALERT_PHONE_NUMBER` or `ALERT_EMAIL`, not both.

#### SMS (`NOTIFY_CHANNEL=sms`)

Set `SMS_PROVIDER` to `telnyx` (default) or `twilio`.

#### Telnyx
1. Create an API key in the [Telnyx Mission Control Portal](https://portal.telnyx.com) → API Keys → `TELNYX_API_KEY`.
2. Buy an SMS-capable number and assign it to a Messaging Profile → `TELNYX_FROM_NUMBER` (E.164, `+1...`).
3. On that Messaging Profile, set the inbound webhook URL to `https://<your-app>/webhooks/telnyx` (API v2 format).
4. Copy your account public key (Account Settings → Keys & Credentials → Public Key) → `TELNYX_PUBLIC_KEY`; it's used to verify webhook signatures.

#### Twilio
1. Copy your Account SID and Auth Token from the [Twilio Console](https://console.twilio.com) → `TWILIO_ACCOUNT_SID`, `TWILIO_AUTH_TOKEN`.
2. Buy an SMS-capable number → `TWILIO_FROM_NUMBER` (E.164, `+1...`).
3. On that number's Messaging Configuration, set the inbound webhook URL to `https://<your-app>/webhooks/twilio` (HTTP POST).
4. Set `TWILIO_WEBHOOK_URL` to that same full public URL — Twilio's `X-Twilio-Signature` is validated against it.

For both SMS providers, set `ALERT_PHONE_NUMBER` to the phone number(s) to alert — a comma-separated list, either bare 10-digit US numbers (`4155551234,3105556789`) or E.164 (`+14155551234`). Alerts go to every number; commands are only accepted from these numbers, and replies go back to whoever sent them.

#### Email (`NOTIFY_CHANNEL=email`)

While waiting for 10DLC campaign approval, you can receive alerts by email instead of SMS:

1. Create an account at [UseSend](https://usesend.com) and verify your sending domain.
2. Create an API key → `USESEND_API_KEY`.
3. Set `USESEND_FROM_EMAIL` to your verified sender address (e.g. `alerts@yourdomain.com`).
4. Set `ALERT_EMAIL` to the recipient address(es), comma-separated (same pattern as `ALERT_PHONE_NUMBER`).
5. Optionally set `USESEND_SUBJECT` (default `Kalshi Alerts`) and `USESEND_REPLY_TO`.

Email mode sends alert digests via UseSend. Onboarding uses the same short-lived preferences link as SMS.

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

To receive webhook replies locally you'll need a tunnel (e.g. `ngrok http 8080`) registered as the webhook URL on your Telnyx Messaging Profile or Twilio phone number. Set `PUBLIC_BASE_URL` to the same tunnel URL so onboarding links work on your phone.

### 4. Deploy to Railway
1. Create a Railway project from this repo — the included `Dockerfile` and `railway.json` are picked up automatically (health check on `GET /healthz`).
2. Add a **libSQL** database (Railway libSQL template or Turso) and set `LIBSQL_URL` and `LIBSQL_AUTH_TOKEN` on the app service. Railway can inject these when you link the database.
3. Set all required env vars (see `.env.example`). Paste the private key PEM directly into `KALSHI_API_PRIVATE_KEY` (Railway handles multiline values).
4. After the first deploy, set `https://<app>.up.railway.app/webhooks/telnyx` or `https://<app>.up.railway.app/webhooks/twilio` as the webhook URL on your SMS provider (and set `TWILIO_WEBHOOK_URL` to match if using Twilio).

On first boot the bot sends you a short-lived link to choose categories and subcategories; alerts begin after you save your preferences.

## Configuration reference

| Variable | Default | Description |
|---|---|---|
| `KALSHI_API_KEY_ID` | — | Kalshi API key ID (required) |
| `KALSHI_API_PRIVATE_KEY` | — | PEM contents; or use `KALSHI_PRIVATE_KEY_PATH` |
| `NOTIFY_CHANNEL` | `sms` | `sms` or `email` (mutually exclusive with the other channel's recipient var) |
| `SMS_PROVIDER` | `telnyx` | `telnyx` or `twilio` (when `NOTIFY_CHANNEL=sms`) |
| `TELNYX_API_KEY` | — | Telnyx API key (required when `SMS_PROVIDER=telnyx`) |
| `TELNYX_FROM_NUMBER` | — | Your Telnyx SMS number, E.164 (required when `SMS_PROVIDER=telnyx`) |
| `TELNYX_PUBLIC_KEY` | — | Base64 Ed25519 key for Telnyx webhook verification |
| `TWILIO_ACCOUNT_SID` | — | Twilio account SID (required when `SMS_PROVIDER=twilio`) |
| `TWILIO_AUTH_TOKEN` | — | Twilio auth token (required when `SMS_PROVIDER=twilio`) |
| `TWILIO_FROM_NUMBER` | — | Your Twilio SMS number, E.164 (required when `SMS_PROVIDER=twilio`) |
| `TWILIO_WEBHOOK_URL` | — | Full public webhook URL for Twilio signature verification |
| `ALERT_PHONE_NUMBER` | — | Comma-separated numbers when `NOTIFY_CHANNEL=sms` |
| `ALERT_EMAIL` | — | Comma-separated addresses when `NOTIFY_CHANNEL=email` |
| `USESEND_API_KEY` | — | UseSend API key (required when `NOTIFY_CHANNEL=email`) |
| `USESEND_FROM_EMAIL` | — | Verified sender address (required when `NOTIFY_CHANNEL=email`) |
| `USESEND_BASE_URL` | `https://app.usesend.com/api/v1` | UseSend API base URL |
| `USESEND_SUBJECT` | `Kalshi Alerts` | Subject line for alert emails |
| `USESEND_REPLY_TO` | — | Comma-separated reply-to addresses |
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
| `PORT` | `8080` | HTTP port (webhook + healthz + onboarding) |
| `PUBLIC_BASE_URL` | — | Public app URL for onboarding links (required) |
| `ONBOARDING_TOKEN_TTL` | `24h` | How long preference links stay valid |
| `SCAN_CYCLE_TIMEOUT` | `4m` | Max duration for one scan cycle |
| `EVENTS_CACHE_TTL` | `30s` | Reuse Kalshi event list between cycles |
| `PASS_SUPPRESS_DAYS` | `30` | Days a `PASS` suppresses a market (0 = forever) |
| `MAX_PER_SERIES_DIGEST` | `2` | Max picks per series in one digest |
| `LINK_SIGNING_SECRET` | — | HMAC secret for email action links (required when `ENABLED=true`) |
| `LINK_TTL` | `168h` | Signed feedback/toggle link lifetime |
| `INSTANCE_ID` | auto | Unique id for leader election |
| `LEADER_ELECTION` | `true` | Only one instance runs the scanner when multiple replicas deploy |
| `METRICS_ENABLED` | `true` | Expose Prometheus metrics on `GET /metrics` |
| `LOG_LEVEL` | `info` | `debug` logs per-market filter/score decisions |

## Logging

Structured JSON logs on stdout via `log/slog`: every scan cycle (events/markets fetched, matches, per-reason filter counts, duration), every Kalshi and SMS HTTP call (status, latency), every alert, every inbound command, and all errors with context. Set `LOG_LEVEL=debug` to see why each individual market was filtered or how each suggestion was scored.

`GET /healthz` returns JSON with the last scan cycle stats, inbound queue depth, and leader status. `GET /metrics` exposes Prometheus metrics when `METRICS_ENABLED=true`.
