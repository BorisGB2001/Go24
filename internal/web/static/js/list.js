// go24 task lists: the gear in the header picks columns. They show or hide at once in every list on the page,
// and the choice is saved on the server for this user (it follows them to other browsers).
(() => {
  // the list scrolls sideways, which would clip a normal dropdown: the picker floats above the page instead
  document.querySelectorAll('details.colpick').forEach(d => {
    d.addEventListener('toggle', () => {
      if (!d.open) return;
      const r = d.querySelector('summary').getBoundingClientRect();
      const pop = d.querySelector('.pop');
      pop.style.position = 'fixed';
      pop.style.top = (r.bottom + 4) + 'px';
      pop.style.left = r.left + 'px';
    });
  });
  document.addEventListener('click', e => {
    document.querySelectorAll('details.colpick[open]').forEach(d => { if (!d.contains(e.target)) d.open = false; });
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
