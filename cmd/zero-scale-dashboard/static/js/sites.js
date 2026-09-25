function renderOtherSites(otherSites) {
  const sitesCont = document.getElementById('sites-container');
  if (!sitesCont || !otherSites) return;

  const ph = document.getElementById('placeholder-sites');
  if (ph) ph.remove();

  // Only render once if not yet built; avoid rebuilding static Ingress links every 10s
  if (sitesCont.children.length > 0) return;

  otherSites.forEach(group => {
    const section = document.createElement('div');
    section.innerHTML = `
      <div class="section-header">
        <div class="section-title">
          <span>${group.category}</span>
          <span class="badge-count">${group.sites.length}</span>
        </div>
      </div>
      <div class="sites-grid" id="group-${group.category.replace(/\s+/g, '-')}"></div>
    `;
    sitesCont.appendChild(section);

    const grid = section.querySelector('.sites-grid');
    group.sites.forEach(site => {
      const card = document.createElement('a');
      card.className = 'site-card';
      card.href = site.url;
      card.target = '_blank';
      card.innerHTML = `
        <div class="site-icon">${site.icon || '🌐'}</div>
        <div class="site-info">
          <div class="site-name">${site.name}</div>
          <div class="site-host">${site.host}</div>
          <div class="site-desc">${site.description}</div>
          <span class="site-ns">ns: ${site.namespace}</span>
        </div>
      `;
      grid.appendChild(card);
    });
  });
}
