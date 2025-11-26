# Repository Guidelines

## Project Structure & Module Organization
- Core packages live under `api/`, `config/`, `indicators/`, `order/`, `position/`, `strategy/`, `daemon/`, `logging/`, and `models/`; `internal/` holds shared utilities.
- Entrypoint: `main.go`; ignore generated binaries such as `go-trade`.
- Tests sit beside code in each package (for example, `indicators/indicators_test.go`).
- Configuration defaults live in `config/config.go` and are driven by environment variables (see Security below).

## Build, Test, and Development Commands
- Build: `go build -o go-trade ./...` produces the CLI binary.
- Run in debug: `go run main.go -debug` (or execute `./go-trade -debug` after building).
- Tests: `go test ./...` for the full suite; `go test ./indicators` to scope to a package; coverage via `go test -cover ./...`.
- Dependencies: `go mod tidy` to sync modules when adding imports.

## Coding Style & Naming Conventions
- Go 1.23+ with standard `gofmt` formatting; run `gofmt -w .` before pushing. Use `go vet ./...` on meaningful changes.
- Prefer clear names: packages lower_snake (`indicators`), exported types/functions in `CamelCase`, locals in `mixedCase`. Avoid abbreviations unless industry-standard (API, WS, TP/SL).
- Keep configuration centralized in `config/`; avoid hardcoding credentials or URLs elsewhere.

## Testing Guidelines
- Framework: Go’s built-in `testing` package with table-driven tests where practical.
- Naming: place tests in `*_test.go` with `TestXxx` functions that describe behavior (e.g., `TestVolumeWeightedPrice`). Cover boundary cases for order book math and indicator windows.
- Keep tests deterministic and offline—mock or stub any network-facing logic instead of hitting Bybit endpoints.

## Commit & Pull Request Guidelines
- Commit messages: concise, present tense; optional type prefix is acceptable (`feat:`, `fix:`, `chore:`), but keep the subject under ~72 characters.
- Before opening a PR, run `gofmt`, `go test ./...`, and note any config changes. Avoid committing generated binaries or log files (`go-trade`, `trading_bot*.log`).
- PR description should cover: summary of behavior change, affected packages, configuration/env expectations, and manual verification steps (commands run). Link related issues when available.

## Security & Configuration Tips
- Secrets are read from env vars: `BYBIT_API_KEY`, `BYBIT_API_SECRET`, and optional endpoints (`BYBIT_DEMO_REST_HOST`, `BYBIT_DEMO_WS_PRIVATE`, `BYBIT_DEMO_WS_PUBLIC`). Defaults target Bybit demo hosts.
- Do not log or commit credentials. Use a local `.env` (untracked) when developing; rotate keys regularly.
- Logging rotates via Lumberjack defaults in `config/config.go`; keep log levels and files configurable rather than hardcoded in new code.
