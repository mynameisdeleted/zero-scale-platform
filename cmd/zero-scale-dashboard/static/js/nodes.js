function renderHogPills(hogs) {
  if (!hogs || hogs.length === 0) return '';
  return hogs.map(h => `
    <span class="hog-pill" title="${h.namespace}/${h.name}">
      ${h.display} <span class="hog-ram">${formatBytes(h.ram_bytes)}</span>
    </span>
  `).join('');
}

function renderNodes(nodes, isAuthed) {
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

    const hogsHtml = renderHogPills(n.top_memory_consumers);

    if (!card) {
      card = document.createElement('div');
      card.className = 'node-card';
      card.id = cardId;
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
          <div><strong>Runtime:</strong> <span class="node-runtime-val">${n.container_engine.split('://')[0]}</span></div>
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
            <div class="ram-sub-title"><span>🐘 24/7 Workloads:</span></div>
            <div class="ram-sub-val"><span class="node-other-ram-val">${formatBytes(n.other_workload_ram_bytes || 0)}</span></div>
          </div>
          <div class="node-hogs-section">
            <div class="node-hogs-header">Top 24/7 Monoliths on Node:</div>
            <div class="node-hogs-list">${hogsHtml}</div>
          </div>
        </div>
      `;
      nodesCont.appendChild(card);
    } else {
      // In-place smooth update without DOM recreation
      const badge = card.querySelector('.node-status-badge');
      if (badge) { badge.className = 'status-badge ' + readyCls + ' node-status-badge'; badge.innerText = n.status; }
      const upEl = card.querySelector('.node-uptime-val');
      if (upEl && upEl.innerText !== n.uptime) upEl.innerText = n.uptime;

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

      const otherRamVal = card.querySelector('.node-other-ram-val');
      if (otherRamVal) otherRamVal.innerText = formatBytes(n.other_workload_ram_bytes || 0);

      const kernelVal = card.querySelector('.node-kernel-val');
      if (kernelVal && kernelVal.innerHTML !== kernelHtml) kernelVal.innerHTML = kernelHtml;
      const kubeletVal = card.querySelector('.node-kubelet-val');
      if (kubeletVal && kubeletVal.innerHTML !== kubeletHtml) kubeletVal.innerHTML = kubeletHtml;

      const hogsList = card.querySelector('.node-hogs-list');
      if (hogsList && hogsList.innerHTML !== hogsHtml) hogsList.innerHTML = hogsHtml;
    }
  });
}
