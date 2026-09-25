function formatBytes(bytes) {
  if (!bytes || bytes === 0) return '0 B';
  const k = 1024;
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
}

function renderSummaryMetrics(sum) {
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

  const ramPct = Math.round(sum.ram_usage_percent || 0);
  const elValRam = document.getElementById('val-ram');
  if (elValRam) elValRam.innerText = `${ramPct}%`;
  const elSubRam = document.getElementById('sub-ram-detail');
  if (elSubRam) elSubRam.innerText = `${formatBytes(sum.used_ram_bytes)} / ${formatBytes(sum.total_ram_bytes)}`;
  const elSubRamPct = document.getElementById('sub-ram-pct');
  if (elSubRamPct) elSubRamPct.innerText = `${ramPct}%`;
  const elFillRam = document.getElementById('fill-ram');
  if (elFillRam) elFillRam.style.width = `${Math.min(ramPct, 100)}%`;

  const elValZero = document.getElementById('val-zero');
  if (elValZero) elValZero.innerText = `${sum.total_zero_apps}`;
  const elCountZero = document.getElementById('count-zero');
  if (elCountZero) elCountZero.innerText = sum.total_zero_apps;
  const elSubZeroAct = document.getElementById('sub-zero-active');
  if (elSubZeroAct) elSubZeroAct.innerText = `${sum.active_zero_apps} Active Now`;
}
