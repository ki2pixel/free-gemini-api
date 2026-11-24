# 🧩 Gemini Api Agent (Chrome Extension)

A lightweight, automated Chrome Extension (Manifest V3) that automatically syncs active Google Gemini session cookies to your local **[Free Gemini API Server](https://github.com/kodelyx/free-gemini-api)** via real-time WebSockets.

---

## 🚀 Quick Setup & Installation

1. **Download / Clone this repository:**
   ```bash
   git clone https://github.com/kodelyx/gemini-extension.git
   ```
2. Open Google Chrome and navigate to:
   ```text
   chrome://extensions/
   ```
3. Enable **Developer mode** (toggle switch in the top right corner).
4. Click **Load unpacked** and select the `gemini-extension` folder.
5. Log in to [gemini.google.com](https://gemini.google.com).
6. Open the extension popup and configure:
   - **Server API URL** (default `http://127.0.0.1:8100`)
   - **WebSocket Port** (default `9226`)
   - **Sync Token** — the `API_TOKEN` (or `WS_TOKEN`) from the server `.env`
7. Click **Save Configuration**. The extension then streams active session
   cookies to the local API server in real time.

> [!NOTE]
> The popup stores a **single token** used for both WebSocket sync and the HTTP
> fallback. The recommended configuration leaves `ADMIN_TOKEN` and `WS_TOKEN`
> empty on the server, so both fall back to `API_TOKEN`:
>
> | Server variable | Empty value falls back to | Used by |
> |---|---|---|
> | `API_TOKEN` | — (mandatory) | every HTTP route, including `POST /api/sync-cookies` |
> | `WS_TOKEN` | `API_TOKEN` | WebSocket bridge handshake (`?token=…`) |
> | `ADMIN_TOKEN` | `API_TOKEN` | `POST /v1/models`, `POST /v1/models/discover`, `POST /api/sync-cookies` |
>
> If you set `WS_TOKEN` or `ADMIN_TOKEN` to a distinct value, the extension's
> single token can only satisfy one of them. Either keep them empty or align
> them with `API_TOKEN`.

> [!IMPORTANT]
> The extension no longer scans the LAN or hardcodes server IPs. Non-loopback
> servers must use HTTPS/WSS, and raw cookies can no longer be copied to the
> clipboard.

---

## 🍪 Cookie scoping and subdomain isolation

Chrome exports carry the **same cookie names** (`OSID`, `__Secure-OSID`,
`COMPASS`) for `gemini.google.com`, `myaccount.google.com` and
`console.cloud.google.com`, with **different values**. The server does not
concatenate them blindly:

- it uses an **RFC 6265 jar** backed by the public suffix list;
- each cookie is registered against the **single host that owns it**;
- a **host-only** cookie is stored without a `Domain` attribute, so it never
  reaches a sibling Google host;
- the `Cookie` header is resolved **live from the jar** on every request, so
  rotated tokens (`__Secure-1PSIDTS`, `__Secure-3PSIDTS`) are sent immediately.

Consequence: the subscription path (Neon and thinking tiers) keeps the
Gemini-scoped `OSID` / `__Secure-OSID` / `COMPASS` set instead of a
`console.cloud.google.com` value that would cause a `401`.

Full specification and regression tests:
[`../docs/architecture/cookie_scoping_rfc6265.md`](../docs/architecture/cookie_scoping_rfc6265.md).

> [!TIP]
> The HTTP fallback `POST /api/sync-cookies` accepts an optional `profile_id`.
> Sending it names the file `account_profile_<N>.json` instead of deriving the
> name from a rotating cookie hash, which otherwise mints a duplicate worker for
> the same Google account on nearly every refresh. The Chrome extension does not
> send it and keeps the hash-based naming.

---

## ⚡ Features

- **Multi-Account Support:** Automatically detects and manages multiple logged-in Google accounts.
- **WebSocket Real-Time Sync:** Instant zero-delay cookie sync on login, rotation, or session refresh.
- **Background Keepalive:** Runs seamlessly in the background without slowing down your browser.
- **Explicit Configuration:** Backend URL and token are stored in `chrome.storage.local`; no LAN discovery or hardcoded IPs.
- **Minimal Permissions:** Only Google domains and loopback backend origins are requested.

---

## 🔗 Related Repositories

- 🚀 **[Free Gemini API Server (Go + Needle 2 SLM + SQLite)](https://github.com/kodelyx/free-gemini-api)**
- 🐍 **[Gemini Engine Python SDK](https://github.com/kodelyx/free-gemini-api)**
