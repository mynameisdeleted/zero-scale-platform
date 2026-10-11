function formatBytes(bytes) {
  if (!bytes || bytes === 0) return '0 B';
  const k = 1024;
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
}

function formatCores(cores) {
  if (!cores || cores <= 0) return '0 cores';
  if (cores < 0.01) {
    const milli = Math.round(cores * 1000);
    return `${milli}m cores`;
  }
  return `${cores.toFixed(2)} cores`;
}

function renderSummaryMetrics(sum, stack) {
  if (!sum) return;
  const elNodes = document.getElementById('val-nodes');
  if (elNodes) elNodes.innerText = `${sum.ready_nodes} / ${sum.total_nodes}`;
  const elCountNodes = document.getElementById('count-nodes');
  if (elCountNodes) elCountNodes.innerText = sum.total_nodes;
  const elSubNodes = document.getElementById('sub-nodes');
  if (elSubNodes) elSubNodes.innerText = sum.ready_nodes === sum.total_nodes ? 'All Nodes Ready' : 'Degraded';

  const cpuPct = Math.round(sum.cpu_usage_percent || 0);
  const elValCpu = document.getElementById('val-cpu');
  if (elValCpu) elValCpu.innerText = `${cpuPct}%`;
  const elSubCpu = document.getElementById('sub-cpu-detail');
  if (elSubCpu) elSubCpu.innerText = `${sum.used_cores.toFixed(2)} / ${sum.total_cores} Cores`;
  const elSubCpuPct = document.getElementById('sub-cpu-pct');
  if (elSubCpuPct) elSubCpuPct.innerText = `${cpuPct}%`;
  const elFillCpu = document.getElementById('fill-cpu');
  if (elFillCpu) elFillCpu.style.width = `${Math.min(cpuPct, 100)}%`;


  const elValZero = document.getElementById('val-zero');
  if (elValZero) elValZero.innerText = `${sum.total_zero_apps}`;
  const elCountZero = document.getElementById('count-zero');
  if (elCountZero) elCountZero.innerText = sum.total_zero_apps;
  const elSubZeroAct = document.getElementById('sub-zero-active');
  if (elSubZeroAct) elSubZeroAct.innerText = `${sum.active_zero_apps} Active Now`;

  // Zero-Scale Stack Total Card
  if (stack) {
    const elStackRam = document.getElementById('val-stack-ram');
    if (elStackRam) elStackRam.innerText = formatBytes(stack.total_ram_bytes);
    const elStackCpu = document.getElementById('sub-stack-cpu');
    if (elStackCpu) elStackCpu.innerText = `${formatCores(stack.total_cpu_cores)} CPU`;
    const elStackPct = document.getElementById('sub-stack-pct');
    if (elStackPct) elStackPct.innerText = `${stack.cluster_ram_percent.toFixed(1)}% Cluster RAM`;

    // Segmented bar
    const totalRAM = stack.total_ram_bytes || 1;
    const zeroPct = (stack.zero_scale_ram_bytes / totalRAM) * 100;
    const cnpgPct = (stack.postgres_ram_bytes / totalRAM) * 100;
    const lhPct = (stack.longhorn_ram_bytes / totalRAM) * 100;

    const elFillZero = document.getElementById('fill-stack-zero');
    if (elFillZero) elFillZero.style.width = `${zeroPct.toFixed(1)}%`;
    const elFillCnpg = document.getElementById('fill-stack-cnpg');
    if (elFillCnpg) elFillCnpg.style.width = `${cnpgPct.toFixed(1)}%`;
    const elFillLh = document.getElementById('fill-stack-lh');
    if (elFillLh) elFillLh.style.width = `${lhPct.toFixed(1)}%`;

    // Breakout rows (RAM + CPU)
    const elBreakZero = document.getElementById('stack-breakout-zero');
    if (elBreakZero) {
      elBreakZero.innerHTML = `<span style="color:var(--green); font-weight:700;">${formatBytes(stack.zero_scale_ram_bytes)}</span> <span style="color:var(--text-muted); font-size:12px; font-weight:normal; margin-left:6px;">(${formatCores(stack.zero_scale_cpu_cores)})</span>`;
    }
    const elBreakCnpg = document.getElementById('stack-breakout-cnpg');
    if (elBreakCnpg) {
      elBreakCnpg.innerHTML = `<span style="color:var(--blue); font-weight:700;">${formatBytes(stack.postgres_ram_bytes)}</span> <span style="color:var(--text-muted); font-size:12px; font-weight:normal; margin-left:6px;">(${formatCores(stack.postgres_cpu_cores)})</span>`;
    }
    const elBreakLh = document.getElementById('stack-breakout-lh');
    if (elBreakLh) {
      elBreakLh.innerHTML = `<span style="color:var(--purple); font-weight:700;">${formatBytes(stack.longhorn_ram_bytes)}</span> <span style="color:var(--text-muted); font-size:12px; font-weight:normal; margin-left:6px;">(${formatCores(stack.longhorn_cpu_cores)})</span>`;
    }
  }
}
