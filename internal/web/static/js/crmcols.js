// CRM lists, «Настроить колонки»: list.js shows or hides a ticked column; here its line in the gear is
// dragged to another place — the list's columns follow at once and the order is saved for this person.
(() => {
  document.querySelectorAll('form[data-crmcols]').forEach(form => {
    const list = document.querySelector('.crmcols-list');
    const table = list?.closest('table');
    if (!list) return;
    let dragged = null;
    // the gear's window floats below the gear (list.js) and scrolls inside when the page below is short
    const pick = list.closest('details');
    pick?.addEventListener('toggle', () => {
      if (!pick.open) return;
      const r = pick.querySelector('summary').getBoundingClientRect();
      list.closest('.pop').style.maxHeight = Math.max(200, innerHeight - r.bottom - 16) + 'px';
    });
    const save = async () => {
      try {
        await fetch(form.action, { method: 'POST', headers: { Accept: 'application/json' }, body: new URLSearchParams(new FormData(form)) });
      } catch { /* the page already shows the order; it is saved next time */ }
    };
    // the optional cells go after the fixed ones, in the order of the gear's lines
    const reorder = () => {
      const keys = [...list.querySelectorAll('input[name=cols]')].map(b => b.value);
      table?.querySelectorAll('tr').forEach(tr => {
        keys.forEach(k => { const c = tr.querySelector(`:scope > [data-col="${k}"]`); if (c) tr.appendChild(c); });
      });
    };
    list.addEventListener('dragstart', e => {
      dragged = e.target.closest('li');
      if (!dragged) return;
      dragged.classList.add('dragging');
      e.dataTransfer?.setData('text/plain', '');
    });
    list.addEventListener('dragover', e => {
      const li = e.target.closest('li');
      if (!dragged || !li || li === dragged) return;
      e.preventDefault();
      const r = li.getBoundingClientRect();
      li.parentNode.insertBefore(dragged, e.clientY > r.top + r.height / 2 ? li.nextSibling : li);
    });
    list.addEventListener('drop', e => e.preventDefault());
    list.addEventListener('dragend', () => {
      if (!dragged) return;
      dragged.classList.remove('dragging');
      dragged = null;
      reorder();
      save();
    });
  });
})();
