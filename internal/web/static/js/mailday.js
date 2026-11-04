// «Мой день» beside the mail, as in Outlook: the button over the mail opens the pane (it stays open or closed for the
// next visit, on its last tab); the pane comes from the server (web.mailMyDay) — the day's meetings with ‹ › and one's
// open tasks; the box of a task completes it; a letter of the list dropped on a tab (held over a tab — it opens) opens
// the form of a meeting on the day shown or of a task made of the letter.
(() => {
  const pane = document.querySelector('[data-myday]');
  const toggle = document.querySelector('[data-myday-toggle]');
  if (!pane || !toggle) return;
  const get = k => { try { return localStorage.getItem(k); } catch { return null; } };
  const set = (k, v) => { try { localStorage.setItem(k, v); } catch { /* not kept: the pane is closed next time */ } };
  let day = '';

  const tab = t => {
    t = t === 'tasks' ? 'tasks' : 'cal';
    pane.querySelectorAll('[data-myday-tab]').forEach(b => {
      const on = b.dataset.mydayTab === t;
      b.classList.toggle('on', on);
      b.setAttribute('aria-selected', on);
    });
    pane.querySelectorAll('[data-myday-pane]').forEach(p => { p.hidden = p.dataset.mydayPane !== t; });
    set('mail.myday.tab', t);
  };
  const load = async () => {
    try {
      const res = await fetch('/mail/myday' + (day ? '?day=' + encodeURIComponent(day) : ''), { headers: { Accept: 'text/html' } });
      if (!res.ok) return;
      pane.innerHTML = await res.text();
      tab(get('mail.myday.tab'));
    } catch { /* the server could not be reached: the pane stays as it was */ }
  };
  const show = open => {
    if (open) document.querySelector('[data-ai-toggle].on')?.click(); // one pane on the right at a time (mailai.js)
    pane.hidden = !open;
    toggle.classList.toggle('on', open);
    toggle.setAttribute('aria-expanded', open);
    document.body.classList.toggle('with-myday', open); // the mail and its bar make room for it
    set('mail.myday', open ? '1' : '0');
    if (open) return load();
  };
  toggle.addEventListener('click', () => show(pane.hidden));

  pane.addEventListener('click', e => {
    const t = e.target.closest('[data-myday-tab]');
    if (t) return tab(t.dataset.mydayTab);
    const d = e.target.closest('[data-myday-day]');
    if (d) {
      day = d.dataset.mydayDay;
      return load();
    }
    if (e.target.closest('[data-myday-close]')) show(false);
  });

  // the box of a task: complete it (one that needs the creator's control waits for it, as on the task's page)
  pane.addEventListener('change', async e => {
    const box = e.target.closest('[data-myday-done]');
    if (!box?.checked) return;
    box.disabled = true;
    const undo = msg => {
      box.checked = box.disabled = false;
      if (msg) alert(msg);
    };
    try {
      const res = await fetch(`/t/${encodeURIComponent(box.dataset.mydayDone)}/action`, { method: 'POST',
        body: new URLSearchParams({ action: 'complete' }), headers: { Accept: 'application/json', 'X-Go24-Form': '1' } });
      if (!res.ok) return undo((await res.json().catch(() => null))?.error || res.statusText);
      box.closest('.mmyday-task')?.classList.add('done');
    } catch { undo(); }
  });

  // a letter of the list onto a tab
  let letter = null;
  const over = e => (letter ? e.target.closest?.('[data-myday-drop]') : null);
  const clear = () => pane.querySelectorAll('.drop').forEach(p => p.classList.remove('drop'));
  document.addEventListener('dragstart', e => { letter = e.target.closest?.('.mailrow[data-letter]')?.dataset.letter || null; });
  document.addEventListener('dragend', () => { letter = null; clear(); });
  pane.addEventListener('dragover', e => {
    const t = letter && e.target.closest?.('[data-myday-tab]');
    if (t && !t.classList.contains('on')) tab(t.dataset.mydayTab);
    const p = over(e) || (t && pane.querySelector(`[data-myday-pane="${t.dataset.mydayTab}"]`));
    if (!p) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move'; // the list lets its letters be moved only
    if (!p.classList.contains('drop')) { clear(); p.classList.add('drop'); }
  });
  pane.addEventListener('dragleave', e => { if (!pane.contains(e.relatedTarget)) clear(); });
  pane.addEventListener('drop', e => {
    const t = letter && e.target.closest?.('[data-myday-tab]');
    const p = over(e) || (t && pane.querySelector(`[data-myday-pane="${t.dataset.mydayTab}"]`));
    if (!p) return;
    e.preventDefault();
    clear();
    location.assign(p.dataset.mydayDrop + encodeURIComponent(letter));
  });

  if (get('mail.myday') === '1') show(true);
})();
