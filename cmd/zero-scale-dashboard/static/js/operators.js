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
    const ramText = `Consumes ~${eff ? eff.database_actual_mb : 280} MB (Saves ~${eff ? eff.database_savings_mb : 620} MB)`;

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
            <span class="operator-stat-val op-pg-replicas" style="color: var(--green);">${pg.operator_ready} / ${pg.operator_replicas} Replicas Ready</span>
          </div>
          <div class="operator-stat-box">
            <span class="operator-stat-label">HA Database Cluster</span>
            <span class="operator-stat-val op-pg-instances">${pg.ready_instances} / ${pg.instances} Instances Ready</span>
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
            <span class="operator-stat-label">RAM Footprint (Savings)</span>
            <span class="operator-stat-val op-pg-ram" style="color: var(--blue);">${ramText}</span>
          </div>
        </div>
      `;
      opsCont.appendChild(pgCard);
    } else {
      const badge = pgCard.querySelector('.op-pg-status');
      if (badge) { badge.className = 'status-badge ' + pgReadyCls + ' op-pg-status'; badge.innerText = pg.status || 'Active'; }
      const replEl = pgCard.querySelector('.op-pg-replicas');
      if (replEl) replEl.innerText = `${pg.operator_ready} / ${pg.operator_replicas} Replicas Ready`;
      const instEl = pgCard.querySelector('.op-pg-instances');
      if (instEl) instEl.innerText = `${pg.ready_instances} / ${pg.instances} Instances Ready`;
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
            <span class="operator-stat-val op-lh-manager" style="color: var(--green);">${lh.manager_ready} / ${lh.manager_desired} Nodes Ready</span>
          </div>
          <div class="operator-stat-box">
            <span class="operator-stat-label">Management UI</span>
            <span class="operator-stat-val op-lh-ui" style="color: var(--green);">${lh.ui_ready} / ${lh.ui_desired} Replicas Ready</span>
          </div>
          <div class="operator-stat-box">
            <span class="operator-stat-label">Distributed Volumes</span>
            <span class="operator-stat-val op-lh-vols" style="color: var(--blue);">${lh.total_volumes} Active PVs</span>
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
      if (mgrEl) mgrEl.innerText = `${lh.manager_ready} / ${lh.manager_desired} Nodes Ready`;
      const uiEl = lhCard.querySelector('.op-lh-ui');
      if (uiEl) uiEl.innerText = `${lh.ui_ready} / ${lh.ui_desired} Replicas Ready`;
      const volsEl = lhCard.querySelector('.op-lh-vols');
      if (volsEl) volsEl.innerText = `${lh.total_volumes} Active PVs`;
      const scEl = lhCard.querySelector('.op-lh-sc');
      if (scEl) scEl.innerText = lh.storage_class || 'longhorn-2-replicas';
    }
  }
}
