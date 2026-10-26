// go24 scrum planning: drag tasks between the backlog and sprints, estimate story points, pick an epic,
// add tasks to the backlog — all on the page; the server's answer (the row's HTML) replaces the row.
(() => {
  const root = document.querySelector('[data-scrum]');
  if (!root) return;
  const failed = root.dataset.failed;

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
  const fromHTML = (html) => { const t = document.createElement('template'); t.innerHTML = html.trim(); return t.content.firstElementChild; };
  const empties = () => root.querySelectorAll('[data-sprint]').forEach(sec => {
    const e = sec.querySelector(':scope > .empty');
    if (e) e.hidden = sec.querySelectorAll('.srow').length > 0;
  });

  // drag & drop between sections
  let dragged = null, from = null;
  root.addEventListener('dragstart', e => {
    const row = e.target.closest('.srow[draggable=true]');
    if (!row) return;
    dragged = row; from = row.closest('[data-sprint]');
    e.dataTransfer.effectAllowed = 'move';
    row.classList.add('dragging');
  });
  root.addEventListener('dragend', () => {
    dragged?.classList.remove('dragging');
    root.querySelectorAll('[data-sprint]').forEach(x => x.classList.remove('over'));
    dragged = null;
  });
  root.querySelectorAll('[data-sprint]').forEach(sec => {
    sec.addEventListener('dragover', e => { if (dragged && sec !== from) { sec.classList.add('over'); e.preventDefault(); } });
    sec.addEventListener('dragleave', () => sec.classList.remove('over'));
    sec.addEventListener('drop', async e => {
      e.preventDefault();
      sec.classList.remove('over');
      if (!dragged || sec === from) return;
      const row = dragged, source = from;
      sec.querySelector('[data-rows]').prepend(row); empties();
      const { ok, data } = await post('/t/' + encodeURIComponent(row.dataset.key) + '/sprint', { sprint: sec.dataset.sprint });
      if (!ok) { source.querySelector('[data-rows]').prepend(row); empties(); flash(data.error || failed); return; }
      if (data.row) row.replaceWith(fromHTML(data.row));
    });
  });

  // story points and epic: saved on change
  root.addEventListener('change', async e => {
    const row = e.target.closest('.srow');
    if (!row) return;
    let url, params;
    if (e.target.matches('[data-points]')) { url = '/points'; params = { points: e.target.value }; }
    else if (e.target.matches('[data-epic]')) { url = '/epic'; params = { epic: e.target.value }; }
    else return;
    const { ok, data } = await post('/t/' + encodeURIComponent(row.dataset.key) + url, params);
    if (!ok) { flash(data.error || failed); return; }
    if (data.row) row.replaceWith(fromHTML(data.row));
  });

  // quick add to the backlog
  root.querySelectorAll('form[data-quick-row]').forEach(form => form.addEventListener('submit', async e => {
    e.preventDefault();
    const { ok, data } = await post(form.action, new FormData(form));
    if (!ok || !data.row) { flash(data.error || failed); return; }
    form.closest('[data-sprint]').querySelector('[data-rows]').prepend(fromHTML(data.row));
    empties(); form.reset(); form.querySelector('input[name=title]').focus();
  }));

  // «+ Эпик» opens the epics block (no reload)
  root.addEventListener('click', e => {
    const b = e.target.closest('[data-show]');
    if (!b) return;
    const el = document.getElementById(b.dataset.show);
    if (el) { el.hidden = false; el.scrollIntoView({ behavior: 'smooth', block: 'nearest' }); el.querySelector('input[name=name]')?.focus(); }
  });
})();
