// «Открытые линии» (openlines.html): «Быстрые ответы» — typed after «/» in the reply or picked by the button; a file to
// the reply, «печатает…» both ways; the dialogue list, its tabs and the open dialogue refresh by themselves when a
// dialogue changes.
(() => {
  const root = document.querySelector('[data-ol]');
  if (!root) return;
  const msgs = () => root.querySelector('[data-ol-msgs]');
  const bottom = () => { const m = msgs(); if (m) m.scrollTop = m.scrollHeight; };
  bottom();

  // ---------- quick answers ----------
  const reply = root.querySelector('[data-ol-reply]');
  const box = root.querySelector('[data-ol-quick]');
  if (reply && box) {
    let list = [];
    try { list = JSON.parse(reply.closest('[data-ol-quick-list]').dataset.olQuickList) || []; } catch { /* none */ }
    let shown = [], at = 0, from = -1; // from: where «/…» starts in the reply, -1 — the list was opened by the button
    const close = () => { box.hidden = true; shown = []; };
    const open = (q, start) => {
      from = start;
      q = q.toLowerCase();
      shown = list.filter(x => !q || x.Title.toLowerCase().includes(q) || x.Body.toLowerCase().includes(q));
      box.replaceChildren();
      if (!shown.length) {
        if (q) return close();
        box.append(document.querySelector('[data-ol-quick-empty]').content.cloneNode(true));
      }
      at = 0;
      shown.forEach((x, i) => {
        const b = document.createElement('button');
        b.type = 'button';
        b.className = 'ghost' + (i === at ? ' on' : '');
        const t = document.createElement('b');
        t.textContent = x.Title;
        const p = document.createElement('div');
        p.className = 'small muted';
        p.textContent = x.Body.length > 80 ? x.Body.slice(0, 80) + '…' : x.Body;
        b.append(t, p);
        b.addEventListener('mousedown', e => { e.preventDefault(); put(i); });
        box.append(b);
      });
      box.hidden = false;
    };
    const mark = () => box.querySelectorAll('button').forEach((b, i) => b.classList.toggle('on', i === at));
    const put = i => {
      const x = shown[i];
      if (!x) return;
      const end = reply.selectionStart;
      const start = from >= 0 ? from : end;
      reply.value = reply.value.slice(0, start) + x.Body + reply.value.slice(end);
      reply.selectionStart = reply.selectionEnd = start + x.Body.length;
      close();
      reply.focus();
    };
    // «/» at the start of the reply or of a word opens the answers whose title or text has what follows it
    reply.addEventListener('input', () => {
      const before = reply.value.slice(0, reply.selectionStart);
      const m = before.match(/(^|\s)\/([^\s/]*)$/);
      if (m) open(m[2], before.length - m[2].length - 1);
      else if (from >= 0) close();
    });
    reply.addEventListener('keydown', e => {
      if (box.hidden || !shown.length) {
        if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); reply.form.requestSubmit(); }
        if (e.key === 'Escape') close();
        return;
      }
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault();
        at = (at + (e.key === 'ArrowDown' ? 1 : shown.length - 1)) % shown.length;
        mark();
      } else if (e.key === 'Enter' || e.key === 'Tab') {
        e.preventDefault();
        put(at);
      } else if (e.key === 'Escape') {
        e.preventDefault();
        close();
      }
    });
    reply.addEventListener('blur', () => setTimeout(close, 150));
    root.querySelector('[data-ol-quick-btn]')?.addEventListener('click', () => {
      if (!box.hidden) return close();
      reply.focus();
      open('', -1);
    });
  }

  // ---------- the reply: a file, «печатает…» ----------
  const form = root.querySelector('[data-ol-typing-url]');
  if (form) {
    const file = form.querySelector('[data-ol-file]'), label = form.querySelector('[data-ol-file-name]'), text = form.querySelector('[name="body"]');
    file?.addEventListener('change', () => { label.textContent = file.files[0]?.name || ''; });
    form.addEventListener('submit', e => { // a text, a file or both
      if (!text.value.trim() && !file?.files.length) { e.preventDefault(); text.focus(); }
    });
    const hidden = form.querySelector('[data-ol-hidden]'), ph = text.placeholder;
    hidden?.addEventListener('change', () => { // «Скрытое»: only employees see the message
      text.placeholder = hidden.checked ? hidden.dataset.ph : ph;
      form.classList.toggle('hidden-msg', hidden.checked);
    });
    let typed = 0;
    text.addEventListener('input', () => { // a hidden message is not shown to the visitor as «печатает…»
      if (Date.now() - typed < 3000 || !text.value.trim() || hidden?.checked) return;
      typed = Date.now();
      fetch(form.dataset.olTypingUrl, { method: 'POST', headers: { Accept: 'application/json' } }).catch(() => {});
    });
  }

  // ---------- live ----------
  if (!window.EventSource) return;
  let timer = null, current = false;
  const refresh = async () => {
    timer = null;
    try {
      const r = await fetch(location.href, { headers: { Accept: 'text/html' } });
      if (!r.ok) return;
      const doc = new DOMParser().parseFromString(await r.text(), 'text/html');
      for (const sel of ['[data-ol-list]', '[data-ol-tabs]']) {
        const a = root.querySelector(sel), b = doc.querySelector(sel);
        if (a && b) a.innerHTML = b.innerHTML;
      }
      const a = msgs(), b = doc.querySelector('[data-ol-msgs]');
      if (current && a && b) {
        const end = a.scrollHeight - a.scrollTop - a.clientHeight < 40;
        a.innerHTML = b.innerHTML;
        if (end) bottom();
      }
    } catch { /* the next change refreshes it */ }
    current = false;
  };
  const es = new EventSource('/openlines/live');
  let typing = null;
  es.addEventListener('typing', e => { // the visitor is typing in the open dialogue
    const t = root.querySelector('[data-ol-typing]');
    if (!t || e.data !== root.dataset.current) return;
    t.hidden = false;
    clearTimeout(typing);
    typing = setTimeout(() => { t.hidden = true; }, 6000);
  });
  es.addEventListener('session', e => {
    if (e.data === root.dataset.current) { current = true; const t = root.querySelector('[data-ol-typing]'); if (t) t.hidden = true; }
    if (!timer) timer = setTimeout(refresh, 400);
  });
  addEventListener('pagehide', () => es.close());
})();
