// go24 Gantt chart: drag a bar to move the task's plan dates, drag its edge to change the start or
// the finish; drag from the dot at a bar's side to the side of another bar to link them (the four kinds of links:
// finish → start, start → start, finish → finish, start → finish); click a link to remove it. The page reloads
// with the server's chart after each change.
(() => {
  const root = document.querySelector('[data-gantt]');
  if (!root) return;
  const svg = root.querySelector('svg');
  const chart = root.querySelector('.g-chart');
  const dayW = +root.dataset.dayw;
  const DAY = 86400000;
  const scrollKey = 'gantt-scroll:' + location.pathname;

  try { // back to where the chart was scrolled before the reload
    const left = sessionStorage.getItem(scrollKey);
    if (left !== null) { chart.scrollLeft = +left; sessionStorage.removeItem(scrollKey); }
  } catch { /* no storage */ }

  const flash = (text) => {
    let el = document.querySelector('.content .flash');
    if (!el) { el = document.createElement('div'); el.className = 'flash'; document.querySelector('.content').prepend(el); }
    el.textContent = text;
  };
  const post = async (url, params) => {
    try {
      const res = await fetch(url, { method: 'POST', headers: { Accept: 'application/json' }, body: new URLSearchParams(params) });
      let data = {};
      try { data = await res.json(); } catch { data = {}; }
      return { ok: res.ok, data };
    } catch { return { ok: false, data: {} }; }
  };
  const done = (res) => {
    if (!res.ok) { flash(res.data.error || root.dataset.failed); return false; }
    try { sessionStorage.setItem(scrollKey, chart.scrollLeft); } catch { /* no storage */ }
    location.reload();
    return true;
  };
  const pt = (e) => { // the pointer in the chart's coordinates
    const p = svg.createSVGPoint();
    p.x = e.clientX; p.y = e.clientY;
    return p.matrixTransform(svg.getScreenCTM().inverse());
  };
  const iso = (s, days) => new Date(Date.parse(s + 'T00:00:00Z') + days * DAY).toISOString().slice(0, 10);
  const shown = (s) => new Date(s + 'T00:00:00Z').toLocaleDateString(document.documentElement.lang || undefined, { timeZone: 'UTC', day: 'numeric', month: 'short' });

  const tip = document.createElement('div');
  tip.className = 'g-tip';
  tip.hidden = true;
  document.body.append(tip);
  const showTip = (e, text) => {
    tip.textContent = text; tip.hidden = false;
    tip.style.left = (e.clientX + 12) + 'px'; tip.style.top = (e.clientY + 14) + 'px';
  };

  let drag = null; // {mode: move|start|end|link, item, …}
  let swallowClick = false;

  svg.addEventListener('pointerdown', (e) => {
    if (e.button !== 0) return;
    const item = e.target.closest('.g-item');
    if (!item) return;
    const p = pt(e);
    if (e.target.classList.contains('g-dot')) { // a link from this side
      const line = document.createElementNS('http://www.w3.org/2000/svg', 'line');
      line.setAttribute('class', 'g-link draft');
      const x = +e.target.getAttribute('cx'), y = +e.target.getAttribute('cy');
      line.setAttribute('x1', x); line.setAttribute('y1', y); line.setAttribute('x2', x); line.setAttribute('y2', y);
      svg.append(line);
      drag = { mode: 'link', item, side: e.target.dataset.side, line };
    } else if (item.classList.contains('edit')) {
      const bar = item.querySelector('.g-bar');
      const x = +item.dataset.x, w = +item.dataset.w;
      let mode = 'move';
      if (bar.tagName === 'rect' && w >= 3 * 6) {
        if (p.x - x <= 6) mode = 'start';
        else if (x + w - p.x <= 6) mode = 'end';
      }
      drag = { mode, item, bar, x, w, x0: p.x, days: 0 };
    } else return;
    svg.setPointerCapture(e.pointerId);
    e.preventDefault();
  });

  svg.addEventListener('pointermove', (e) => {
    if (!drag) {
      const item = e.target.closest('.g-item.edit');
      svg.style.cursor = '';
      if (item && e.target.tagName === 'rect') {
        const p = pt(e), x = +item.dataset.x, w = +item.dataset.w;
        if (w >= 18 && (p.x - x <= 6 || x + w - p.x <= 6)) svg.style.cursor = 'ew-resize';
      }
      return;
    }
    const p = pt(e);
    if (drag.mode === 'link') {
      drag.line.setAttribute('x2', p.x); drag.line.setAttribute('y2', p.y);
      return;
    }
    const { item, bar, x, w } = drag;
    let days = Math.round((p.x - drag.x0) / dayW);
    const len = Math.round(w / dayW); // the bar's days
    if (drag.mode === 'start') days = Math.min(days, len - 1);
    if (drag.mode === 'end') days = Math.max(days, 1 - len);
    drag.days = days;
    const dx = days * dayW;
    if (drag.mode === 'move') bar.setAttribute('transform', `translate(${dx} 0)`);
    else if (drag.mode === 'start') { bar.setAttribute('x', x + dx); bar.setAttribute('width', w - dx); }
    else bar.setAttribute('width', w + dx);
    item.classList.add('dragging');
    const s = item.dataset.start, f = item.dataset.end;
    const ns = drag.mode === 'end' ? s : iso(s, days), nf = drag.mode === 'start' ? f : iso(f, days);
    showTip(e, `${shown(ns)} – ${shown(nf)}`);
  });

  const finish = async (e, cancel) => {
    if (!drag) return;
    const d = drag;
    drag = null;
    tip.hidden = true;
    if (d.mode === 'link') {
      d.line.remove();
      if (cancel) return;
      const under = document.elementFromPoint(e.clientX, e.clientY);
      const to = under?.closest('.g-item');
      if (!to || to === d.item) return;
      swallowClick = true;
      let side = under.dataset?.side;
      if (!side) { // dropped on the bar: its nearer side
        const p = pt(e), x = +to.dataset.x, w = +to.dataset.w;
        side = p.x < x + w / 2 ? 'start' : 'finish';
      }
      done(await post(`/t/${to.dataset.key}/links`, { pred: d.item.dataset.key, type: `${d.side}_${side}` }));
      return;
    }
    d.item.classList.remove('dragging');
    const revert = () => { d.bar.removeAttribute('transform'); d.bar.setAttribute('x', d.x); d.bar.setAttribute('width', d.w); };
    if (cancel || d.days === 0) { revert(); return; }
    swallowClick = true;
    const s = d.item.dataset.start, f = d.item.dataset.end;
    const res = await post(`/t/${d.item.dataset.key}/gantt-dates`, {
      start_plan: d.mode === 'end' ? s : iso(s, d.days),
      end_plan: d.mode === 'start' ? f : iso(f, d.days),
    });
    if (!done(res)) revert();
  };
  svg.addEventListener('pointerup', (e) => finish(e, false));
  svg.addEventListener('pointercancel', (e) => finish(e, true));
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape') finish(e, true); });

  svg.addEventListener('click', async (e) => {
    if (swallowClick) { swallowClick = false; e.preventDefault(); return; }
    const link = e.target.closest('.g-link[data-link]');
    if (!link) return;
    e.preventDefault();
    if (!confirm(root.dataset.unlink)) return;
    done(await post(`/t/${link.dataset.to}/links/${link.dataset.link}/delete`, {}));
  }, true);
})();
