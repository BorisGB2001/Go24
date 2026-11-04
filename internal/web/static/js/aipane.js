// CoPilot of a section (templates/parts/aipane.html): the button over the page opens the pane on the right (it stays
// open or closed for the next visit, on its last conversation); the pane comes from the server (data-ai-url) — the
// conversation with the section's agent, quick questions, and «Подтвердить / Отклонить» for the actions that change
// data. The task open (data-ai-task) goes with every message; a confirmed change reloads the page to show it.
(() => {
  const pane = document.querySelector('[data-ai]');
  const toggle = document.querySelector('[data-ai-toggle]');
  if (!pane || !toggle) return;
  const base = pane.dataset.aiUrl;
  const task = pane.dataset.aiTask || '';
  const key = 'ai.' + base;
  const get = k => { try { return localStorage.getItem(k); } catch { return null; } };
  const set = (k, v) => { try { localStorage.setItem(k, v); } catch { /* not kept: the pane is closed next time */ } };
  let busy = false;

  const render = html => {
    pane.innerHTML = html;
    if (!task) pane.querySelectorAll('[data-ai-open]').forEach(b => b.remove()); // about «the open task»: none open
    const log = pane.querySelector('[data-ai-log]');
    if (log) {
      set(key + '.run', log.dataset.run || '');
      log.scrollTop = log.scrollHeight;
    }
  };
  const load = async run => {
    try {
      const res = await fetch(base + (run ? '?run=' + encodeURIComponent(run) : ''), { headers: { Accept: 'text/html' } });
      if (res.ok) render(await res.text());
    } catch { /* the server could not be reached: the pane stays as it was */ }
  };
  // post sends a form to the agent and shows its answer; the model may think a while
  const post = async (url, fields) => {
    if (busy) return false;
    busy = true;
    pane.querySelector('[data-ai-wait]')?.removeAttribute('hidden');
    pane.querySelectorAll('button, textarea').forEach(el => { el.disabled = true; });
    try {
      const res = await fetch(url, { method: 'POST', body: new URLSearchParams({ task, ...fields }),
        headers: { Accept: 'text/html', 'X-Go24-Form': '1' } });
      if (!res.ok) {
        alert((await res.json().catch(() => null))?.error || res.statusText);
        return false;
      }
      render(await res.text());
      return true;
    } catch {
      return false;
    } finally {
      busy = false;
      pane.querySelector('[data-ai-wait]')?.setAttribute('hidden', '');
      pane.querySelectorAll('button, textarea').forEach(el => { el.disabled = false; });
    }
  };
  const run = () => pane.querySelector('[data-ai-log]')?.dataset.run || '';
  const say = text => (text.trim() ? post(base, { run: run(), text }) : false);

  const show = open => {
    pane.hidden = !open;
    toggle.classList.toggle('on', open);
    toggle.setAttribute('aria-expanded', open);
    document.body.classList.toggle('with-aipane', open); // the page makes room for it
    set(key, open ? '1' : '0');
    if (open) return load(get(key + '.run'));
  };
  toggle.addEventListener('click', () => show(pane.hidden));

  pane.addEventListener('click', async e => {
    const b = e.target.closest('button');
    if (!b || busy) return;
    if (b.matches('[data-ai-close]')) return show(false);
    if (b.matches('[data-ai-run]')) {
      b.closest('details')?.removeAttribute('open');
      return load(b.dataset.aiRun);
    }
    if (b.matches('[data-ai-delete]')) return post(`${base}/${encodeURIComponent(b.dataset.aiDelete)}/delete`, {});
    if (b.matches('[data-ai-say]')) return say(b.dataset.aiSay);
    if (b.matches('[data-ai-fill]')) { // what to do is the employee's: the request waits in the box for it
      const box = pane.querySelector('[data-ai-form] textarea');
      box.value = b.dataset.aiFill;
      box.focus();
      box.setSelectionRange(box.value.length, box.value.length);
      return;
    }
    if (b.matches('[data-ai-decide]')) {
      const yes = b.dataset.yes === '1';
      const ok = await post(`${base}/${encodeURIComponent(run())}/${encodeURIComponent(b.dataset.aiDecide)}`, { yes: b.dataset.yes });
      if (ok && yes && !pane.querySelector('[data-ai-decide]')) location.reload(); // the data changed: the page shows it as it is now
    }
  });
  pane.addEventListener('submit', e => {
    if (!e.target.matches('[data-ai-form]')) return;
    e.preventDefault();
    say(e.target.elements.text.value);
  });
  pane.addEventListener('keydown', e => { // Enter sends, Shift+Enter is a new line
    if (e.key !== 'Enter' || e.shiftKey || e.isComposing || !e.target.matches('[data-ai-form] textarea')) return;
    e.preventDefault();
    e.target.form.requestSubmit();
  });

  if (get(key) === '1') show(true);
})();
