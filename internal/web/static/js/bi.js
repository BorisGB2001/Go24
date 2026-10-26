// «BI-аналитика»: «Печать» prints the dashboard (bi.css leaves it alone on the page); while a dashboard is edited its
// widgets are dragged to a new order, which is saved at once.
document.querySelectorAll('[data-bi-print]').forEach(b => b.addEventListener('click', () => {
  b.closest('details')?.removeAttribute('open');
  print();
}));
(() => {
  const grid = document.querySelector('.bi-grid.editing[data-bi-grid]');
  if (!grid) return;
  let dragged = null;
  grid.addEventListener('dragstart', e => {
    dragged = e.target.closest('.bi-w');
    if (!dragged) return;
    dragged.classList.add('dragging');
    e.dataTransfer.effectAllowed = 'move';
    e.dataTransfer.setData('text/plain', dragged.dataset.id);
  });
  grid.addEventListener('dragover', e => {
    const over = e.target.closest('.bi-w');
    if (!dragged || !over || over === dragged) return;
    e.preventDefault();
    const r = over.getBoundingClientRect();
    const after = e.clientX > r.left + r.width / 2 || e.clientY > r.bottom - r.height / 4;
    over.parentNode.insertBefore(dragged, after ? over.nextSibling : over);
  });
  grid.addEventListener('dragend', () => {
    if (!dragged) return;
    dragged.classList.remove('dragging');
    dragged = null;
    const body = new URLSearchParams();
    grid.querySelectorAll('.bi-w').forEach(w => body.append('ids', w.dataset.id));
    fetch(grid.dataset.biGrid, { method: 'POST', headers: { Accept: 'application/json', 'X-Go24-Form': '1' }, body });
  });
})();
// The filters of a dashboard: a value picked in a filter replaces the field's values; a click on a column, a point, a
// slice or a cell of a table filters the whole dashboard by its value (a date — by the one value, the others — adding
// to the field's), a click on a value picked takes it off.
(() => {
  const go = set => {
    const u = new URL(location.href);
    set(u.searchParams);
    u.hash = '';
    location.assign(u);
  };
  document.querySelectorAll('select[data-bi-filter]').forEach(s => s.addEventListener('change', () => {
    if (s.value === '*') return;
    go(p => {
      p.delete(s.dataset.biFilter);
      if (s.value !== '' || s.selectedIndex > 0) p.append(s.dataset.biFilter, s.value);
    });
  }));
  const grid = document.querySelector('.bi-grid');
  if (!grid || grid.classList.contains('editing')) return;
  grid.addEventListener('click', e => {
    const el = e.target.closest('[data-bi-f]');
    if (!el) return;
    const f = el.dataset.biF, v = el.dataset.biV;
    go(p => {
      const had = p.getAll(f);
      p.delete(f);
      if (had.includes(v)) had.filter(x => x !== v).forEach(x => p.append(f, x));
      else if (/^(day|week|month|quarter|year):\d{4}-\d\d-\d\d$/.test(v)) p.append(f, v);
      else [...had, v].forEach(x => p.append(f, x));
    });
  });
})();
