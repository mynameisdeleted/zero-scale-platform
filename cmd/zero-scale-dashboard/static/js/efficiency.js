function renderEfficiency(eff) {
  if (!eff) return;

  const effPct = Math.round(eff.saved_memory_percent || 0);
  const savedGB = (eff.saved_memory_mb / 1024).toFixed(2);
  const tradGB = (eff.traditional_baseline_mb / 1024).toFixed(2);
  const actGB = (eff.current_actual_mb / 1024).toFixed(2);

  const badgeEff = document.getElementById('badge-efficiency-saved');
  if (badgeEff) badgeEff.innerText = `${effPct}% Memory Saved`;

  // Card 1: Zero-Scale Serverless
  const serverlessSavedMB = eff.serverless_savings_mb;
  const serverlessSavedGB = (serverlessSavedMB / 1024).toFixed(2);
  const elZeroSaved = document.getElementById('eff-zero-saved');
  if (elZeroSaved) elZeroSaved.innerText = `${serverlessSavedGB} GB`;
  const elZeroDesc = document.getElementById('eff-zero-desc');
  if (elZeroDesc) elZeroDesc.innerText = `${eff.idle_zero_apps} of ${eff.total_apps_count} services idle at 0 MB RAM`;
  const zeroSavedPct = eff.total_apps_count > 0 ? Math.round((eff.idle_zero_apps / eff.total_apps_count) * 100) : 100;
  const elZeroPct = document.getElementById('eff-zero-pct');
  if (elZeroPct) elZeroPct.innerText = `${zeroSavedPct}% Idle (0 MB)`;
  const elZeroFill = document.getElementById('fill-zero-saved');
  if (elZeroFill) elZeroFill.style.width = `${zeroSavedPct}%`;

  // Total Zero-Scale Cluster Footprint
  const zeroFootMB = eff.total_zero_scale_footprint_mb || 0;
  const zeroClusterPct = eff.zero_scale_cluster_percent || 0;
  const badgeFootprint = document.getElementById('badge-zero-footprint');
  if (badgeFootprint) {
    badgeFootprint.innerText = `⚡ Total Zero-Scale: ${zeroFootMB.toFixed(1)} MB (${zeroClusterPct.toFixed(2)}% of Cluster)`;
  }
  const elZeroFootVal = document.getElementById('eff-zero-footprint-val');
  if (elZeroFootVal) {
    elZeroFootVal.innerText = `${zeroFootMB.toFixed(1)} MB (${zeroClusterPct.toFixed(2)}% of cluster)`;
  }

  // Card 2: CloudNativePG DB Pooling (Explicitly Clarifying Savings vs Consumption)
  const dbSavedMB = eff.database_savings_mb || 620;
  const dbActualMB = eff.database_actual_mb || 280;
  const dbBaselineMB = eff.database_baseline_mb || 900;
  const elDbSaved = document.getElementById('eff-db-saved');
  if (elDbSaved) elDbSaved.innerText = `~${dbSavedMB} MB Saved`;
  const elDbDesc = document.getElementById('eff-db-desc');
  if (elDbDesc) elDbDesc.innerText = `Saves ~${dbSavedMB} MB net by pooling databases into 1 shared HA cluster vs 3+ separate 24/7 DB pods`;
  const elDbActualVal = document.getElementById('eff-db-actual-val');
  if (elDbActualVal) elDbActualVal.innerText = `~${dbActualMB} MB`;
  const dbSavedPct = dbBaselineMB > 0 ? Math.round((dbSavedMB / dbBaselineMB) * 100) : 69;
  const elDbPct = document.getElementById('eff-db-pct');
  if (elDbPct) elDbPct.innerText = `~${dbSavedPct}% Saved`;
  const elDbFill = document.getElementById('fill-db-saved');
  if (elDbFill) elDbFill.style.width = `${Math.min(dbSavedPct, 100)}%`;

  // Card 4: Total Net Cluster Memory Saved
  const elTotalSaved = document.getElementById('eff-total-saved');
  if (elTotalSaved) elTotalSaved.innerText = `${savedGB} GB`;
  const elTotalBreakdown = document.getElementById('eff-total-breakdown');
  if (elTotalBreakdown) elTotalBreakdown.innerText = `Live: ${actGB} GB vs 24/7 Baseline: ${tradGB} GB`;
  const elTotalPct = document.getElementById('eff-total-pct');
  if (elTotalPct) elTotalPct.innerText = `${effPct}% Reduction`;
  const elTotalFill = document.getElementById('fill-total-saved');
  if (elTotalFill) elTotalFill.style.width = `${Math.min(effPct, 100)}%`;
}
