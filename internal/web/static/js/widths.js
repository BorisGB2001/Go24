// go24 widths: the borders of a list's column headers and of a page's panes are dragged grids and
// the mail (GO24DEV-135); a double click on a border gives the usual widths back. They are kept for the user and the
// list — in this browser at once (localStorage) and on the server (/prefs/widths) for the other devices (GO24DEV-477).
//   <table data-cols="tasks">  a table joins under this name; list tables (.tlist, .disk-list, table.list) join by
//                              themselves under the page's address; data-cols="off" keeps one out, th[data-nosize]
//                              a column. A column is named by its data-col, else by its place.
//   <div class="board" data-cols-board="deadlines">  a board of columns (Сроки, kanban): each column's right border is
//                              dragged; a column is named by its data-stage, else by its place.
//   <aside data-pane="task-chat" data-pane-side="left">  a pane of a grid or flex page whose (left) border is dragged;
//                              narrow screens keep the page's own layout.
//   <aside data-pane="im-side" data-pane-var="--im-side">  the width goes into that CSS variable of the parent instead,
//                              for a page whose own CSS lays the panes out (a grid whose columns come and go: the
//                              messenger); several panes may share one name and variable.
(() => {
  const STORE = 'go24.widths';
  let saved = {};
  try { saved = JSON.parse(localStorage.getItem(STORE) || '{}') || {}; } catch { saved = {}; }
  const keepLocal = () => { try { localStorage.setItem(STORE, JSON.stringify(saved)); } catch { /* private mode */ } };
  const keep = (name, w) => {
    if (w && Object.keys(w).length) saved[name] = w; else delete saved[name];
    keepLocal();
    fetch('/prefs/widths', { method: 'POST', headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: new URLSearchParams({ name, widths: saved[name] ? JSON.stringify(saved[name]) : '' }) }).catch(() => {});
  };
  // /crm/deal/42 and /crm/deal/43 are the same list
  const place = () => location.pathname.split('/').map(s => (/\d/.test(s) ? '*' : s)).join('/');
  // a border is dragged: fn(dx) on every move, done() at the end
  const drag = (grip, e, fn, done) => {
    e.preventDefault(); e.stopPropagation();
    const x0 = e.clientX;
    grip.classList.add('on');
    grip.setPointerCapture?.(e.pointerId);
    const move = ev => fn(ev.clientX - x0);
    const up = () => {
      grip.classList.remove('on');
      grip.removeEventListener('pointermove', move);
      grip.removeEventListener('pointerup', up);
      grip.removeEventListener('pointercancel', up);
      done();
    };
    grip.addEventListener('pointermove', move);
    grip.addEventListener('pointerup', up);
    grip.addEventListener('pointercancel', up);
  };
  const appliers = {}; // name → apply(widths) of this page's lists and panes

  // ---- table columns
  let auto = 0;
  document.querySelectorAll('table[data-cols], table.tlist, table.disk-list, table.list').forEach(t => {
    // the header row, or the first row of a table without one (Дни рождения): it sets the widths of a fixed table
    const row = t.tHead?.rows[0] || t.rows[0];
    if (t.dataset.cols === 'off' || !row) return;
    const name = t.dataset.cols || 'list:' + place() + ':' + auto++;
    const heads = () => [...row.cells];
    const col = (th, i) => th.dataset.col || String(i);
    const fit = () => {
      let sum = 0;
      heads().forEach(th => { if (!th.hidden) sum += parseFloat(th.style.width) || th.getBoundingClientRect().width; });
      t.style.tableLayout = 'fixed';
      t.style.width = Math.round(sum) + 'px';
      t.classList.add('cols-set');
      // wider than the page: the list scrolls sideways, the page does not
      const box = t.parentElement;
      if (box && !box.classList.contains('cols-scroll') && !['auto', 'scroll'].includes(getComputedStyle(box).overflowX)) {
        const wrap = document.createElement('div');
        wrap.className = 'cols-scroll';
        box.insertBefore(wrap, t);
        wrap.append(t);
      }
    };
    const apply = w => {
      if (!w) {
        t.style.tableLayout = t.style.width = '';
        heads().forEach(th => { th.style.width = ''; });
        t.classList.remove('cols-set');
        return;
      }
      // measured before anything is fixed: the columns not dragged keep what they have now
      const hs = heads(), now = hs.map(th => th.getBoundingClientRect().width);
      hs.forEach((th, i) => {
        const v = w[col(th, i)] ?? Math.round(now[i]);
        if (v >= 20) th.style.width = v + 'px';
      });
      fit();
    };
    const save = () => {
      const w = {};
      heads().forEach((th, i) => { const v = parseInt(th.style.width, 10); if (v >= 20) w[col(th, i)] = v; });
      keep(name, w);
    };
    t.classList.add('cols');
    heads().forEach(th => {
      if (th.classList.contains('gearcol') || th.classList.contains('bulkcol') || 'nosize' in th.dataset) return;
      const g = document.createElement('span');
      g.className = 'colgrip';
      g.setAttribute('aria-hidden', 'true');
      if (getComputedStyle(th).position === 'static') th.style.position = 'relative';
      th.append(g);
      g.addEventListener('pointerdown', e => {
        if (!t.classList.contains('cols-set')) apply({});
        const w0 = parseFloat(th.style.width) || th.getBoundingClientRect().width;
        drag(g, e, dx => { th.style.width = Math.max(40, Math.round(w0 + dx)) + 'px'; fit(); }, save);
      });
      g.addEventListener('click', e => { e.preventDefault(); e.stopPropagation(); }); // not the header's sort link
      g.addEventListener('dblclick', e => { e.preventDefault(); e.stopPropagation(); apply(null); keep(name, null); });
    });
    // a column shown by the gear («Колонки») gets a width of its own instead of being squeezed
    if (window.MutationObserver) {
      new MutationObserver(() => {
        if (!t.classList.contains('cols-set')) return;
        heads().forEach(th => { if (!th.hidden && !th.style.width) th.style.width = '140px'; });
        fit();
      }).observe(row, { attributes: true, attributeFilter: ['hidden'], subtree: true });
    }
    appliers[name] = apply;
    if (saved[name]) apply(saved[name]);
  });

  // ---- board columns
  document.querySelectorAll('[data-cols-board]').forEach(b => {
    const name = 'board:' + b.dataset.colsBoard;
    const cols = () => [...b.children].filter(c => !c.hidden && c.tagName !== 'SCRIPT');
    const key = (c, i) => c.dataset.stage || String(i);
    const apply = w => {
      if (!w) { b.style.gridTemplateColumns = ''; b.classList.remove('cols-set'); return; }
      const now = cols().map(c => c.getBoundingClientRect().width);
      b.style.gridTemplateColumns = cols().map((c, i) => Math.round(w[key(c, i)] ?? now[i]) + 'px').join(' ');
      b.classList.add('cols-set');
    };
    const widths = () => {
      const w = {};
      cols().forEach((c, i) => { const v = Math.round(c.getBoundingClientRect().width); if (v >= 20) w[key(c, i)] = v; });
      return w;
    };
    cols().forEach(c => {
      if ('nosize' in c.dataset) return;
      if (getComputedStyle(c).position === 'static') c.style.position = 'relative';
      const g = document.createElement('span');
      g.className = 'colgrip boardgrip';
      g.setAttribute('aria-hidden', 'true');
      c.append(g);
      g.addEventListener('pointerdown', e => {
        const w = widths(), k = key(c, cols().indexOf(c)), w0 = w[k] || c.getBoundingClientRect().width;
        drag(g, e, dx => { w[k] = Math.max(160, Math.round(w0 + dx)); apply(w); }, () => keep(name, { ...w }));
      });
      g.addEventListener('dragstart', e => { e.preventDefault(); e.stopPropagation(); });
      g.addEventListener('dblclick', e => { e.stopPropagation(); apply(null); keep(name, null); });
    });
    appliers[name] = apply;
    if (saved[name]) apply(saved[name]);
  });

  // ---- panes
  document.querySelectorAll('[data-pane]').forEach(p => {
    const name = 'pane:' + p.dataset.pane, left = p.dataset.paneSide === 'left', box = p.parentElement;
    const rule = document.createElement('style');
    document.head.append(rule);
    box.dataset.paneBox = p.dataset.pane;
    const apply = w => {
      const px = w?.w;
      if (p.dataset.paneVar) {
        if (px) box.style.setProperty(p.dataset.paneVar, px + 'px'); else box.style.removeProperty(p.dataset.paneVar);
        return;
      }
      if (!px) { rule.textContent = ''; return; }
      const sel = `[data-pane-box="${CSS.escape(p.dataset.pane)}"]`;
      let css;
      if (getComputedStyle(box).display.includes('grid')) {
        const kids = [...box.children].filter(c => !c.hidden && c.tagName !== 'SCRIPT');
        css = `${sel} { grid-template-columns: ${kids.map(c => (c === p ? px + 'px' : 'minmax(0, 1fr)')).join(' ')} !important; }`;
      } else {
        css = `${sel} > [data-pane="${CSS.escape(p.dataset.pane)}"] { flex: 0 0 ${px}px !important; width: ${px}px !important; }`;
      }
      rule.textContent = `@media (min-width: 1001px) { ${css} }`;
    };
    if (getComputedStyle(p).position === 'static') p.style.position = 'relative';
    const g = document.createElement('span');
    g.className = 'panegrip ' + (left ? 'left' : 'right');
    g.setAttribute('aria-hidden', 'true');
    p.append(g);
    let px = 0;
    g.addEventListener('pointerdown', e => {
      const w0 = p.getBoundingClientRect().width;
      let max = box.getBoundingClientRect().width - 240;
      if (p.dataset.paneVar) { // the page's other panes keep theirs: what is left of its free one (the chat) stays 300px
        const free = Math.max(0, ...[...box.children].filter(c => c !== p && !c.hidden && !c.dataset.pane).map(c => c.getBoundingClientRect().width));
        max = w0 + free - 300;
      }
      drag(g, e, dx => {
        px = Math.round(Math.min(max, Math.max(200, w0 + (left ? -dx : dx))));
        apply({ w: px });
      }, () => keep(name, { w: px }));
    });
    g.addEventListener('dblclick', () => { apply(null); keep(name, null); });
    appliers[name] = apply;
    if (saved[name]) apply(saved[name]);
  });

  // what the user set on another device
  if (!Object.keys(appliers).length || !window.fetch) return;
  fetch('/prefs/widths', { headers: { Accept: 'application/json' } })
    .then(r => (r.ok ? r.json() : null))
    .then(server => {
      if (!server || typeof server !== 'object') return;
      for (const [name, apply] of Object.entries(appliers)) {
        if (JSON.stringify(server[name] ?? null) !== JSON.stringify(saved[name] ?? null)) apply(server[name] || null);
      }
      saved = server;
      keepLocal();
    })
    .catch(() => {});
})();
