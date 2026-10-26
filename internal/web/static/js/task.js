// go24 task card: optional blocks opened by chips, the description clamp and the task chat ([data-copy] — forms.js).
// Everything here works on the page itself; only the chat talks to the server (and the page stays put).
(() => {
  const card = document.querySelector('.tcard, [data-blocks]'); // task card, or another page with hidden blocks
  if (!card) return;

  const show = (id, focus) => {
    const block = document.getElementById(id);
    if (!block) return;
    block.hidden = false;
    card.querySelectorAll(`.tchip[data-show="${id}"]`).forEach(c => { c.hidden = true; });
    card.querySelectorAll('details[open]').forEach(d => { d.open = false; });
    block.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
    if (focus) card.querySelector(focus)?.focus();
  };
  card.addEventListener('click', e => {
    const s = e.target.closest('[data-show]');
    if (s) { show(s.dataset.show, s.dataset.focus); return; }
    const h = e.target.closest('[data-hide]');
    if (h) document.getElementById(h.dataset.hide).hidden = true;
  });
  // forms[data-ajax]: sent in the background; on an error the form stays as typed and shows the message
  card.querySelectorAll('form[data-ajax]').forEach(form => form.addEventListener('submit', async e => {
    e.preventDefault();
    const box = form.querySelector('[data-error]');
    let res, data = {};
    try {
      res = await fetch(form.action, { method: 'POST', headers: { Accept: 'application/json' }, body: new URLSearchParams(new FormData(form)) });
      try { data = await res.json(); } catch { data = {}; }
    } catch { res = { ok: false }; }
    if (res.ok) { location.href = data.redirect || location.href; return; }
    if (box) { box.textContent = data.error || form.dataset.failed; box.hidden = false; box.scrollIntoView({ behavior: 'smooth', block: 'nearest' }); }
  }));
  // a [data-autosubmit] input or select is sent at once (employee photo, the type of a scrum task)
  card.querySelectorAll('input[data-autosubmit], select[data-autosubmit]').forEach(i => i.addEventListener('change', () => i.form.submit()));
  // a link or a redirect to #checklist, #time… opens that block
  if (location.hash.length > 1) {
    const id = decodeURIComponent(location.hash.slice(1));
    if (document.getElementById(id)?.hidden) show(id);
  }

  // checklists: «Скрыть выполненные» (remembered), a list renamed in place, an item edited on a double click,
  // Enter adds an item (a pasted list — many), items dragged within a list or to another one
  const cl = card.querySelector('[data-checklist]');
  if (cl) {
    const hideBtn = cl.querySelector('[data-ck-hide]');
    const hide = on => {
      cl.classList.toggle('hide-done', on);
      if (hideBtn) hideBtn.querySelector('span').textContent = on ? hideBtn.dataset.showText : hideBtn.dataset.hideText;
      try { localStorage.setItem('go24.checklist.hide', on ? '1' : ''); } catch {}
    };
    try { if (localStorage.getItem('go24.checklist.hide')) hide(true); } catch {}
    hideBtn?.addEventListener('click', () => hide(!cl.classList.contains('hide-done')));
    cl.querySelectorAll('input[data-ck-name]').forEach(i => {
      i.addEventListener('change', () => i.form.requestSubmit());
    });
    cl.querySelectorAll('textarea[data-ck-new]').forEach(t => {
      const grow = () => { t.rows = Math.min(8, t.value.split('\n').length); };
      t.addEventListener('input', grow);
      t.addEventListener('keydown', e => {
        if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) { e.preventDefault(); if (t.value.trim()) t.form.requestSubmit(); }
      });
    });
    cl.addEventListener('dblclick', e => {
      const t = e.target.closest('[data-ck-edit]');
      if (!t) return;
      const form = t.parentElement.querySelector('.ck-edit'), input = form.elements.title;
      t.hidden = true; form.hidden = false; input.focus(); input.select();
      const back = () => { input.value = t.textContent; form.hidden = true; t.hidden = false; };
      input.onkeydown = k => { if (k.key === 'Escape') back(); };
      input.onblur = () => { if (input.value.trim() && input.value.trim() !== t.textContent) form.requestSubmit(); else back(); };
    });
    let dragged = null;
    cl.addEventListener('dragstart', e => {
      dragged = e.target.closest?.('.ck-item[draggable="true"]');
      if (!dragged) return;
      dragged.classList.add('dragging');
      e.dataTransfer.effectAllowed = 'move';
      e.dataTransfer.setData('text/plain', dragged.dataset.item);
    });
    cl.addEventListener('dragover', e => {
      const box = dragged && e.target.closest('[data-ck-items]');
      if (!box) return;
      e.preventDefault();
      const after = [...box.querySelectorAll('.ck-item:not(.dragging)')].find(i => {
        const r = i.getBoundingClientRect();
        return e.clientY < r.top + r.height / 2;
      });
      if (after) box.insertBefore(dragged, after); else box.append(dragged);
    });
    cl.addEventListener('dragend', async () => {
      if (!dragged) return;
      const item = dragged, from = item.dataset.from;
      dragged = null;
      item.classList.remove('dragging');
      const list = item.closest('[data-list]').dataset.list, before = item.nextElementSibling?.dataset.item || '';
      const form = item.querySelector('form');
      const body = new URLSearchParams({ op: 'move', list, before });
      let ok = false;
      try { ok = (await fetch(form.getAttribute('action'), { method: 'POST', headers: { 'X-Go24-Form': '1' }, body })).ok; } catch {}
      // another list's progress changed, or the move failed: the page as the server has it
      if (!ok || list !== from) location.reload();
    });
    cl.querySelectorAll('.ck-item').forEach(i => { i.dataset.from = i.closest('[data-list]').dataset.list; });
  }

  // long descriptions are clamped, with «Развернуть»
  const desc = card.querySelector('[data-clamp]');
  const expand = card.querySelector('[data-expand]');
  if (desc && expand) {
    const more = expand.textContent;
    expand.hidden = desc.scrollHeight <= desc.clientHeight + 4;
    expand.addEventListener('click', () => {
      const open = desc.classList.toggle('clamp') === false;
      expand.textContent = open ? expand.dataset.less : more;
    });
  }

  // task chat
  const msgs = card.querySelector('[data-msgs]');
  const form = card.querySelector('form[data-chat]');
  if (!msgs || !form) return;
  const bottom = () => { msgs.scrollTop = msgs.scrollHeight; };
  bottom();
  const text = form.querySelector('textarea');
  const grow = () => { text.style.height = 'auto'; text.style.height = Math.min(text.scrollHeight, 200) + 'px'; };
  text.addEventListener('input', grow);
  text.addEventListener('keydown', e => {
    if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) { e.preventDefault(); form.requestSubmit(); }
  });
  // live: messages written elsewhere (a colleague, the messenger) appear at once
  const add = (id, html) => {
    const cur = msgs.querySelector(`.msg[data-id="${CSS.escape(id)}"]`);
    if (cur) { // our own post is already here, or the message changed (reactions): show the new version
      const t = document.createElement('template');
      t.innerHTML = html.trim();
      const fresh = t.content.querySelector('.msg');
      if (fresh) cur.replaceWith(fresh);
      return;
    }
    const near = msgs.scrollHeight - msgs.scrollTop - msgs.clientHeight < 80;
    msgs.querySelector('.chat-empty')?.remove();
    const t = document.createElement('template');
    t.innerHTML = html.trim();
    msgs.append(t.content);
    if (near) bottom();
  };
  // a message seen in the open chat is not news: its notifications are read (after the bell has heard of them)
  let readTimer = 0;
  const readNews = () => {
    clearTimeout(readTimer);
    readTimer = setTimeout(async () => {
      try {
        const res = await fetch('/notifications/read', { method: 'POST', headers: { Accept: 'application/json' },
          body: new URLSearchParams({ task: card.dataset.task }) });
        if (!res.ok) return;
        const d = await res.json();
        document.querySelectorAll('[data-counter="notify"]').forEach(c => { c.textContent = d.unread > 99 ? '99+' : d.unread; c.hidden = !d.unread; });
        if ('BroadcastChannel' in window) { const bc = new BroadcastChannel('go24-notify'); bc.postMessage({ unread: d.unread }); bc.close(); }
      } catch { /* stays unread */ }
    }, 1500);
  };
  if (window.EventSource && card.dataset.task) {
    const es = new EventSource('/t/' + encodeURIComponent(card.dataset.task) + '/live');
    es.addEventListener('message', e => {
      try {
        const d = JSON.parse(e.data);
        add(d.id, d.html);
        if (document.visibilityState === 'visible') readNews(); else unseen = true;
      } catch { /* ignore a broken event */ }
    });
  }
  let unseen = false; // came while the tab was hidden: read when the person looks
  document.addEventListener('visibilitychange', () => {
    if (unseen && document.visibilityState === 'visible') { unseen = false; readNews(); }
  });
  // reactions: a click on an emoji under a message puts it or takes it back
  msgs.addEventListener('click', async e => {
    const b = e.target.closest('[data-react]');
    if (!b) return;
    const msg = b.closest('.msg');
    b.closest('details')?.removeAttribute('open');
    try {
      const res = await fetch(`/t/${encodeURIComponent(card.dataset.task)}/comments/${encodeURIComponent(msg.dataset.id)}/react`, {
        method: 'POST', headers: { Accept: 'application/json' }, body: new URLSearchParams({ emoji: b.dataset.react }) });
      if (!res.ok) return;
      const d = await res.json();
      add(d.id, d.html);
    } catch { /* stays as it was */ }
  });
  let busy = false;
  form.addEventListener('submit', async e => {
    e.preventDefault();
    if (busy || !text.value.trim()) return;
    busy = true;
    let ok = false, data = {};
    try {
      const res = await fetch(form.action, { method: 'POST', headers: { Accept: 'application/json' }, body: new URLSearchParams(new FormData(form)) });
      ok = res.ok;
      try { data = await res.json(); } catch { data = {}; }
    } catch { ok = false; }
    busy = false;
    let err = form.querySelector('.chat-error');
    if (!ok || !data.html) {
      if (!err) { err = document.createElement('div'); err.className = 'chat-error'; form.prepend(err); }
      err.textContent = data.error || form.dataset.failed;
      return;
    }
    err?.remove();
    add(data.id, data.html);
    form.reset(); grow(); bottom(); text.focus();
  });
})();
