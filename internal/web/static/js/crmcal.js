// «Календарь» of CRM deals and leads (and of tasks: their deadline): a card dragged onto a day gets that day in the
// date field shown. The card moves at once; on error it goes back; with data-reload the page is shown anew.
(() => {
  const cal = document.querySelector('[data-crmcal]');
  if (!cal) return;
  let dragged = null, from = null;
  const flash = (text) => {
    let el = document.querySelector('.content .flash');
    if (!el) {
      el = document.createElement('div');
      el.className = 'flash';
      document.querySelector('.content').prepend(el);
    }
    el.textContent = text;
  };

  cal.addEventListener('dragstart', e => {
    const item = e.target.closest('.calitem[draggable=true]');
    if (!item) return;
    dragged = item; from = item.closest('.calday');
    e.dataTransfer.effectAllowed = 'move';
  });
  cal.addEventListener('dragend', () => {
    cal.querySelectorAll('.calday.over').forEach(x => x.classList.remove('over'));
    dragged = null;
  });
  cal.querySelectorAll('.calday[data-day]').forEach(day => {
    day.addEventListener('dragover', e => {
      if (!dragged || day === from) return;
      day.classList.add('over');
      e.preventDefault();
    });
    day.addEventListener('dragleave', () => day.classList.remove('over'));
    day.addEventListener('drop', async e => {
      e.preventDefault();
      day.classList.remove('over');
      if (!dragged || day === from) return;
      const item = dragged, source = from;
      day.append(item);
      let ok = false, error = '';
      try {
        const res = await fetch(cal.dataset.crmcal + encodeURIComponent(item.dataset.key) + '/date', {
          method: 'POST', headers: { Accept: 'application/json' },
          body: new URLSearchParams({ date: cal.dataset.date, day: day.dataset.day }),
        });
        ok = res.ok;
        if (!ok) error = (await res.json().catch(() => ({}))).error;
      } catch { /* the network */ }
      if (!ok) { source.append(item); flash(error || cal.dataset.failed); } else if ('reload' in cal.dataset) location.reload();
    });
  });
})();
