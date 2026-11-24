# 🚀 Gemini 4 Hydrogen & Neon — Unlimited Free OpenAI-Compatible API

<div align="center">

[![Flagship Everyday](https://img.shields.io/badge/Everyday_Help-Gemini_4_Hydrogen-00C853?style=for-the-badge&logo=google)](https://github.com/kodelyx/free-gemini-api)
[![Frontier Reasoning](https://img.shields.io/badge/Deeper_Analysis-Gemini_4_Neon-8A2BE2?style=for-the-badge&logo=google)](https://github.com/kodelyx/free-gemini-api)
[![Thinking Depth](https://img.shields.io/badge/Thinking_Depth-High_%7C_Medium_%7C_Low-00E5FF?style=for-the-badge)](https://github.com/kodelyx/free-gemini-api)
[![Agentic Task Mode](https://img.shields.io/badge/Agentic_Mode-Chat_%26_Task_[40]-FF6F00?style=for-the-badge)](https://github.com/kodelyx/free-gemini-api)
[![Pricing](https://img.shields.io/badge/Cost-100%25_FREE_Unlimited-00E676?style=for-the-badge)](https://github.com/kodelyx/free-gemini-api)
[![Transport](https://img.shields.io/badge/Transport-HTTP%2F3_QUIC_%2B_Pure_Go-007ACC?style=for-the-badge&logo=go)](https://github.com/kodelyx/free-gemini-api)

<p align="center">
  <b>The open-source, drop-in OpenAI-compatible proxy engine that unlocks Google's official <ins>Gemini 4 Hydrogen</ins> (Everyday Workhorse), <ins>Gemini 4 Neon</ins> (Deeper Analysis with High/Medium/Low Thinking Controls), and <ins>Agentic Task Mode</ins> — 100% Free & Unlimited.</b>
</p>

[⭐ Star this repo on GitHub](https://github.com/kodelyx/free-gemini-api) • [⚡ Quickstart](#-quickstart) • [📊 Model Matrix](#-supported-models--thinking-matrix) • [💻 Code Examples](#-code-examples) • [🔌 Integrations](#-third-party-client-integrations)

**[📚 Documentation Hub](docs/README.md)** • **[🛡️ Security Audit 2026-11](audit-2026-11/README.md)** • **[🤖 AGENTS.md](AGENTS.md)** • **[📜 CHANGELOG](CHANGELOG.md)**

</div>

---

## 🛡️ v3.5.7 — Post-Benchmark Resilience (P0 / P1 / P2)

The proxy now refuses to report success on an unusable turn. Each failure mode
observed in the in-flight benchmark is either intercepted or replayed on a fresh
upstream conversation, and any exhausted turn terminates with a truthful
`finish_reason: "error"`.

| Mechanism | Guarantee |
|---|---|
| **Strict session continuity** | A delta is sent only when the worker, the upstream conversation ID and the exact transcript prefix all match. Any mismatch replays the full transcript on a fresh conversation. |
| **RFC 6265 cookie jar** | Every cookie is registered against the single host that owns it. Host-only cookies carry no `Domain` attribute, so `console.cloud.google.com` can never overwrite a `gemini.google.com` token of the same name. |
| **Stall Guard 45 s** | A silent upstream stream is cancelled after 45 s without a byte and surfaces as `ErrUpstreamStreamStalled`. A plain transport timeout would leave the session frozen (14-minute freeze in v3.5.0). |
| **Draft 2020-12 schema validation** | Explicit tool calls are validated against `req.Tools`. Compacted schemas filter `required` against `properties` and synthesize `items` for arrays. |
| **Untagged JSON quarantine** | A bare JSON object is promoted only if it is complete, schema-valid and **non-mutating**. Any untagged mutation is stripped from the visible text and replayed. |
| **`SessionLedger` anti-S7** | A completion claim that cites an unconfirmed path, or asserts a test success without recorded validation evidence, is classified `unbacked_completion` and retried. |
| **Interrupted prose detection** | Visible fragments of 10–80 runes without terminal punctuation are retried, then terminated with `finish_reason: "error"`. |
| **Global LRU transcript budget** | 256 sessions and 64 MiB of cumulative transcripts (`SESSION_MEMORY_BUDGET_MB`). Eviction always degrades to a full replay, never to an orphan delta. |
| **Task → Chat routing** | Tool-bearing requests to `*-task` aliases are requalified to the Chat alias of the same family, preserving the external tool loop. Opt in with `X-Allow-Task-Mode-For-Tools: true`. |
| **`<thought` sanitizer** | Orphan opening and closing thought artifacts are removed from the visible text only. Complete tag citations inside fenced code blocks are preserved, and reasoning content is never altered. |

See [`docs/README.md`](docs/README.md) for the full documentation hub and
[`CHANGELOG.md`](CHANGELOG.md) for the semantic history.

---

## 🌟 What's New in Gemini 4?

Google has officially categorized its next-generation Gemini 4 lineup with specialized elemental tiers and an all-new **Agentic Task Mode**:

- 🚀 **Gemini 4 Hydrogen (`gemini-4-hydrogen` / `hydrogen`)**: Google's lightning-fast, high-throughput multimodal flagship (`is_default: true`). Engineered for everyday coding, fast conversations, and zero-latency problem-solving. **100% Free for everyone**.
- 🧠 **Gemini 4 Neon (`gemini-4-neon` / `neon`)**: The heavy-duty analytical engine for deep logic deductions, architecture designs, and multi-turn complex reasoning.
- 🎛️ **Thinking Depth Controls (Neon)**:
  - 🧠 **Neon High (`neon-high` / Level 2)**: Maximum multi-pass reasoning trace for complex math, algorithmic logic, and hard bug investigations.
  - ⚖️ **Neon Medium (`neon-medium` / Level 4)**: Balanced reasoning depth for comprehensive code reviews and analytical summaries.
  - ⚡ **Neon Low (`neon-low` / Level 1)**: Rapid self-verification with minimal overhead.
- 📋 **Chat vs Agentic Task Mode (`[0]` vs `[40]`)**: Toggle between standard reactive conversational Q&A (`[0]`) and Google's autonomous multi-step agentic planner (`[40]`) via `hydrogen-task`, `neon-task`, or header `X-Gemini-Mode: task`.
- 🔄 **Adaptive Auto (`gemini-auto` / `auto`)**: Google's smart auto-mode that dynamically adapts model capacity and reasoning to your query's complexity.
- 🛡️ **Ultra-Ready Architecture**: Background queue ready for Ultra models (**Gemini 4 Argon** & **Gemini 4 Carbon**) whenever an Ultra account is linked.

---

## 🔥 Key Highlights

| Feature | Description |
|---|---|
| **🚀 Gemini 4 Hydrogen Default** | Sub-second zero-latency responses out of the box with zero configuration needed. |
| **🧠 Gemini 4 Neon Deep Thinking** | Programmatic access to Google's frontier reasoning engine with multi-level thinking depth control (`neon-high`, `neon-medium`, `neon-low`). |
| **📋 Native Agentic Task Mode** | Supports Google's new Task mode (`[40]`) for multi-step project planning and autonomous workflows alongside standard Chat (`[0]`). |
| **🛠️ Resilient Tool Calling** | Balanced JSON extraction (`extractTaggedToolCalls`) handles malformed model output (markdown code fences, unclosed tags) during large file writes without text leaks. |
| **🔁 Multi-Turn Agent Continuity** | Preserves session prefix and conversation state across dynamic mid-stream harness notices (e.g. OpenCode tool updates) without resetting or replaying massive transcripts. |
| **🔌 Drop-In OpenAI Standard** | Connects seamlessly with **OpenCode**, **Cursor**, **Cline**, **NextChat**, **OpenWebUI**, **LangChain**, and official OpenAI Python & TypeScript SDKs. |
| **🛡️ Distributed Account Pooling** | Connect 1 to 100+ accounts with automatic least-busy load-balancing, tier segregation (Free / Pro / Ultra), and cooldown protection. |
| **🍪 Zero-Touch Cookie Bridge** | Companion Chrome extension extracts cookies directly from browser memory and syncs via local WebSocket (`ws://127.0.0.1:9226`) with 0 open tabs. |
| **⚡ Pure Go HTTP/3 QUIC** | Native TLS 1.3 fingerprint simulation (Chrome 152 JA4) with 0-RTT session resumption for minimal round-trip latency. |
| **📊 Dual Intelligence Analytics** | Real-time SQLite WAL accounting tracking tokens, latency, and estimated cost savings with direct JSON & Excel (`.xlsx`) export. |

---

## 📊 Supported Models & Thinking Matrix

| Model ID | Aliases | Thinking Depth | Mode | Tier | Role & Description |
|---|---|:---:|:---:|:---:|---|
| **`gemini-4-hydrogen`** | `hydrogen`, `4-hydrogen`, `default` | None | Chat (`[0]`) | **Free** | 🚀 **Everyday Help** — Ultra-fast balanced multimodal flagship (Default) |
| **`gemini-4-hydrogen-task`**| `hydrogen-task`, `4-hydrogen-task` | None | **Task (`[40]`)** | **Free** | 📋 **Task Mode** — Agentic multi-step task execution & planning |
| **`gemini-4-neon`** | `neon`, `4-neon`, `deeper-analysis` | None | Chat (`[0]`) | **Pro** | 💻 **Deeper Analysis** — Complex coding and multi-turn problem solving |
| **`gemini-4-neon-task`** | `neon-task`, `4-neon-task` | None | **Task (`[40]`)** | **Pro** | 📋 **Task Mode** — Deep agentic task execution & complex workflows |
| **`gemini-4-neon-thinking-high`** | `neon-high`, `thinking-high` | **High (2)** | Chat (`[0]`) | **Pro** | 🧠 **Deep Reasoning** with maximum thorough thinking steps |
| **`gemini-4-neon-thinking-medium`** | `neon-medium`, `thinking-medium` | **Medium (4)** | Chat (`[0]`) | **Pro** | ⚖️ **Balanced Reasoning** for code reviews and analysis |
| **`gemini-4-neon-thinking-low`** | `neon-low`, `thinking-low` | **Low (1)** | Chat (`[0]`) | **Pro** | ⚡ **Fast Thinking** with lightweight verification |
| **`gemini-auto`** | `auto`, `gemini-auto` | Auto | Chat (`[0]`) | **Free** | 🔄 **Adaptive Model** — Automatically adapts to your needs |
| **`gemini-4-argon`** | `argon`, `4-argon`, `gemini-4` | Low (1) | Chat (`[0]`) | **Ultra** | 🌟 **Frontier Model** for complex problem solving |
| **`gemini-4-carbon`** | `carbon`, `4-carbon` | High (2) | Chat (`[0]`) | **Ultra** | 🧪 **Long-Horizon Agent** for autonomous workflows |
| **`gemini-3.8-flash`** | `flash`, `3.8-flash` | None | Chat (`[0]`) | **Free** | ⚡ Legacy Flash alias (routes to Hydrogen) |
| **`gemini-3.1-pro`** | `pro`, `3.1-pro` | None | Chat (`[0]`) | **Pro** | 💻 Legacy Pro alias (routes to Neon) |

---

## ⚡ Quickstart

### 1. Launch the Server

```bash
git clone https://github.com/kodelyx/free-gemini-api.git
cd free-gemini-api/free-gemini-api
cp .env.example .env
# Edit .env and replace API_TOKEN with a long random value.
```

#### Option A: Native Go (Recommended)
```bash
go run main.go
```

#### Option B: Docker Compose
```bash
# HOST_UID/HOST_GID in .env should match `id -u` / `id -g`.
docker compose up -d --build
```

The server listens on:
* **`http://127.0.0.1:8100`** — OpenAI REST & Streaming API
* **`ws://127.0.0.1:9226`** — Zero-Touch Cookie Bridge

> [!IMPORTANT]
> Since the November 2026 P0 hardening, every route except `GET /health`
> requires `Authorization: Bearer $API_TOKEN` or `X-API-Token: $API_TOKEN`.
> Copy `.env.example` to `.env`, change `API_TOKEN`, and use that value in every
> client. See [`free-gemini-api/SECURITY.md`](./free-gemini-api/SECURITY.md).

---

### 2. Connect Your Account (Zero-Touch)

1. Open `chrome://extensions/` in Chrome.
2. Toggle **Developer mode** on (top-right).
3. Click **Load unpacked** and select the [`gemini-extension/`](./gemini-extension) folder.
4. Log into [gemini.google.com](https://gemini.google.com).
5. Open the extension popup, set the **Server API URL** (`http://127.0.0.1:8100`),
   **WebSocket Port** (`9226`) and **Sync Token** (`API_TOKEN` from `.env`), then
   click **Save Configuration**. The extension streams the active session
   credentials to the local API pool.

---

## 💻 Code Examples

### 1. Gemini 4 Neon Deep Thinking (Python — Official OpenAI SDK)

```python
import os
from openai import OpenAI

# Point client to your local Free Gemini API server.
# API_TOKEN is the value configured in free-gemini-api/.env
client = OpenAI(
    base_url="http://localhost:8100/v1",
    api_key=os.environ["API_TOKEN"]
)

# 1. Ask Gemini 4 Neon with High Thinking Depth
response = client.chat.completions.create(
    model="neon-high",
    messages=[
        {"role": "system", "content": "You are a world-class distributed systems engineer."},
        {"role": "user", "content": "Design a high-throughput, fault-tolerant message queue in Go with 0-copy I/O."}
    ]
)
print("🧠 Gemini 4 Neon (High Thinking):", response.choices[0].message.content)

# 2. Use Everyday Fast Model (Gemini 4 Hydrogen)
flash_resp = client.chat.completions.create(
    model="hydrogen",
    messages=[{"role": "user", "content": "Explain quantum entanglement in 1 sentence."}]
)
print("⚡ Gemini 4 Hydrogen:", flash_resp.choices[0].message.content)
```

---

### 2. Real-Time Streaming (Node.js / Bun / TypeScript)

```typescript
import OpenAI from "openai";

const openai = new OpenAI({
  baseURL: "http://localhost:8100/v1",
  apiKey: process.env.API_TOKEN,
});

async function main() {
  const stream = await openai.chat.completions.create({
    model: "neon-medium",
    messages: [{ role: "user", content: "Solve: If 5 machines make 5 widgets in 5 minutes, how long do 100 machines take to make 100 widgets?" }],
    stream: true,
  });

  for await (const chunk of stream) {
    process.stdout.write(chunk.choices[0]?.delta?.content || "");
  }
}

main();
```

---

### 3. Agentic Task Mode Execution (cURL)

Execute requests in Google's autonomous Task Mode (`[40]`) using either the model alias or header:

```bash
# Using model alias:
curl -X POST http://localhost:8100/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_TOKEN" \
  -d '{
    "model": "hydrogen-task",
    "messages": [
      {
        "role": "user",
        "content": "Create a multi-step execution plan for deploying a zero-downtime PostgreSQL cluster."
      }
    ]
  }'

# Or using the X-Gemini-Mode header:
curl -X POST http://localhost:8100/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_TOKEN" \
  -H "X-Gemini-Mode: task" \
  -d '{
    "model": "neon-high",
    "messages": [
      {
        "role": "user",
        "content": "Audit this smart contract architecture and build an automated remediation checklist."
      }
    ]
  }'
```

---

## 🛠️ Third-Party Client Integrations

| Client / Tool | Base URL | Model Name | API Key |
|---|---|---|---|
| **OpenCode** | `http://localhost:8100/v1` | `gemini-4-neon`, `neon-high`, `gemini-4-hydrogen` | `$API_TOKEN` |
| **Cursor / Cline** | `http://localhost:8100/v1` | `neon-high`, `neon`, or `hydrogen` | `$API_TOKEN` |
| **NextChat / ChatGPT-Next-Web** | `http://localhost:8100/v1` | `neon-high`, `hydrogen` | `$API_TOKEN` |
| **OpenWebUI / LibreChat** | `http://localhost:8100/v1` | `gemini-4-neon`, `gemini-4-hydrogen`, `auto` | `$API_TOKEN` |
| **LangChain / LlamaIndex** | `http://localhost:8100/v1` | `neon-high`, `hydrogen-task` | `$API_TOKEN` |

### OpenCode Configuration Example

Add the provider configuration in `~/.config/opencode/opencode.json`:

```json
{
  "providers": {
    "free-gemini": {
      "type": "openai",
      "baseURL": "http://127.0.0.1:8100/v1",
      "apiKey": "YOUR_API_TOKEN_FROM_ENV"
    }
  }
}
```

> **Note:** OpenCode strictly passes the Bearer token in requests. Set `apiKey` to match the `API_TOKEN` defined in your `.env` file.

---

## 🏛️ System Architecture

```
                 [ Cursor / Cline / OpenAI SDK / AI Agents ]
                                      │
                                      ▼
                 ┌─────────────────────────────────────────┐
                 │        Free Gemini API Server           │
                 │   (Port 8100: HTTP/3 | Port 9226: WS)   │
                 └────────────────────┬────────────────────┘
                                      │
     ┌────────────────────────────────┼────────────────────────────────┐
     ▼                                ▼                                ▼
┌─────────────────────┐   ┌─────────────────────┐   ┌─────────────────────┐
│   Model Registry    │   │  Mode Controller    │   │   Output Arranger   │
│ - gemini-4-hydrogen │   │ - Chat Mode   ([0]) │   │  (Native Tool Calls │
│ - gemini-4-neon     │   │ - Task Mode  ([40]) │   │   & Sanitization)   │
│ - neon-high (lvl 2) │   └─────────────────────┘   └─────────────────────┘
│ - gemini-auto       │
└──────────┬──────────┘
           │
           ▼
┌────────────────────────────────────────┐
│     Multi-Account Worker Pool          │
│  - Free Workers    (Hydrogen & Auto)   │
│  - Pro Workers     (Neon & Thinking)   │
│  - Ultra Workers   (Argon & Carbon)    │
└──────────────────┬─────────────────────┘
                   │
    ┌──────────────┴──────────────┐
    ▼                             ▼
┌──────────────┐           ┌──────────────┐
│ Google Cloud │           │  Chrome Ext  │
│ Backend API  │           │ (WebSocket)  │
└──────────────┘           └──────────────┘
```

---

## 📡 API Reference & Endpoints

Every route except `GET /health` requires `Authorization: Bearer $API_TOKEN` or
`X-API-Token: $API_TOKEN`. `[Admin]` marks routes that accept **only**
`ADMIN_TOKEN` when that variable is set (otherwise they fall back to
`API_TOKEN`). Source of truth: [`free-gemini-api/api/routes.go`](free-gemini-api/api/routes.go).

| Method | Endpoint | Auth | Description |
|---|---|---|:---:|---|
| `POST` | `/v1/chat/completions` | API | OpenAI-compatible chat & streaming completions (Models: `hydrogen`, `neon-high`, `auto`, etc.) |
| `GET` | `/v1/models` | API | List all dynamic models, aliases, tiers, and thinking levels |
| `POST` | `/v1/models` | **[Admin]** | Register custom model hashes & mode IDs dynamically at runtime |
| `POST` | `/v1/models/discover` | **[Admin]** | Autonomously probe Google backend for newly rolled out models |
| `GET` | `/v1/workers` | API | Real-time worker pool metrics, active account tiers & throughput |
| `GET` | `/api/pool` | API | Alias of `/v1/workers` |
| `POST` | `/api/sync-cookies` | **[Admin]** | Direct HTTP cookie sync fallback for the Chrome extension |
| `GET` | `/metrics` | API | Prometheus metrics (workers, cookies, HTTP, tokens, classifier counters). Loopback-only unauthenticated scrape when `PUBLIC_METRICS=true` |
| `POST` | `/chat` | API | Unified multimodal chat (text, images, MP4 screen recordings, reference videos) |
| `POST` | `/music` | API | AI music generation via Gemini Lyria |
| `POST` | `/reset` | API | Reset an active agent/user session |
| `GET` | `/status` | API | Active session status, initialization flag & upstream conversation ID |
| `GET` | `/history` | API | Query conversation history (`?conversation_id=…&limit=…`) |
| `GET` | `/history/search` | API | Full-text search across stored conversations (`?q=…&limit=…`) |
| `DELETE` | `/history/:conversation_id` | API | GDPR art. 17 deletion. Authenticated, idempotent, audited (`audit action=gdpr_delete`) |
| `GET` | `/media` | API | 50 most recent media generations (type, prompt, file, URL) |
| `GET` | `/stats` | API | SQLite token accounting, messages count & financial ROI savings |
| `GET` | `/v1/analytics` | API | Machine-readable JSON analytics report (5 req/min) |
| `GET` | `/export/json` | API | JSON analytics export (5 req/min) |
| `GET` | `/export/excel` | API | Download comprehensive multi-sheet Excel report (`.xlsx`, 5 req/min) |
| `GET` | `/output/*` | API | Generated media files, confined to `./output` by `safeOutputPath` |
| `GET` | `/health` | **Public** | Live cookie health, TTL inspector & extension status |
| `GET` | `/help` | API | Terminal developer CLI manual (`?format=json` for JSON) |
| `GET` | `/` | API | Same CLI manual. Public when `PUBLIC_HELP=true` |

### Standard parameters & headers

| Name | Type | Effect |
|---|---|---|
| `reasoning_effort` | body field | Maps an OpenAI reasoning level onto a thinking variant by **name**, without mutating shared model config. Explicit aliases (`neon-high`, …) take precedence. |
| `X-Gemini-Mode: task` | header | Requests Task Mode `[40]` instead of Chat `[0]`. |
| `X-Allow-Task-Mode-For-Tools: true` | header | Keeps Task Mode `[40]` on tool-bearing requests. Default is to requalify to Chat `[0]`. Server-wide equivalent: `ALLOW_TASK_MODE_FOR_TOOLS=true`. |
| `X-Agent-ID: <id>` | header | Sticky affinity binding: keeps an agent thread on the same account. |
| `X-Session-ID: <id>` | header | Custom session identifier. |
| `X-Conversation-ID: <id>` | header | Gemini server-side thread continuation. |

### Antigravity Hub aliases

Upstream `gemini-4-<family>-<tier>` identifiers are registered as aliases of the
canonical `gemini-4-<family>-thinking-<tier>` models:

| Upstream ID | Canonical ID | `ThinkingLevel` |
|---|---|---|
| `gemini-4-argon-high` | `gemini-4-argon-thinking-high` | 2 |
| `gemini-4-argon-medium` | `gemini-4-argon-thinking-medium` | 4 |
| `gemini-4-argon-low` | `gemini-4-argon-thinking-low` | 1 |
| `gemini-4-neon-high` | `gemini-4-neon-thinking-high` | 2 |
| `gemini-4-neon-medium` | `gemini-4-neon-thinking-medium` | 4 |
| `gemini-4-neon-low` | `gemini-4-neon-thinking-low` | 1 |
| `gemini-4-hydrogen-high` | `gemini-4-hydrogen-thinking-high` | 2 |
| `gemini-4-hydrogen-medium` | `gemini-4-hydrogen-thinking-medium` | 4 |
| `gemini-4-hydrogen-low` | `gemini-4-hydrogen-thinking-low` | 1 |

---

## ⚙️ CLI Tools

```bash
./goapi                     # Start the API server daemon (Port 8100 & WS 9226)
./goapi --help              # Show terminal CLI developer manual
./goapi --stats             # Print real-time database usage & cost saved
./goapi --export out.xlsx   # Export detailed spreadsheet analytics
./goapi --mcp               # Run as Model Context Protocol (MCP) server
```

---

## 🔒 Security & Privacy

- **Mandatory authentication**: every route except `GET /health` requires
  `Authorization: Bearer $API_TOKEN`. An empty `API_TOKEN` returns `401`
  everywhere instead of silently opening the API. A separate `ADMIN_TOKEN`
  gates the three administrative mutations.
- **AES-256-GCM at rest**: setting `COOKIE_ENCRYPTION_KEY` encrypts
  `cookies/account_*.json` and session caches before their atomic write.
  `REQUIRE_COOKIE_ENCRYPTION=true` refuses to start without a key.
- **Per-subdomain cookie scoping**: the RFC 6265 jar registers each cookie
  against the single host that owns it. Host-only cookies never reach a sibling
  Google subdomain. See
  [`docs/architecture/cookie_scoping_rfc6265.md`](docs/architecture/cookie_scoping_rfc6265.md).
- **Zero hardcoded secrets**: credentials and cookies live strictly in local
  memory and local `0600` files; nothing is sent to third parties other than
  Google for inference.
- **Local network isolation**: WebSocket discovery and cookie sync happen
  strictly over loopback or your local private LAN. The extension no longer
  scans the LAN.
- **Safe version control**: `.gitignore` prevents Google session tokens, cookie
  jars, SQLite databases and generated media from being committed.

**Audit posture (2026-11-22):** maturity score **74.25/100**, GO for local
mono-user deployment, NO-GO maintained for public or multi-tenant exposure. See
[`audit-2026-11/RAPPORT_POST_AUDIT.md`](audit-2026-11/RAPPORT_POST_AUDIT.md),
[`free-gemini-api/SECURITY.md`](free-gemini-api/SECURITY.md),
[`free-gemini-api/THREAT_MODEL.md`](free-gemini-api/THREAT_MODEL.md) and
[`free-gemini-api/PRIVACY.md`](free-gemini-api/PRIVACY.md).

---

## 🌟 Support & Community

If you find **Free Gemini API** helpful, please give this repository a ⭐ **Star** to support ongoing open-source development and help other developers discover Google's latest Gemini 4 models!

---

## 📄 License

MIT License. Built for the developer, hacker, and AI research community.
