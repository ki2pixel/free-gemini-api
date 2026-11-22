# AGENTS.md — Free Gemini API

High-signal instructions and operational context for AI agents working in this repository.

---

## 1. Project Overview & Architecture
Open-source, drop-in OpenAI-compatible proxy engine in pure Go unlocking Google's Gemini 4 lineup (Hydrogen, Neon, Argon, Carbon), thinking depth controls, and Agentic Task Mode (`[40]`).

### Directory & Package Boundaries
- **`free-gemini-api/`**: Main Go server root (all build/test commands run here).
  - `api/`: REST & SSE streaming handlers, OpenAI schema conversions, auth middleware, rate-limiting, SQLite metrics/analytics, MCP and queue.
  - `gemini/`: Gemini web/client driver, cookies/session managers, output arranger (tool calls, thinking extraction, JSON repair), model registry.
  - `data/`: Local SQLite database (`gemini.db` + WAL/SHM).
- **`gemini-extension/`**: Companion Chrome extension for automated browser session & cookie syncing via WebSocket (`ws://127.0.0.1:9226`).
- **`audit-2026-11/`**: Security audit reports, CVSS registry, proof-of-concept exploits, and remediation documentation.

---

## 2. Key Developer Commands

All Go commands must be executed inside `free-gemini-api/`:

### Build & Run
```bash
cd free-gemini-api
cp -n .env.example .env   # Configure API_TOKEN in .env if not already set
go run main.go
```

### Tests & Quality Checks
```bash
cd free-gemini-api

# Run test suite (short / race)
go test -short ./...
go test -race -short ./...

# Run a specific unit test
go test -v ./gemini -run TestArrangeGeminiOutput
go test -v ./api -run TestIsToolRefusalOutput

# Linting & Formatting verification
test -z "$(gofmt -l .)"
go vet ./...
```
> **Note on Go Toolchain**: `go.mod` targets Go 1.26. If the local host has an earlier version (e.g. Go 1.18–1.24), run tests inside Docker:
> ```bash
> docker run --rm -v $(pwd)/free-gemini-api:/app -w /app golang:1.26-alpine go test -short ./...
> ```

### Docker Deployment
```bash
cd free-gemini-api
docker compose up -d --build
curl -s http://127.0.0.1:8100/health
```

---

## 3. Tech Stack & Implementation Details

- **Web Framework**: Fiber v3 (`github.com/gofiber/fiber/v3`) with HTTP/3 & QUIC support (`github.com/quic-go/quic-go`).
- **TLS Fingerprinting**: Bogdan Finn's `tls-client` and `fhttp` simulating Chrome 152 JA4 fingerprints.
- **Database & Metrics**: Pure Go SQLite (`modernc.org/sqlite`) in WAL mode tracking tokens, latency, cost savings, with dual export to JSON and Excel (`github.com/xuri/excelize/v2`).
- **Streaming & Arranger**: Real-time SSE parser separating live `<thought>` tags and auto-repairing unescaped control characters in JSON `<tool_call>` arguments.

---

## 4. Security & Operational Gotchas

- **Authentication (P0 Hardening)**: Every route except `GET /health` strictly requires authentication:
  - Header: `Authorization: Bearer $API_TOKEN` or `X-API-Token: $API_TOKEN`.
- **Default Ports**:
  - REST & SSE API: `http://127.0.0.1:8100`
  - Cookie Bridge WebSocket: `ws://127.0.0.1:9226`
- **Supported Model Aliases**:
  - `gemini-4-hydrogen` : Fast, lightweight, default free tier.
  - `gemini-4-neon` : Pro tier, reasoning frontier model.
  - `gemini-4-hydrogen-task` / `[40]` : Dedicated Agentic Task mode.
  - Thinking depth tiers : `neon-high`, `neon-medium`, `neon-low`.
