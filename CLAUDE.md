# CLAUDE.md

ВКурилке: a Telegram bot plus Mini App that shows who is in the smoking area right now. It runs as one Go process with SQLite and the frontend embedded in the binary. There is no CGO, no Redis, no Node at runtime, and only one replica.

## Global rule 

 - **Caveman Mode:** Be extremely concise, direct, and zero-fluff. Skip pleasantries and long explanations.
 - **Karpathy Mode:** Make minimal diffs. Do not refactor or reformat unrelated code around your changes.

## Commands

```bash
make web      # npm ci + build web/dist (REQUIRED before any go command on a fresh clone)
make check    # tsc, go vet, go test -race ./...  (what CI runs, plus `make build` and docker build)
make build    # bin/vkurilke, CGO_ENABLED=0, static assets embedded
make run      # build and run

go test ./internal/store -run TestOneActiveRoomAndIdempotentStatus   # single test
npm --prefix web run dev     # Vite dev server, proxies /api to 127.0.0.1:8080
npm --prefix web run check   # TypeScript only
```

- `web/embed.go` has `//go:embed all:dist`. Without `web/dist`, every `go build/vet/test` fails with `pattern all:dist: no matching files found`.
- `-race` needs a system C compiler. Normal builds do not.
- The binary reads env vars, not `.env`. To load them: `set -a; . ./.env; set +a`. Required: `BOT_TOKEN`, and `BASE_URL`, which must be an HTTPS origin with no path. Optional: `PORT`, `DB_PATH`, `ADMIN_IDS`, `CHECK_INTERVAL_MINUTES`, `ANSWER_TIMEOUT_MINUTES`. See `internal/config/config.go`.
- There is no auth bypass. The Mini App needs real Telegram `initData`, so test end to end with a separate test bot and an HTTPS tunnel.

## Architecture

```text
cmd/app/main.go      wiring: config → store → bot.Setup → HTTP server + 3 goroutines (bot poll, timers, delivery)
internal/config/     env parsing and validation
internal/api/        chi router, initData HMAC auth (auth.go), REST handlers, error → HTTP status mapping
internal/store/      all SQLite access and business rules: roles, statuses, sessions, outbox
  schema.sql         embedded, idempotent CREATE IF NOT EXISTS, PRAGMA user_version=1
internal/bot/        Telegram Bot API client, long polling, commands/callbacks, message rendering
internal/worker/     RunTimers (Store.Tick every 10s), RunDelivery (outbox → Telegram, retries/backoff)
web/                 strict TypeScript without a framework, Tailwind v4, Vite; embed.go serves dist/
docs/                DEPLOY.md, BOTHOST.md, API.md, DECISIONS.md
```

How a request flows. HTTP or a bot callback calls a `store` method. That method validates access, changes state, and enqueues `outbox` jobs in **one transaction**. `worker.RunDelivery` then picks up each job, `Store.Prepare` builds a `Delivery` (or skips it), `bot.Deliver` sends it or edits the existing session card, and `CompleteJob`/`RetryJob`/`BlockBot` records the result.

## Rules that are easy to break

- **Product behavior is defined by `docs/DECISIONS.md`.** Read it before changing statuses, sessions, notifications or roles. If behavior changes, update that file too.
- **Never call Telegram inside a store transaction.** The DB has `SetMaxOpenConns(1)`, so transactions must stay short. Network I/O belongs in the worker or the bot.
- **Everything must be idempotent.** The bot saves its `getUpdates` offset after handling each update, and outbox delivery can repeat after a crash. Status changes, callbacks, and confirmations must tolerate replays. Jobs use `dedupe_key` and `revision`.
- **Access checks go in the same transaction as the write** (see `requireMember`). User IDs come from the auth session and are never taken from the request body.
- **Errors:** return the typed `store.Err*` values. `api.respond` and `bot.publicError` map them to user text and status codes. Any other error is logged and reaches the user only as a generic message, so internal details never leak.
- **Schema changes:** `schema.sql` only runs `CREATE ... IF NOT EXISTS`, so editing a table definition does **not** migrate existing databases. You must add explicit migration code in `store.Open` and bump `user_version`. `Open` already refuses to open a DB whose version is newer.
- **Time:** the store uses `Options.Now` and the API uses `Server.Now`, so tests inject a fixed clock. All timestamps are Unix seconds UTC.
- **Frontend:** every piece of user data put into HTML must go through `escape()` in `web/src/main.ts`. Do not add a framework. The app polls `/api/status` every 5s, and only while the screen is open.

## Conventions

- Go: `gofmt`, standard library first. A new dependency needs justification in the PR.
- SQL is always parameterized.
- All UI text, bot messages, and `store.Err*` messages are in **Russian** and use the informal «ты».
- Tests sit next to the code, e.g. `internal/store/store_test.go` (the `setup(t)` fixture gives users 1–4 and a room «Общага»), `internal/api/server_test.go` (`signedData` fakes initData), and `internal/bot`/`internal/worker` tests (stub `http.RoundTripper`). New behavior needs a test.
- Commits follow Conventional Commits (`feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `chore:`). The default branch is `master`.
- Out of scope unless an issue agrees first: Redis, a separate Node server, CGO, horizontal scaling.

## Deployment

The `Dockerfile` has three stages (node → go → alpine). `docker-entrypoint.sh` fixes ownership of the data dir when started as root, then drops to the `app` user (uid 10001). Health check: `GET /healthz`. On a VPS, `docker compose --profile https up -d --build` also starts Caddy. On Bothost, use `DB_PATH=/app/data/vkurilke.db`. Details are in `docs/DEPLOY.md` and `docs/BOTHOST.md`.
