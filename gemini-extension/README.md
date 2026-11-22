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
> The popup stores a single token used for both WebSocket sync and the HTTP
> fallback. Leave `ADMIN_TOKEN`/`WS_TOKEN` empty (recommended) or set them to the
> same value as `API_TOKEN`.

> [!IMPORTANT]
> The extension no longer scans the LAN or hardcodes server IPs. Non-loopback
> servers must use HTTPS/WSS, and raw cookies can no longer be copied to the
> clipboard.

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
