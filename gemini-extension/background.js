/**
 * Free Gemini API Sync — Ultra-Lightweight Background Service Worker
 * Pure memory-based zero-tab cookie synchronizer.
 *
 * Security model (P0):
 *  - No LAN discovery, no hardcoded IP list, no subnet sweep.
 *  - Backend URL, WS port and token come from chrome.storage.local.
 *  - Non-loopback backends must use HTTPS/WSS.
 *  - Cookies are only collected from Google domains and allowlisted names.
 *  - Raw cookie export to the clipboard was removed.
 */

const DEFAULT_SERVER_URL = 'http://127.0.0.1:8100';
const DEFAULT_WS_PORT = '9226';

let ws = null;
let lastSyncTime = null;
let hasSyncedOnce = false;
let syncDebounceTimeout = null;

chrome.runtime.onInstalled.addListener(init);
chrome.runtime.onStartup.addListener(init);

chrome.alarms.onAlarm.addListener(async (alarm) => {
  if (alarm.name === 'reconnect') connectToBackend();
  if (alarm.name === 'keepAlive') keepAlive();
});

chrome.storage.onChanged.addListener((changes, area) => {
  if (area !== 'local') return;
  if (changes.serverUrl || changes.wsPort || changes.wsToken) {
    closeSocket();
    connectToBackend();
  }
});

async function getConfig() {
  const data = await chrome.storage.local.get(['serverUrl', 'wsPort', 'wsToken', 'lastSyncTime']);

  let serverUrl = String(data.serverUrl || DEFAULT_SERVER_URL).trim().replace(/\/+$/, '');
  if (!/^https?:\/\//i.test(serverUrl)) serverUrl = DEFAULT_SERVER_URL;

  let wsPort = String(data.wsPort || DEFAULT_WS_PORT).trim();
  if (!/^\d{1,5}$/.test(wsPort)) wsPort = DEFAULT_WS_PORT;

  const wsToken = String(data.wsToken || '').trim();
  if (data.lastSyncTime) lastSyncTime = data.lastSyncTime;

  return { serverUrl, wsPort, wsToken };
}

function isLoopbackHost(host) {
  const normalized = String(host || '').toLowerCase().replace(/^\[|\]$/g, '');
  return normalized === '127.0.0.1' || normalized === 'localhost' || normalized === '::1';
}

function validateConfig(config) {
  let parsed;
  try {
    parsed = new URL(config.serverUrl);
  } catch (_) {
    return { ok: false, error: 'Invalid server URL' };
  }

  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    return { ok: false, error: 'Server URL must use http:// or https://' };
  }
  if (!isLoopbackHost(parsed.hostname) && parsed.protocol !== 'https:') {
    return { ok: false, error: 'Non-loopback servers must use HTTPS' };
  }
  if (!config.wsToken) {
    return { ok: false, error: 'Sync token is required' };
  }

  return { ok: true, parsed };
}

function buildWsUrl(config, parsed) {
  const scheme = parsed.protocol === 'https:' ? 'wss' : 'ws';
  return `${scheme}://${parsed.hostname}:${config.wsPort}/?token=${encodeURIComponent(config.wsToken)}`;
}

async function init() {
  await getConfig();
  connectToBackend();
  // Lightweight keep-alive ping every 25 seconds to preserve the WebSocket.
  chrome.alarms.create('keepAlive', { periodInMinutes: 0.4 });
}

function closeSocket() {
  if (ws) {
    try { ws.close(); } catch (_) {}
    ws = null;
  }
}

async function connectToBackend() {
  if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) {
    return;
  }

  const config = await getConfig();
  const validation = validateConfig(config);
  if (!validation.ok) {
    console.warn(`[Gemini Sync] Not connecting: ${validation.error}`);
    return;
  }

  const wsUrl = buildWsUrl(config, validation.parsed);
  console.log(`[Gemini Sync] Connecting to configured backend: ${validation.parsed.origin}`);
  hasSyncedOnce = false;

  try {
    ws = new WebSocket(wsUrl);
  } catch (e) {
    console.warn('[Gemini Sync] WebSocket construction failed:', e);
    scheduleReconnect();
    return;
  }

  ws.onopen = () => {
    console.log(`[Gemini Sync] Connected to ${validation.parsed.origin} (Zero-Tab Mode)`);
    chrome.alarms.clear('reconnect');
    performSync();
  };

  ws.onmessage = (event) => {
    try {
      const msg = JSON.parse(event.data);
      if (msg.type === 'trigger_sync') {
        console.log('[Gemini Sync] Backend requested fresh cookies. Syncing from memory (0 tabs)...');
        performSync();
      }
    } catch (e) {
      console.error('[Gemini Sync] Invalid backend message:', e);
    }
  };

  ws.onclose = () => {
    ws = null;
    console.log('[Gemini Sync] Connection closed. Reconnecting from stored configuration...');
    scheduleReconnect();
  };

  ws.onerror = () => {
    console.warn('[Gemini Sync] WebSocket connection failed');
    try { ws.close(); } catch (_) {}
  };
}

function scheduleReconnect() {
  chrome.alarms.create('reconnect', { delayInMinutes: 0.05 }); // ~3s
}

function keepAlive() {
  if (ws?.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify({ type: 'ping' }));
  } else {
    connectToBackend();
  }
}

const ALLOWED_COOKIE_NAMES = new Set([
  '__Secure-1PSID',
  '__Secure-3PSID',
  '__Secure-1PAPISID',
  '__Secure-3PAPISID',
  '__Secure-1PSIDTS',
  '__Secure-3PSIDTS',
  '__Secure-1PSIDCC',
  '__Secure-3PSIDCC',
  'SID',
  'HSID',
  'SSID',
  'APISID',
  'SAPISID',
  'SIDCC',
  'OSID',
  '__Secure-OSID',
  'COMPASS',
  'NID'
]);

function isTargetDomain(domain) {
  const normalized = String(domain || '').toLowerCase();
  return normalized === '.google.com' ||
    normalized === 'gemini.google.com' ||
    normalized.endsWith('.google.com') ||
    normalized === '.googleusercontent.com' ||
    normalized.endsWith('.googleusercontent.com');
}

function normalizeCookie(cookie) {
  let expirationDate = cookie.expirationDate;
  if (!expirationDate || cookie.session) {
    expirationDate = Math.floor(Date.now() / 1000) + 31536000; // 1 year
  }

  let sameSite = cookie.sameSite || 'unspecified';
  if (sameSite === 'no_restriction') sameSite = 'none';

  return {
    domain: cookie.domain,
    expirationDate,
    hostOnly: cookie.hostOnly,
    httpOnly: cookie.httpOnly,
    name: cookie.name,
    path: cookie.path || '/',
    sameSite,
    secure: cookie.secure,
    session: cookie.session,
    storeId: cookie.storeId || '0',
    value: cookie.value
  };
}

// Extracts only allowlisted cookies from Google domains, directly from memory.
function getFormattedCookies(callback) {
  const collect = (domain, done) => {
    chrome.cookies.getAll({ domain }, (cookies) => {
      if (chrome.runtime.lastError) {
        done([]);
        return;
      }
      done(Array.isArray(cookies) ? cookies : []);
    });
  };

  collect('google.com', (googleCookies) => {
    collect('googleusercontent.com', (contentCookies) => {
      const seen = new Set();
      const formatted = [];

      for (const cookie of [...googleCookies, ...contentCookies]) {
        if (!cookie || !cookie.name || !cookie.value) continue;
        if (!isTargetDomain(cookie.domain)) continue;
        if (!ALLOWED_COOKIE_NAMES.has(cookie.name) && !cookie.domain.includes('googleusercontent.com')) continue;
        if (cookie.value.length > 8192) continue;

        const key = `${cookie.domain}|${cookie.path || '/'}|${cookie.name}`;
        if (seen.has(key)) continue;
        seen.add(key);
        formatted.push(normalizeCookie(cookie));
      }

      callback(formatted);
    });
  });
}

// Performs instantaneous memory extraction and WebSocket/HTTP transfer.
async function performSync() {
  const config = await getConfig();
  const validation = validateConfig(config);
  if (!validation.ok) {
    console.warn(`[Gemini Sync] Sync aborted: ${validation.error}`);
    return;
  }

  getFormattedCookies((formatted) => {
    if (formatted.length === 0) {
      console.warn('[Gemini Sync] No allowlisted Google cookies found');
      return;
    }

    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({
        type: 'cookies_payload',
        cookies: formatted
      }));
      console.log(`[Gemini Sync] Synchronized ${formatted.length} cookies via WebSocket`);
    } else {
      // Dual resilience: authenticated HTTP fallback.
      fetch(`${config.serverUrl}/api/sync-cookies`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'X-API-Token': config.wsToken
        },
        body: JSON.stringify({ cookies: formatted })
      }).then((res) => {
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        console.log(`[Gemini Sync] Synchronized ${formatted.length} cookies via HTTP fallback`);
      }).catch((e) => {
        console.warn('[Gemini Sync] HTTP fallback failed:', e);
      });

      connectToBackend();
    }

    hasSyncedOnce = true;
    lastSyncTime = Date.now();
    chrome.storage.local.set({ lastSyncTime });
    chrome.runtime.sendMessage({ type: 'SYNC_UPDATE', success: true, count: formatted.length }).catch(() => {});
  });
}

// Real-time passive cookie listener: Google rotates session cookies frequently.
chrome.cookies.onChanged.addListener((changeInfo) => {
  const cookie = changeInfo.cookie;
  if (!cookie || changeInfo.removed) return;
  if (!isTargetDomain(cookie.domain) || !ALLOWED_COOKIE_NAMES.has(cookie.name)) return;

  // Debounce a batch of rotations into one sync.
  if (syncDebounceTimeout) clearTimeout(syncDebounceTimeout);
  syncDebounceTimeout = setTimeout(() => {
    performSync();
  }, 1500);
});

// Popup message API.
chrome.runtime.onMessage.addListener((msg, sender, sendResponse) => {
  if (msg.type === 'GET_STATUS') {
    getConfig().then((config) => {
      const connected = Boolean(ws && ws.readyState === WebSocket.OPEN);
      const connecting = Boolean(ws && ws.readyState === WebSocket.CONNECTING);

      // Opening the popup wakes the MV3 worker. If a token is configured but
      // the socket was lost while the worker slept, reconnect immediately.
      if (!connected && !connecting && config.wsToken) {
        connectToBackend();
      }

      sendResponse({
        connected,
        connecting,
        lastSyncTime,
        hasSyncedOnce,
        serverUrl: config.serverUrl,
        wsPort: config.wsPort,
        tokenConfigured: Boolean(config.wsToken)
      });
    });
    return true; // async sendResponse
  }

  if (msg.type === 'SET_SERVER_CONFIG') {
    const serverUrl = String(msg.serverUrl || '').trim().replace(/\/+$/, '');
    const wsPort = String(msg.wsPort || DEFAULT_WS_PORT).trim();
    const wsToken = String(msg.wsToken || '').trim();

    const validation = validateConfig({ serverUrl, wsPort, wsToken });
    if (!validation.ok) {
      sendResponse({ ok: false, error: validation.error });
      return false;
    }

    chrome.storage.local.set({ serverUrl, wsPort, wsToken }).then(() => {
      closeSocket();
      connectToBackend();
      sendResponse({ ok: true });
    });
    return true; // async sendResponse
  }

  if (msg.type === 'FORCE_SYNC') {
    performSync();
    sendResponse({ ok: true });
  }

  return true;
});
