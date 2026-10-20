function isZeroScaleSupporting(h) {
  if (h.category === 'supporting') return true;
  const name = (h.name || '').toLowerCase();
  const ns = (h.namespace || '').toLowerCase();
  const disp = (h.display || '').toLowerCase();
  if (name.includes('share-manager') || disp.includes('share-manager')) return true;
  if (name.includes('ha-postgres') || name.includes('cnpg') || ns === 'cnpg-system' || disp.includes('postgresql')) return true;
  if (name.includes('longhorn') || name.includes('instance-manager') || ns === 'longhorn-system' || disp.includes('longhorn')) return true;
  if (name.includes('nginx-ingress') || name.includes('ingress-nginx') || ns === 'ingress' || disp.includes('nginx ingress')) return true;
  return false;
}

function renderSupportingPills(hogs) {
  if (!hogs || hogs.length === 0) return '<span style="font-size: 10px; color: var(--text-muted); grid-column: 1 / -1;">None on this node</span>';
  return hogs.slice(0, 6).map(h => `
    <span class="supporting-pill" title="${h.display} (Zero-Scale Supporting Infrastructure)">
      <span class="supporting-name">${h.display}</span>
      <span class="supporting-ram">${formatBytes(h.ram_bytes)}</span>
    </span>
  `).join('');
}

function renderLegacyPills(hogs) {
  if (!hogs || hogs.length === 0) return '<span style="font-size: 10px; color: var(--green); grid-column: 1 / -1;">None (Clean)</span>';
  return hogs.slice(0, 6).map(h => `
    <span class="non-zero-pill legacy-pill hog-pill" title="${h.display} (Legacy Monolith)">
      <span class="non-zero-name">${h.display}</span>
      <span class="non-zero-ram hog-ram">${formatBytes(h.ram_bytes)}</span>
    </span>
  `).join('');
}

function renderHogPills(hogs) {
  return renderLegacyPills(hogs);
}

function formatZeroServiceName(host) {
  if (!host) return '';
  return host;
}

function renderZeroPills(zeroApps) {
  if (!zeroApps || zeroApps.length === 0) {
    return '<span style="font-size: 11px; color: var(--text-muted);">No 0-scale services</span>';
  }
  return zeroApps.map(app => {
    const isActive = app.state === 'active';
    const ramText = isActive ? formatBytes(app.ram_bytes) : '0 B';
    const stateIcon = isActive ? '🟢' : '⚡';
    const name = formatZeroServiceName(app.host);
    const title = `${app.host} • ${isActive ? 'Active (' + ramText + ')' : 'Scaled to Zero (0 B)'}`;
    const url = app.url || `https://${app.host}/`;
    return `
      <a href="${url}" target="_blank" class="zero-pill ${isActive ? 'active' : 'idle'}" title="${title}">
        <span class="zero-pill-dot">${stateIcon}</span>
        <span class="zero-pill-name">${name}</span>
        <span class="zero-pill-ram">${ramText}</span>
      </a>
    `;
  }).join('');
}

function renderNodes(nodes, isAuthed, allZeroApps) {
  const nodesCont = document.getElementById('nodes-container');
  if (!nodesCont || !nodes) return;

  const ph = document.getElementById('placeholder-nodes');
  if (ph) ph.remove();

  nodes.forEach(n => {
    const cardId = 'node-card-' + n.name.replace(/[^a-zA-Z0-9_-]/g, '-');
    let card = document.getElementById(cardId);
    const cpuPct = Math.round(n.cpu_usage_percent || 0);
    const ramPct = Math.round(n.ram_usage_percent || 0);
    const readyCls = n.status === 'Ready' ? 'ready' : 'notready';

    const kernelHtml = isAuthed
      ? `<span class="tag-unlocked">🟢 ${n.kernel_version}</span>`
      : `<span class="tag-locked" onclick="toggleAuthModal()" title="Click to unlock with RBAC token">${n.kernel_version}</span>`;
    const kubeletHtml = isAuthed
      ? `<span class="tag-unlocked">🟢 ${n.kubelet_version}</span>`
      : `<span class="tag-locked" onclick="toggleAuthModal()" title="Click to unlock with RBAC token">${n.kubelet_version}</span>`;

    const zeroApps = (n.zero_scale_services && n.zero_scale_services.length > 0)
      ? n.zero_scale_services
      : (allZeroApps || []);
    const zeroHtml = renderZeroPills(zeroApps);

    // Classify supporting vs legacy pods
    let supportingPods = n.zero_scale_supporting || [];
    let legacyPods = n.legacy_monoliths || [];

    if (supportingPods.length === 0 && legacyPods.length === 0 && n.top_memory_consumers) {
      supportingPods = n.top_memory_consumers.filter(isZeroScaleSupporting);
      legacyPods = n.top_memory_consumers.filter(h => !isZeroScaleSupporting(h));
    }

    const suppRAM = (n.supporting_ram_bytes != null && n.supporting_ram_bytes > 0)
      ? n.supporting_ram_bytes
      : supportingPods.reduce((acc, h) => acc + (h.ram_bytes || 0), 0);

    const legRAM = (n.legacy_ram_bytes != null && n.legacy_ram_bytes > 0)
      ? n.legacy_ram_bytes
      : (n.other_workload_ram_bytes != null && n.other_workload_ram_bytes > 0
          ? n.other_workload_ram_bytes
          : legacyPods.reduce((acc, h) => acc + (h.ram_bytes || 0), 0));

    const supportingHtml = renderSupportingPills(supportingPods);
    const legacyHtml = renderLegacyPills(legacyPods);
    const runtimeName = (n.container_engine || 'containerd').split('://')[0].split(' ')[0];

    const needsRebuild = !card || !card.querySelector('.supporting-total');

    if (needsRebuild) {
      if (!card) {
        card = document.createElement('div');
        card.className = 'node-card';
        card.id = cardId;
        nodesCont.appendChild(card);
      }
      card.innerHTML = `
        <div class="node-top">
          <div>
            <div class="node-name">${n.name}</div>
            <div class="node-role"><span class="node-role-text">${n.roles.join(', ')}</span> &bull; <span class="node-uptime-val">${n.uptime}</span> uptime</div>
          </div>
          <span class="status-badge ${readyCls} node-status-badge">${n.status}</span>
        </div>
        <div class="node-meta">
          <div><strong>OS:</strong> <span class="node-os-val">${n.os_image}</span></div>
          <div><strong>Runtime:</strong> <span class="node-runtime-val">${runtimeName}</span></div>
          <div><strong>Kernel:</strong> <span class="node-kernel-val">${kernelHtml}</span></div>
          <div><strong>Kubelet:</strong> <span class="node-kubelet-val">${kubeletHtml}</span></div>
        </div>
        <div class="node-metric">
          <div class="metric-label-row">
            <span>CPU (${n.cpu_cores} Cores)</span>
            <span class="node-cpu-val">${n.cpu_used_cores.toFixed(2)} cores (${cpuPct}%)</span>
          </div>
          <div class="progress-bg">
            <div class="progress-fill node-cpu-fill" style="width:${Math.min(cpuPct, 100)}%; --fill-color: var(--blue);"></div>
          </div>
        </div>
        <div class="node-metric">
          <div class="metric-label-row">
            <span>RAM (${formatBytes(n.ram_total_bytes)})</span>
            <span class="node-ram-val">${formatBytes(n.ram_used_bytes)} (${ramPct}%)</span>
          </div>
          <div class="progress-bg">
            <div class="progress-fill node-ram-fill" style="width:${Math.min(ramPct, 100)}%; --fill-color: var(--purple);"></div>
          </div>
        </div>
        <div class="node-ram-breakdown">
          <div class="ram-breakdown-row">
            <div class="ram-sub-title"><span>⚡ Zero-Scale Footprint:</span></div>
            <div class="ram-sub-val highlight-green">
              <span class="node-zero-ram-val">${formatBytes(n.zero_scale_ram_bytes || 0)}</span>
              <span class="ram-pct-badge node-zero-pct-badge">${(n.zero_scale_ram_percent || 0).toFixed(1)}% of node</span>
            </div>
          </div>
          <div class="ram-breakdown-row">
            <div class="ram-sub-title"><span>🛠️ Supporting Infra (DB/Storage/Ingress):</span></div>
            <div class="ram-sub-val highlight-blue">
              <span class="node-supporting-ram-val">${formatBytes(suppRAM)}</span>
            </div>
          </div>
          <div class="ram-breakdown-row">
            <div class="ram-sub-title"><span>🐘 Legacy 24/7 Monoliths:</span></div>
            <div class="ram-sub-val highlight-red">
              <span class="node-legacy-ram-val">${formatBytes(legRAM)}</span>
            </div>
          </div>
          <div class="node-services-section node-zero-section">
            <div class="node-services-header zero-header">
              <span>⚡ 0-Scale Services:</span>
              <span class="section-total-ram zero-total">${formatBytes(n.zero_scale_ram_bytes || 0)}</span>
            </div>
            <div class="node-services-list node-zero-list">${zeroHtml}</div>
          </div>
          <div class="node-services-section node-supporting-section">
            <div class="node-services-header supporting-header">
              <span>🛠️ Zero-Scale Supporting:</span>
              <span class="section-total-ram supporting-total">${formatBytes(suppRAM)}</span>
            </div>
            <div class="node-services-list node-supporting-list">${supportingHtml}</div>
          </div>
          <div class="node-services-section node-nonzero-section node-hogs-section">
            <div class="node-services-header nonzero-header node-hogs-header">
              <span>🐘 LEGACY:</span>
              <span class="section-total-ram legacy-total">${formatBytes(legRAM)}</span>
            </div>
            <div class="node-services-list node-hogs-list node-legacy-list">${legacyHtml}</div>
          </div>
        </div>
      `;
    } else {
      // In-place smooth update without DOM recreation
      const badge = card.querySelector('.node-status-badge');
      if (badge) { badge.className = 'status-badge ' + readyCls + ' node-status-badge'; badge.innerText = n.status; }
      const upEl = card.querySelector('.node-uptime-val');
      if (upEl && upEl.innerText !== n.uptime) upEl.innerText = n.uptime;

      const rtEl = card.querySelector('.node-runtime-val');
      if (rtEl && rtEl.innerText !== runtimeName) rtEl.innerText = runtimeName;

      const cpuVal = card.querySelector('.node-cpu-val');
      if (cpuVal) cpuVal.innerText = `${n.cpu_used_cores.toFixed(2)} cores (${cpuPct}%)`;
      const cpuFill = card.querySelector('.node-cpu-fill');
      if (cpuFill) cpuFill.style.width = Math.min(cpuPct, 100) + '%';

      const ramVal = card.querySelector('.node-ram-val');
      if (ramVal) ramVal.innerText = `${formatBytes(n.ram_used_bytes)} (${ramPct}%)`;
      const ramFill = card.querySelector('.node-ram-fill');
      if (ramFill) ramFill.style.width = Math.min(ramPct, 100) + '%';

      const zeroRamVal = card.querySelector('.node-zero-ram-val');
      if (zeroRamVal) zeroRamVal.innerText = formatBytes(n.zero_scale_ram_bytes || 0);
      const zeroPctBadge = card.querySelector('.node-zero-pct-badge');
      if (zeroPctBadge) zeroPctBadge.innerText = `${(n.zero_scale_ram_percent || 0).toFixed(1)}% of node`;

      const suppRamVal = card.querySelector('.node-supporting-ram-val');
      if (suppRamVal) suppRamVal.innerText = formatBytes(suppRAM);

      const legacyRamVal = card.querySelector('.node-legacy-ram-val');
      if (legacyRamVal) legacyRamVal.innerText = formatBytes(legRAM);

      const zeroTot = card.querySelector('.zero-total');
      if (zeroTot) zeroTot.innerText = formatBytes(n.zero_scale_ram_bytes || 0);

      const suppTot = card.querySelector('.supporting-total');
      if (suppTot) suppTot.innerText = formatBytes(suppRAM);

      const legTot = card.querySelector('.legacy-total');
      if (legTot) legTot.innerText = formatBytes(legRAM);

      const kernelVal = card.querySelector('.node-kernel-val');
      if (kernelVal && kernelVal.innerHTML !== kernelHtml) kernelVal.innerHTML = kernelHtml;
      const kubeletVal = card.querySelector('.node-kubelet-val');
      if (kubeletVal && kubeletVal.innerHTML !== kubeletHtml) kubeletVal.innerHTML = kubeletHtml;

      const zeroList = card.querySelector('.node-zero-list');
      if (zeroList && zeroList.innerHTML !== zeroHtml) zeroList.innerHTML = zeroHtml;

      const suppList = card.querySelector('.node-supporting-list');
      if (suppList && suppList.innerHTML !== supportingHtml) suppList.innerHTML = supportingHtml;

      const hogsList = card.querySelector('.node-hogs-list');
      if (hogsList && hogsList.innerHTML !== legacyHtml) hogsList.innerHTML = legacyHtml;
    }
  });
}
