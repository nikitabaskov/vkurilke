# Repository Guidelines

## Project Structure & Module Organization

ВКурилке is a Telegram bot and Mini App running as one Go process with SQLite and embedded frontend assets.

- `cmd/app/`: startup, shutdown, and HTTP server wiring.
- `internal/`: `api` (authentication/REST), `store` (SQLite/business rules), `bot` (Telegram), `worker` (timers/delivery), and `config` (environment).
- `web/src/`: TypeScript and CSS; Vite builds `web/dist/`, embedded by `web/embed.go`.
- Tests live beside Go source in `*_test.go`; `docs/` contains API, deployment, and product documentation.

## Build, Test, and Development Commands

Use Go 1.25+ and Node.js 22.12+. Race tests require a system C compiler.

- `make web`: install locked npm dependencies and type-check/build frontend assets. Run before standalone Go checks on fresh clones; embedding requires `web/dist`.
- `make build`: build `bin/vkurilke` with embedded assets and CGO disabled.
- `make run`: build and launch the application.
- `make test`: build assets and run `go test ./...`.
- `make check`: build assets, check TypeScript, run `go vet` and race tests.
- `npm --prefix web run dev`: start Vite, proxying `/api` to port 8080.

## Coding Style & Naming Conventions

Use `gofmt` (tabs), idiomatic Go names, and typed `store.Err*` errors. Parameterize SQL and check authorization within the write transaction. Justify new dependencies.

Keep TypeScript strict and framework-free; follow existing two-space indentation, camelCase functions, and PascalCase types. Escape user data inserted into HTML with `escape()`. Write interface and bot text in Russian using informal «ты».

## Testing Guidelines

Use Go's `testing` package with `TestBehavior` names. Cover new behavior; no numeric coverage threshold is configured. Run focused tests with `go test ./internal/store -run TestOneActiveRoomAndIdempotentStatus`. Verify Mini App flows using a test bot and HTTPS endpoint with real Telegram authentication.

## Commit & Pull Request Guidelines

History follows Conventional Commits, including `feat:`, `fix:`, `build:`, and `docs:`. Put each independent fix in its own commit, including fixes completed in the same working session. Keep each PR focused; use the PR template to explain what, why, and verification. Run `make check`; CI also builds the binary and Docker image. Discuss product changes in an issue and update `docs/DECISIONS.md`.

## Configuration & Agent Instructions

Copy `.env.example`; export variables before running locally—the binary does not load `.env`. Keep tokens and databases untracked.

Agents: follow `@/home/nikita/.codex/RTK.md`; prefix shell commands with `rtk`. Prefer MCP graph search, tracing, snippets, queries, and architecture tools for code discovery; use text search for non-code, literals, or insufficient graph results.
