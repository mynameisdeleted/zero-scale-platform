function renderZeroScale(zeroApps, isAuthed, token) {
  const zeroCont = document.getElementById('zero-container');
  if (!zeroCont || !zeroApps) return;

  const ph = document.getElementById('placeholder-zero');
  if (ph) ph.remove();

  zeroApps.forEach(app => {
    const cardId = 'zero-card-' + app.host.replace(/[^a-zA-Z0-9_-]/g, '-');
    let card = document.getElementById(cardId);
    const stateCls = app.state === 'active' ? 'active' : 'idle';
    const stateLabel = app.state === 'active' ? '🟢 Active' : '💤 Scaled to Zero';
    const logsUrl = isAuthed && token ? `${app.logs_url}?token=${encodeURIComponent(token)}` : app.logs_url;

    const resHtml = app.state === 'active'
      ? `<span style="color: var(--green); font-weight:600;">⚡ ${formatBytes(app.ram_bytes)} RAM</span> &bull; <span style="color: var(--blue); font-weight:600;">${formatCores(app.cpu_cores)} CPU</span>`
      : `<span style="color: var(--text-muted);">⚡ 0 B RAM &bull; 0 cores (Scaled to 0)</span>`;

    if (!card) {
      card = document.createElement('div');
      card.className = 'zero-card';
      card.id = cardId;
      card.innerHTML = `
        <div>
          <div class="zero-host">${app.host}</div>
          <div class="zero-status-row">
            <span class="status-badge ${stateCls} zero-badge">${stateLabel}</span>
          </div>
          <div class="zero-resource-row" style="text-align:center; font-size:12px; margin-bottom:8px;">
            ${resHtml}
          </div>
          <div class="zero-desc">${app.description || 'Zero-scale dynamic service'}</div>
        </div>
        <div class="zero-actions">
          <a href="${app.url}" target="_blank" onclick="handleOpenApp()" class="btn btn-primary" style="flex:1; justify-content:center;">🌐 Open App</a>
          <a href="${logsUrl}" target="_blank" class="btn btn-logs" style="flex:1; justify-content:center;">🔐 Logs</a>
        </div>
      `;
      zeroCont.appendChild(card);
    } else {
      // In-place update
      const badge = card.querySelector('.zero-badge');
      if (badge && badge.innerText !== stateLabel) {
        badge.className = 'status-badge ' + stateCls + ' zero-badge';
        badge.innerText = stateLabel;
      }
      const resEl = card.querySelector('.zero-resource-row');
      if (resEl && resEl.innerHTML !== resHtml) {
        resEl.innerHTML = resHtml;
      }
      const logsLink = card.querySelector('.btn-logs');
      if (logsLink && logsLink.href !== logsUrl) {
        logsLink.href = logsUrl;
      }
    }
  });
}

function handleOpenApp() {
  setTimeout(() => { if (typeof loadStatus === 'function') loadStatus(); }, 400);
  setTimeout(() => { if (typeof loadStatus === 'function') loadStatus(); }, 1500);
}
