function renderOperators(ops, eff) {
  const opsCont = document.getElementById('operators-container');
  if (!opsCont || !ops) return;

  const ph = document.getElementById('placeholder-operators');
  if (ph) ph.remove();

  const pg = ops.postgres;
  const lh = ops.longhorn;

  if (pg) {
    let pgCard = document.getElementById('op-card-postgres');
    const pgReadyCls = pg.operator_ready > 0 ? 'ready' : 'notready';
    const ramText = pg.total_ram_bytes > 0
      ? `Live: ${formatBytes(pg.total_ram_bytes)} RAM • ${formatCores(pg.total_cpu_cores)} CPU (Saves ~${eff ? eff.database_savings_mb : 620} MB)`
      : `Consumes ~${eff ? eff.database_actual_mb : 280} MB (Saves ~${eff ? eff.database_savings_mb : 620} MB)`;

    const opCtrlText = pg.operator_ram_bytes > 0
      ? `${pg.operator_ready} / ${pg.operator_replicas} Ready • ${formatBytes(pg.operator_ram_bytes)} RAM • ${formatCores(pg.operator_cpu_cores)}`
      : `${pg.operator_ready} / ${pg.operator_replicas} Replicas Ready`;

    const haDbText = pg.database_ram_bytes > 0
      ? `${pg.ready_instances} / ${pg.instances} Ready • ${formatBytes(pg.database_ram_bytes)} RAM • ${formatCores(pg.database_cpu_cores)}`
      : `${pg.ready_instances} / ${pg.instances} Instances Ready`;

    if (!pgCard) {
      pgCard = document.createElement('div');
      pgCard.className = 'operator-card';
      pgCard.id = 'op-card-postgres';
      pgCard.innerHTML = `
        <div class="operator-top">
          <div>
            <div class="operator-title"><span>🐘</span> CloudNativePG Operator</div>
            <div class="operator-sub">Enterprise PostgreSQL HA Controller &bull; ns: ${pg.namespace || 'cnpg-system'}</div>
          </div>
          <span class="status-badge ${pgReadyCls} op-pg-status">${pg.status || 'Active'}</span>
        </div>
        <div class="operator-meta">
          <div class="operator-stat-box">
            <span class="operator-stat-label">Operator Controller</span>
            <span class="operator-stat-val op-pg-replicas" style="color: var(--green);">${opCtrlText}</span>
          </div>
          <div class="operator-stat-box">
            <span class="operator-stat-label">HA Database Cluster</span>
            <span class="operator-stat-val op-pg-instances">${haDbText}</span>
          </div>
          <div class="operator-stat-box">
            <span class="operator-stat-label">Primary Instance</span>
            <span class="operator-stat-val op-pg-primary" style="color: var(--blue);">🟢 ${pg.primary || 'ha-postgres-1'}</span>
          </div>
          <div class="operator-stat-box">
            <span class="operator-stat-label">Cluster Target</span>
            <span class="operator-stat-val op-pg-target">${pg.cluster_name || 'ha-postgres'}</span>
          </div>
          <div class="operator-stat-box">
            <span class="operator-stat-label">RAM &amp; CPU Measured (Savings)</span>
            <span class="operator-stat-val op-pg-ram" style="color: var(--blue);">${ramText}</span>
          </div>
        </div>
      `;
      opsCont.appendChild(pgCard);
    } else {
      const badge = pgCard.querySelector('.op-pg-status');
      if (badge) { badge.className = 'status-badge ' + pgReadyCls + ' op-pg-status'; badge.innerText = pg.status || 'Active'; }
      const replEl = pgCard.querySelector('.op-pg-replicas');
      if (replEl) replEl.innerText = opCtrlText;
      const instEl = pgCard.querySelector('.op-pg-instances');
      if (instEl) instEl.innerText = haDbText;
      const primEl = pgCard.querySelector('.op-pg-primary');
      if (primEl) primEl.innerText = `🟢 ${pg.primary || 'ha-postgres-1'}`;
      const targEl = pgCard.querySelector('.op-pg-target');
      if (targEl) targEl.innerText = pg.cluster_name || 'ha-postgres';
      const ramEl = pgCard.querySelector('.op-pg-ram');
      if (ramEl) ramEl.innerText = ramText;
    }
  }

  if (lh) {
    let lhCard = document.getElementById('op-card-longhorn');
    const lhReadyCls = lh.manager_ready > 0 && lh.manager_ready === lh.manager_desired ? 'ready' : 'notready';

    const lhMgrText = lh.manager_ram_bytes > 0
      ? `${lh.manager_ready} / ${lh.manager_desired} Nodes • ${formatBytes(lh.manager_ram_bytes)} RAM • ${formatCores(lh.manager_cpu_cores)}`
      : `${lh.manager_ready} / ${lh.manager_desired} Nodes Ready`;

    const lhMeshText = lh.total_ram_bytes > 0
      ? `Total: ${formatBytes(lh.total_ram_bytes)} RAM • ${formatCores(lh.total_cpu_cores)} CPU`
      : `${lh.total_volumes} Active PVs`;

    if (!lhCard) {
      lhCard = document.createElement('div');
      lhCard.className = 'operator-card';
      lhCard.id = 'op-card-longhorn';
      lhCard.innerHTML = `
        <div class="operator-top">
          <div>
            <div class="operator-title"><span>🐮</span> Longhorn Storage Operator</div>
            <div class="operator-sub">Cloud-Native Distributed Block Storage &bull; ns: ${lh.namespace || 'longhorn-system'}</div>
          </div>
          <span class="status-badge ${lhReadyCls} op-lh-status">${lh.status || 'Healthy'}</span>
        </div>
        <div class="operator-meta">
          <div class="operator-stat-box">
            <span class="operator-stat-label">Storage Manager (DS)</span>
            <span class="operator-stat-val op-lh-manager" style="color: var(--green);">${lhMgrText}</span>
          </div>
          <div class="operator-stat-box">
            <span class="operator-stat-label">Management UI</span>
            <span class="operator-stat-val op-lh-ui" style="color: var(--green);">${lh.ui_ready} / ${lh.ui_desired} Replicas Ready</span>
          </div>
          <div class="operator-stat-box">
            <span class="operator-stat-label">Longhorn Mesh Measured</span>
            <span class="operator-stat-val op-lh-vols" style="color: var(--blue);">${lhMeshText}</span>
          </div>
          <div class="operator-stat-box">
            <span class="operator-stat-label">Storage Class</span>
            <span class="operator-stat-val op-lh-sc">${lh.storage_class || 'longhorn-2-replicas'}</span>
          </div>
        </div>
      `;
      opsCont.appendChild(lhCard);
    } else {
      const badge = lhCard.querySelector('.op-lh-status');
      if (badge) { badge.className = 'status-badge ' + lhReadyCls + ' op-lh-status'; badge.innerText = lh.status || 'Healthy'; }
      const mgrEl = lhCard.querySelector('.op-lh-manager');
      if (mgrEl) mgrEl.innerText = lhMgrText;
      const uiEl = lhCard.querySelector('.op-lh-ui');
      if (uiEl) uiEl.innerText = `${lh.ui_ready} / ${lh.ui_desired} Replicas Ready`;
      const volsEl = lhCard.querySelector('.op-lh-vols');
      if (volsEl) volsEl.innerText = lhMeshText;
      const scEl = lhCard.querySelector('.op-lh-sc');
      if (scEl) scEl.innerText = lh.storage_class || 'longhorn-2-replicas';
    }
  }
}
