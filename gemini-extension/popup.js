document.addEventListener('DOMContentLoaded', () => {
  const statusBadge = document.getElementById('status-badge');
  const statusText = document.getElementById('status-text');
  const lastSync = document.getElementById('last-sync');
  const syncBtn = document.getElementById('sync-btn');
  const saveBtn = document.getElementById('save-btn');
  const serverUrlInput = document.getElementById('server-url');
  const wsPortInput = document.getElementById('ws-port');
  const wsTokenInput = document.getElementById('ws-token');
  const configHint = document.getElementById('config-hint');

  const DEFAULT_SERVER_URL = 'http://127.0.0.1:8100';
  const DEFAULT_WS_PORT = '9226';

  function setHint(message, warning = false) {
    configHint.textContent = message;
    configHint.className = warning ? 'hint warning' : 'hint';
  }

  function loadStoredConfig() {
    chrome.storage.local.get(['serverUrl', 'wsPort', 'wsToken'], (data) => {
      serverUrlInput.value = data.serverUrl || DEFAULT_SERVER_URL;
      wsPortInput.value = data.wsPort || DEFAULT_WS_PORT;
      wsTokenInput.value = data.wsToken || '';
    });
  }

  function updateUI() {
    chrome.runtime.sendMessage({ type: 'GET_STATUS' }, (response) => {
      if (chrome.runtime.lastError || !response) return;

      if (response.connected) {
        statusBadge.className = 'badge connected';
        statusText.innerText = 'Connected';
        statusText.style.color = '#10b981';
        setHint('Connected. Cookies sync automatically while gemini.google.com is logged in.');
      } else if (response.connecting) {
        statusBadge.className = 'badge connected';
        statusText.innerText = 'Connecting…';
        statusText.style.color = '#f59e0b';
        setHint('Connecting to the local API…');
      } else {
        statusBadge.className = 'badge disconnected';
        statusText.innerText = 'Disconnected';
        statusText.style.color = '#ef4444';
        if (!response.tokenConfigured) {
          setHint('Paste the API_TOKEN value from free-gemini-api/.env, then click Save.', true);
        } else {
          setHint('Token configured, but the local API is unreachable. Is the container running?', true);
        }
      }

      if (response.lastSyncTime) {
        lastSync.innerText = new Date(response.lastSyncTime).toLocaleTimeString();
      } else {
        lastSync.innerText = 'Never';
      }
    });
  }

  loadStoredConfig();
  updateUI();
  // The background worker may need a moment to (re)connect after being woken.
  setTimeout(updateUI, 1500);

  chrome.runtime.onMessage.addListener((msg) => {
    if (msg.type === 'SYNC_UPDATE') {
      updateUI();
    }
  });

  saveBtn.addEventListener('click', () => {
    saveBtn.disabled = true;
    saveBtn.innerText = 'Saving...';

    chrome.runtime.sendMessage({
      type: 'SET_SERVER_CONFIG',
      serverUrl: serverUrlInput.value,
      wsPort: wsPortInput.value,
      wsToken: wsTokenInput.value
    }, (response) => {
      saveBtn.disabled = false;
      saveBtn.innerText = 'Save Configuration';

      if (chrome.runtime.lastError || !response || !response.ok) {
        const reason = response && response.error ? response.error : 'Invalid configuration';
        setHint(reason, true);
        return;
      }

      setHint('Configuration saved. Connecting…');
      setTimeout(updateUI, 1500);
    });
  });

  syncBtn.addEventListener('click', () => {
    syncBtn.disabled = true;
    syncBtn.innerText = 'Syncing...';
    chrome.runtime.sendMessage({ type: 'FORCE_SYNC' }, () => {
      setTimeout(() => {
        syncBtn.disabled = false;
        syncBtn.innerText = 'Force Sync Cookies';
        updateUI();
      }, 800);
    });
  });
});
