// go24 task lists: the gear in the header picks columns. They show or hide at once in every list on the page,
// and the choice is saved on the server for this user (it follows them to other browsers).
(() => {
  // the list scrolls sideways, which would clip a normal dropdown: the picker, the menu of a row (☰) and the deadline
  // editor float above the page instead. The last two come from the server when first opened (data-lazy — the
  // address of the menu: only what the user may do with the task).
  const POPS = 'details.colpick, details.trowmenu, details.tdl';
  const place = d => {
    const r = d.querySelector('summary').getBoundingClientRect();
    const pop = d.querySelector('.pop');
    pop.style.position = 'fixed';
    pop.style.top = (r.bottom + 4) + 'px';
    pop.style.left = Math.max(4, Math.min(r.left, document.documentElement.clientWidth - pop.offsetWidth - 12)) + 'px';
  };
  document.querySelectorAll(POPS).forEach(d => {
    d.addEventListener('toggle', async () => {
      if (!d.open) return;
      document.querySelectorAll(POPS).forEach(o => { if (o !== d && o.open) o.open = false; });
      place(d);
      if (!d.dataset.lazy || d.dataset.loaded) return;
      const pop = d.querySelector('.pop');
      pop.innerHTML = '<span class="muted small">…</span>';
      try {
        const res = await fetch(d.dataset.lazy, { headers: { Accept: 'text/html' } });
        if (!res.ok) throw new Error((await res.json().catch(() => ({}))).error || res.statusText);
        pop.innerHTML = await res.text();
        d.dataset.loaded = '1';
      } catch (err) {
        pop.textContent = err.message;
      }
      place(d);
      pop.querySelector('input:not([type=hidden])')?.focus();
    });
  });
  document.addEventListener('click', e => {
    document.querySelectorAll(POPS).forEach(d => { if (d.open && !d.contains(e.target)) d.open = false; });
  });
  // a box belongs to its picker's form, inside it or by form="…" (a picker inside another form, as the CRM lists)
  document.addEventListener('change', async e => {
    const box = e.target.closest?.('input[name=cols]');
    const form = box?.form;
    if (!form?.matches('[data-colpick]')) return;
    document.querySelectorAll(`[data-col="${box.value}"]`).forEach(c => { c.hidden = !box.checked; });
    document.querySelectorAll(`input[name=cols][value="${box.value}"]`).forEach(b => { if (b.form?.matches('[data-colpick]')) b.checked = box.checked; });
    try {
      await fetch(form.action, { method: 'POST', headers: { Accept: 'application/json' }, body: new URLSearchParams(new FormData(form)) });
    } catch { /* the page already shows the choice; it is saved next time */ }
  });

  // subtasks under their parent task: the arrow shows or hides them; a row shows when every task above it is open.
  // What is open is kept in this browser.
  const TREE = 'go24.tasklist.open';
  let open = new Set();
  try { open = new Set(JSON.parse(localStorage.getItem(TREE) || '[]')); } catch { /* nothing kept */ }
  const tree = () => {
    document.querySelectorAll('tr[data-path]').forEach(tr => {
      tr.hidden = !tr.dataset.path.split(' ').every(k => open.has(k));
    });
    document.querySelectorAll('tr[data-tree] [data-tree-toggle]').forEach(b => {
      b.setAttribute('aria-expanded', open.has(b.closest('tr').dataset.tree));
    });
  };
  document.addEventListener('click', e => {
    const b = e.target.closest?.('[data-tree-toggle]');
    if (!b) return;
    const k = b.closest('tr').dataset.tree;
    if (open.has(k)) open.delete(k); else open.add(k);
    try { localStorage.setItem(TREE, JSON.stringify([...open].slice(-500))); } catch { /* not kept */ }
    tree();
  });
  tree();

  // «На странице» under a task list: the choice reloads the list
  document.querySelectorAll('form.pagesize select').forEach(sel => {
    sel.addEventListener('change', () => sel.form.submit());
  });

  // group actions: «all» ticks every task of its list; the panel shows when something is ticked, with the field
  // its action needs
  const bulk = document.querySelector('form[data-bulk]');
  if (bulk) {
    const boxes = () => [...document.querySelectorAll('input[name=task][form=bulk]')];
    const op = bulk.querySelector('[data-bulk-op]');
    const refresh = () => {
      const n = boxes().filter(b => b.checked).length;
      bulk.hidden = n === 0;
      bulk.querySelector('[data-bulk-count]').textContent = n;
      document.querySelectorAll('[data-bulk-picked]').forEach(c => { c.textContent = n; });
      bulk.querySelectorAll('[data-bulk-for]').forEach(f => {
        const on = f.dataset.bulkFor.split(' ').includes(op.value);
        f.hidden = !on; f.disabled = !on;
      });
    };
    document.querySelectorAll('input[data-bulk-all]').forEach(all => {
      all.addEventListener('change', () => {
        all.closest('table').querySelectorAll('input[name=task][form=bulk]').forEach(b => { b.checked = all.checked; });
        refresh();
      });
    });
    document.addEventListener('change', e => { if (e.target.matches('input[name=task][form=bulk]')) refresh(); });
    op.addEventListener('change', refresh);
    refresh();
  }
})();
