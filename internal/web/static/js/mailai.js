// CoPilot beside the mail, as Copilot in Outlook: the button over the mail opens the pane (it stays open or closed
// for the next visit, on its last conversation; «Мой день» and it take turns); the pane comes from the server
// (web.mailCopilot) — the conversation with the mail agent, quick questions about the open letter and the inbox, and
// «Подтвердить / Отклонить» for the actions that change the mail. A confirmed change of letters reloads the list.
(() => {
  const pane = document.querySelector('[data-ai]');
  const toggle = document.querySelector('[data-ai-toggle]');
  if (!pane || !toggle) return;
  const get = k => { try { return localStorage.getItem(k); } catch { return null; } };
  const set = (k, v) => { try { localStorage.setItem(k, v); } catch { /* not kept: the pane is closed next time */ } };
  let busy = false;

  const letter = () => document.querySelector('[data-mail-read] [data-letter]')?.dataset.letter || '';
  const render = html => {
    pane.innerHTML = html;
    const log = pane.querySelector('[data-ai-log]');
    if (log) {
      set('mail.ai.run', log.dataset.run || '');
      log.scrollTop = log.scrollHeight;
    }
  };
  const load = async run => {
    try {
      const res = await fetch('/mail/copilot' + (run ? '?run=' + encodeURIComponent(run) : ''), { headers: { Accept: 'text/html' } });
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
      const res = await fetch(url, { method: 'POST', body: new URLSearchParams({ letter: letter(), ...fields }),
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
  const say = text => (text.trim() ? post('/mail/copilot', { run: run(), text }) : false);

  const show = open => {
    if (open) document.querySelector('[data-myday-toggle].on')?.click(); // one pane on the right at a time
    pane.hidden = !open;
    toggle.classList.toggle('on', open);
    toggle.setAttribute('aria-expanded', open);
    document.body.classList.toggle('with-myday', open); // the mail and its bar make room for it
    set('mail.ai', open ? '1' : '0');
    if (open) return load(get('mail.ai.run'));
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
    if (b.matches('[data-ai-delete]')) return post(`/mail/copilot/${encodeURIComponent(b.dataset.aiDelete)}/delete`, {});
    if (b.matches('[data-ai-say]')) return say(b.dataset.aiSay);
    if (b.matches('[data-ai-draft]')) { // what to answer is the employee's: the request waits in the box for it
      const box = pane.querySelector('[data-ai-form] textarea');
      box.value = b.dataset.aiDraft;
      box.focus();
      box.setSelectionRange(box.value.length, box.value.length);
      return;
    }
    if (b.matches('[data-ai-decide]')) {
      const ok = await post(`/mail/copilot/${encodeURIComponent(run())}/${encodeURIComponent(b.dataset.aiDecide)}`, { yes: b.dataset.yes });
      if (ok && b.hasAttribute('data-reload')) location.reload(); // the letters changed: the list shows them as they are now
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

  if (get('mail.ai') === '1') show(true);
})();
