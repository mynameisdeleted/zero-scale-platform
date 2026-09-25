let isUpdating = false;

async function loadStatus() {
  if (isUpdating) return;
  isUpdating = true;

  const statusEl = document.getElementById('update-status');
  const pulseDot = document.querySelector('.pulse-dot');
  if (pulseDot) pulseDot.classList.add('syncing');

  try {
    const headers = {};
    const token = getAuthToken();
    if (token) {
      headers['Authorization'] = 'Bearer ' + token;
    }

    // Load https://kstat.fairfaxmedia.net/api/status in background asynchronously
    let res;
    try {
      res = await fetch('https://kstat.fairfaxmedia.net/api/status', { headers });
    } catch (e) {
      res = await fetch('/api/status', { headers });
    }

    if (!res.ok) throw new Error('API returned ' + res.status);
    const data = await res.json();
    renderDashboard(data);
    if (statusEl) {
      const timeStr = new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
      statusEl.innerText = 'Live • ' + timeStr;
    }
  } catch (err) {
    console.error('Status sync error:', err);
    if (statusEl) statusEl.innerText = 'Sync Error (Retrying...)';
  } finally {
    isUpdating = false;
    if (pulseDot) pulseDot.classList.remove('syncing');
  }
}

function renderDashboard(data) {
  // Update Auth Button State
  const authBtn = document.getElementById('btn-auth');
  const authIcon = document.getElementById('auth-icon');
  const authLabel = document.getElementById('auth-label');
  const isAuthed = !!data.authenticated;
  const token = getAuthToken();

  if (authBtn && authIcon && authLabel) {
    if (isAuthed) {
      authIcon.innerText = '🟢';
      authLabel.innerText = 'RBAC Active';
      authBtn.style.borderColor = 'var(--green)';
      authBtn.style.color = 'var(--green)';
    } else {
      authIcon.innerText = '🔐';
      authLabel.innerText = 'RBAC Access';
      authBtn.style.borderColor = 'var(--border)';
      authBtn.style.color = 'var(--text)';
    }
  }

  // Summary Cards
  if (typeof renderSummaryMetrics === 'function') {
    renderSummaryMetrics(data.summary);
  }

  // Architecture Efficiency Metrics
  if (typeof renderEfficiency === 'function') {
    renderEfficiency(data.efficiency);
  }

  // Smooth in-place updates for child sections
  if (typeof renderOperators === 'function') {
    renderOperators(data.operators, data.efficiency);
  }
  if (typeof renderNodes === 'function') {
    renderNodes(data.nodes, isAuthed);
  }
  if (typeof renderZeroScale === 'function') {
    renderZeroScale(data.zero_scale_apps, isAuthed, token);
  }
  if (typeof renderOtherSites === 'function') {
    renderOtherSites(data.other_sites);
  }
}

// Automatically refresh status in background whenever user switches back to this tab
document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'visible') {
    loadStatus();
  }
});

// Initial load & 10s auto-refresh in background
loadStatus();
setInterval(loadStatus, 10000);
