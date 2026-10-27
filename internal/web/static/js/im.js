// go24 messenger: chats, messages and threads come from Matrix through go24, so they are the same as in Element.
(() => {
  const im = document.querySelector('[data-im]');
  if (!im) return;
  const room = im.dataset.room;
  const failed = im.dataset.failed;
  const msgs = im.querySelector('[data-msgs]');
  const pane = im.querySelector('[data-thread-pane]');
  const tmsgs = im.querySelector('[data-thread-msgs]');
  let thread = '';
  const node = (html) => { const t = document.createElement('template'); t.innerHTML = html.trim(); return t.content; };
  const get = async (url) => { try { const r = await fetch(url, { headers: { Accept: 'application/json' } }); return { ok: r.ok, data: await r.json() }; } catch { return { ok: false, data: {} }; } };
  const post = async (url, params) => {
    try { const r = await fetch(url, { method: 'POST', headers: { Accept: 'application/json' }, body: new URLSearchParams(params) }); return { ok: r.ok, data: await r.json() }; }
    catch { return { ok: false, data: {} }; }
  };
  const bottom = (el) => { el.scrollTop = el.scrollHeight; };
  const has = (el, id) => el.querySelector(`[data-id="${CSS.escape(id)}"]`);

  // the card of the first link of a message (title, text and picture of the page); go24 fetches
  // the page and its picture, links inside go24 have none
  const cards = new Map();
  const linkCard = (msg) => {
    const t = msg.querySelector(':scope > .bubble > .text');
    msg.querySelector('.im-link')?.remove();
    const a = t && [...t.querySelectorAll('a[href]')].find(a => /^https?:$/.test(a.protocol) && a.host !== location.host && !a.closest('pre, code, blockquote'));
    if (!a) return;
    const url = a.href;
    if (!cards.has(url)) cards.set(url, get('/im/link?url=' + encodeURIComponent(url)).then(({ ok, data }) => ok && data.title ? data : null));
    cards.get(url).then(p => {
      if (!p || !msg.isConnected || msg.querySelector('.im-link')) return;
      const c = document.createElement('a');
      c.className = 'im-link'; c.href = url; c.target = '_blank'; c.rel = 'noopener noreferrer';
      if (p.image) { const i = document.createElement('img'); i.loading = 'lazy'; i.alt = ''; i.src = p.image; i.onerror = () => i.remove(); c.append(i); }
      const b = document.createElement('span');
      [['site', p.site], ['title', p.title], ['desc', p.text]].forEach(([k, v]) => { if (!v) return; const e = document.createElement(k === 'title' ? 'b' : 'span'); e.className = k; e.textContent = v; b.append(e); });
      c.append(b);
      t.after(c);
    });
  };
  const linkCards = (root) => (root.matches?.('.msg') ? [root] : [...(root.querySelectorAll?.('.msg') || [])]).forEach(linkCard);
  [msgs, tmsgs].forEach(box => {
    if (!box) return;
    linkCards(box);
    new MutationObserver(list => list.forEach(r => r.addedNodes.forEach(linkCards))).observe(box, { childList: true });
  });

  // the chat list: a tab («Все», «Коллабы», «Каналы» and the search box filter it together;
  // «Звонки» shows the call history instead of the chats
  let tab = im.dataset.tab || 'all';
  const filter = im.querySelector('[data-filter]');
  const callsBox = im.querySelector('[data-calls]');
  // open chats one is not in: the channels on «Каналы», any of them when searched for
  const openBox = im.querySelector('[data-open-list]');
  const applyOpen = (q) => {
    if (!openBox) return;
    let n = 0;
    openBox.querySelectorAll('[data-open-row]').forEach(a => {
      a.hidden = q !== '' ? !a.dataset.name.toLowerCase().includes(q) : !(a.classList.contains('on') || (tab === 'channel' && a.dataset.kind === 'channel'));
      if (!a.hidden) n++;
    });
    openBox.hidden = !n || tab === 'calls';
  };
  const applyFilter = () => {
    const q = (filter?.value || '').toLowerCase();
    im.querySelectorAll('[data-list] .im-room').forEach(a => {
      const row = a.closest('[data-row]') || a; // a hidden chat: only when searched for
      row.hidden = (tab !== 'all' && a.dataset.kind !== tab && a.dataset.in !== tab) || (q !== '' && !a.dataset.name.toLowerCase().includes(q)) || (q === '' && row.dataset.hidden === '1');
    });
    callsBox?.querySelectorAll('[data-call]').forEach(row => { row.hidden = q !== '' && !row.dataset.name.toLowerCase().includes(q); });
    applyOpen(q);
    im.querySelectorAll('[data-im-tab]').forEach(b => b.classList.toggle('on', b.dataset.imTab === tab));
    im.querySelectorAll('[data-tab-empty]').forEach(p => { p.hidden = p.dataset.tabEmpty !== tab || im.querySelector(`.im-room[data-kind="${tab}"], .im-room[data-in="${tab}"]`) !== null; });
    if (callsBox) callsBox.hidden = tab !== 'calls';
  };
  // «Звонки»: all the calls or only the missed ones; looking at them clears the tab's counter
  const loadCalls = async (missed) => {
    const { ok, data } = await get('/im/calls' + (missed ? '?missed=1' : ''));
    if (!ok) { callsBox.textContent = data.error || failed; return; }
    callsBox.innerHTML = data.html;
    const c = im.querySelector('[data-calls-unseen]');
    if (c) c.hidden = true;
    applyFilter();
  };
  callsBox?.addEventListener('click', e => {
    const b = e.target.closest('[data-calls-filter]');
    if (!b) return;
    e.preventDefault();
    loadCalls(b.dataset.callsFilter === '1');
  });
  // the unread messages of a tab's chats, beside its name
  const tabCounts = () => {
    im.querySelectorAll('[data-im-tab]').forEach(b => {
      const c = b.querySelector('[data-tab-unread]');
      if (!c) return;
      let n = 0;
      im.querySelectorAll(`.im-room[data-kind="${b.dataset.imTab}"] [data-unread], .im-room[data-in="${b.dataset.imTab}"] [data-unread]`).forEach(u => { if (!u.hidden) n += parseInt(u.textContent, 10) || 0; });
      c.textContent = n > 99 ? '99+' : n; c.hidden = !n;
    });
  };
  filter?.addEventListener('input', applyFilter);
  // a chat's own marks in the list (its ⋯): pinned at the top, muted, marked unread; the row shows them
  const list = im.querySelector('[data-list]');
  const rowOf = id => im.querySelector(`.im-room[data-room-id="${CSS.escape(id)}"]`)?.closest('[data-row]');
  const place = row => { // to the top of its block: pinned chats first, then the others
    if (row.dataset.favorite === '1') { list.prepend(row); return; }
    const pinned = [...list.querySelectorAll('[data-row][data-favorite="1"]')].filter(r => r !== row);
    if (pinned.length) pinned.at(-1).after(row); else list.prepend(row);
  };
  const markKey = { favorite: 'favorite', mute: 'muted', unread: 'marked' };
  im.addEventListener('click', async e => {
    const b = e.target.closest('[data-room-mark]');
    if (!b) return;
    const row = b.closest('[data-row]'), key = markKey[b.dataset.roomMark];
    b.closest('details')?.removeAttribute('open');
    const on = row.dataset[key] !== '1';
    const { ok, data } = await post('/im/mark', { room: row.querySelector('.im-room').dataset.roomId, mark: b.dataset.roomMark, on: on ? '1' : '0' });
    if (!ok) { alert(data.error || failed); return; }
    row.dataset[key] = on ? '1' : '';
    if (key === 'favorite') place(row);
  });
  // «Скрыть чат»: out of the list until a new message comes (Matrix keeps the mark); the open one closes
  im.addEventListener('click', async e => {
    const b = e.target.closest('[data-room-hide]');
    if (!b) return;
    const row = b.closest('[data-row]'), id = row.querySelector('.im-room').dataset.roomId;
    b.closest('details')?.removeAttribute('open');
    const { ok, data } = await post('/im/mark', { room: id, mark: 'hidden', on: '1' });
    if (!ok) { alert(data.error || failed); return; }
    row.dataset.hidden = '1';
    if (id === room) { location.href = '/im'; return; }
    applyFilter();
  });
  im.addEventListener('click', e => {
    const b = e.target.closest('[data-im-tab]');
    if (!b) return;
    e.preventDefault();
    tab = b.dataset.imTab;
    applyFilter();
    if (tab === 'calls') loadCalls(false);
  });
  applyFilter();
  if (tab === 'calls') loadCalls(false);

  // the thread's field holds the open thread's draft: what is typed there is kept when the thread is left
  const tform = im.querySelector('[data-send-thread]');
  const leaveThread = () => {
    if (tform) { unmode(tform); tform.dispatchEvent(new CustomEvent('thread', { detail: {} })); }
    thread = '';
  };
  const closeThread = () => { pane.hidden = true; leaveThread(); };
  const openThread = async (root) => {
    if (thread && thread !== root) leaveThread();
    thread = root;
    pane.hidden = false;
    setReplies(root, 0, 0);
    const sp = im.querySelector('[data-search-pane]');
    if (sp) sp.hidden = true;
    tmsgs.innerHTML = '';
    const { ok, data } = await get('/im/messages?room=' + encodeURIComponent(room) + '&thread=' + encodeURIComponent(root));
    if (ok) { tmsgs.append(node(data.html)); bottom(tmsgs); }
    if (ok && thread === root) tform?.dispatchEvent(new CustomEvent('thread', { detail: { root, draft: data.draft || '' } }));
  };
  // reactions: put or take back, then the chat (or the thread) is loaded again with fresh counts
  const reload = async (box, url) => {
    const { ok, data } = await get(url);
    if (!ok) return;
    const keep = box.scrollTop;
    box.innerHTML = '';
    box.append(node(data.html));
    box.scrollTop = keep;
  };
  const react = async (msg, emoji) => {
    const inThread = tmsgs && tmsgs.contains(msg);
    const { ok, data } = await post('/im/react', { room, event: msg.dataset.id, emoji });
    if (!ok) { alert(data.error || failed); return; }
    if (inThread) openThread(thread);
    else reload(msgs, '/im/messages?room=' + encodeURIComponent(room));
  };
  im.addEventListener('click', e => {
    const r = e.target.closest('[data-react]');
    if (!r) return;
    r.closest('details')?.removeAttribute('open');
    react(r.closest('.msg'), r.dataset.react);
  });
  // a double click on a message puts «Нравится» (👍) or takes it back; not on its links, buttons, media
  im.addEventListener('dblclick', e => {
    const msg = e.target.closest('.msg');
    if (!msg || !msg.querySelector('.react-add') || e.target.closest('a, button, summary, details, audio, video, input, textarea, .im-poll')) return;
    getSelection()?.removeAllRanges();
    react(msg, '👍');
  });
  im.addEventListener('click', e => {
    const b = e.target.closest('[data-open-thread]');
    if (b) { e.preventDefault(); openThread(b.dataset.openThread); return; }
    if (e.target.closest('[data-close-thread]')) closeThread();
  });

  // unread counters: the open chat, while the tab is in view, has none — it is read as it comes
  const looking = () => document.visibilityState === 'visible';
  const setBadge = (id, n) => {
    const c = im.querySelector(`.im-room[data-room-id="${CSS.escape(id)}"] [data-unread]`);
    if (!c) return;
    if (id === room && looking()) n = 0;
    c.textContent = n > 99 ? '99+' : n; c.hidden = !n;
    tabCounts();
  };
  // threads: the unread replies of the chat's threads beside its own count and on «Треды» of the open chat; under a
  // thread's root — «Ответов: N» and «новых: M» (add — to the count of replies, fresh — the new count, null — kept)
  function setThreads(id, n) {
    const c = im.querySelector(`.im-room[data-room-id="${CSS.escape(id)}"] [data-threads]`);
    if (c) { c.dataset.threads = n; c.querySelector('i').textContent = n > 99 ? '99+' : n; c.hidden = !n; }
    const h = id === room && im.querySelector('[data-threads-cnt]');
    if (h) { h.textContent = n > 99 ? '99+' : n; h.hidden = !n; }
  }
  function setReplies(root, add, fresh) {
    const b = msgs && has(msgs, root)?.querySelector('[data-open-thread]');
    if (!b) return;
    const n = (parseInt(b.dataset.replies, 10) || 0) + add, k = fresh ?? (parseInt(b.dataset.new, 10) || 0);
    b.dataset.replies = n; b.dataset.new = k;
    if (n) b.querySelector('[data-replies-text]').textContent = im.dataset.repliesFmt.replace('%s', n);
    const t = b.querySelector('[data-new-text]');
    t.textContent = im.dataset.repliesNewFmt.replace('%s', k); t.hidden = !k;
    b.classList.toggle('new', k > 0);
  }
  // «@»: the person is mentioned in the chat's unread messages (Matrix highlights)
  const setAt = (id, n) => {
    const c = im.querySelector(`.im-room[data-room-id="${CSS.escape(id)}"] [data-mention]`);
    if (!c) return;
    if (id === room && looking()) n = 0;
    c.dataset.mention = n; c.hidden = !n;
  };
  tabCounts();
  let reading = false;
  const markRead = async () => {
    if (!room || reading) return;
    reading = true;
    setBadge(room, 0);
    await post('/im/read', { room });
    reading = false;
  };
  document.addEventListener('visibilitychange', () => { if (looking()) markRead(); });
  // «Прочитать всё»: every chat of the list is read, the marked ones too
  im.querySelector('[data-read-all]')?.addEventListener('click', async () => {
    const { ok, data } = await post('/im/readall', {});
    if (!ok) { alert(data.error || failed); return; }
    im.querySelectorAll('.im-room[data-room-id]').forEach(a => { setBadge(a.dataset.roomId, 0); setAt(a.dataset.roomId, 0); });
    im.querySelectorAll('[data-row][data-marked="1"]').forEach(r => { r.dataset.marked = ''; });
  });

  // a message shown for a moment: found, pinned, quoted
  const flash = m => { m.scrollIntoView({ block: 'center', behavior: 'smooth' }); m.classList.add('flash-msg'); setTimeout(() => m.classList.remove('flash-msg'), 1500); };
  const latest = im.querySelector('[data-latest]');
  // the chat from its newest messages, or the page around one of them (?at=, a search result): then «К последним»
  const load = async (at) => {
    const { ok, data } = await get('/im/messages?room=' + encodeURIComponent(room) + (at ? '&at=' + encodeURIComponent(at) : ''));
    msgs.innerHTML = '';
    if (!ok) { msgs.textContent = data.error || failed; return false; }
    msgs.append(node(data.html));
    if (latest) latest.hidden = !at;
    const m = at && has(msgs, at);
    if (m) flash(m); else bottom(msgs);
    return true;
  };
  latest?.addEventListener('click', () => load(''));
  // the button «@» of the open chat goes through the mentions not seen yet, oldest first
  const atBtn = im.querySelector('[data-at-next]');
  let atQueue = [];
  const showAt = () => { if (!atBtn) return; atBtn.hidden = !atQueue.length; atBtn.querySelector('[data-at-count]').textContent = atQueue.length; };
  atBtn?.addEventListener('click', () => {
    const id = atQueue.shift(); showAt();
    const m = id && has(msgs, id);
    if (m) flash(m);
  });
  const goto = async (id, root) => {
    if (root) { await openThread(root); const m = has(tmsgs, id); if (m) flash(m); return; }
    const m = has(msgs, id) || has(tmsgs, id);
    if (m) flash(m); else await load(id);
  };
  if (msgs && room) {
    setBadge(room, 0); // the server reads the chat as it gives its messages, and takes off its «непрочитанный» mark
    const open = rowOf(room);
    if (open) open.dataset.marked = '';
    const at = parseInt(open?.querySelector('[data-mention]')?.dataset.mention, 10) || 0; // unread mentions, before reading
    setAt(room, 0);
    load(im.dataset.at).then(ok => {
      if (!ok) return;
      if (at) { atQueue = [...msgs.querySelectorAll('.msg.to-me')].slice(-at).map(m => m.dataset.id); showAt(); }
      if (im.dataset.thread) openThread(im.dataset.thread);
    });
  }

  // «печатает…»: the chat is told while the person types (once in a few seconds), and when they send
  let typed = 0;
  const typing = on => {
    if (!room || (on && Date.now() - typed < 4000) || (!on && !typed)) return;
    typed = on ? Date.now() : 0;
    post('/im/typing', { room, on: on ? '1' : '0' });
  };
  // the draft: the unsent text of the main field is kept for the chat (on the server, so the other
  // devices and the chat list see it) a moment after typing and when the page is left; a message being edited is not
  // a draft — the draft comes back when the edit ends. The thread's field keeps one for each thread: the open one's
  // comes with its messages (the event «thread»), and what is typed is kept when the thread is left
  const draft = (form, text, where) => {
    let saved = where === 'main' ? text.dataset.draft || '' : '', timer = 0, held = null;
    let root = where === 'main' ? '' : null; // the thread whose draft the field holds; null — none open
    if (where === 'main') text.value = saved;
    const keep = (beacon) => {
      clearTimeout(timer);
      if (root === null) return;
      const v = held ?? (form.dataset.edit ? saved : text.value);
      if (v.trim() === saved.trim()) return;
      saved = v;
      const body = new URLSearchParams(root ? { room, thread: root, text: v } : { room, text: v });
      if (beacon && navigator.sendBeacon) navigator.sendBeacon('/im/draft', body);
      else fetch('/im/draft', { method: 'POST', body }).catch(() => {});
    };
    text.addEventListener('input', () => { if (form.dataset.edit) return; clearTimeout(timer); timer = setTimeout(keep, 1000); });
    form.addEventListener('sent', () => { held = null; text.value = ''; keep(); });
    form.addEventListener('editing', () => { if (held === null) { keep(); held = text.value; } });
    form.addEventListener('edited', () => { if (held !== null) { text.value = held; held = null; } });
    addEventListener('pagehide', () => keep(true));
    form.addEventListener('thread', e => {
      keep(); held = null;
      root = e.detail.root ?? null; saved = e.detail.draft || ''; text.value = saved;
    });
  };
  const sender = (form, where) => {
    if (!form) return;
    const text = form.querySelector('textarea');
    // «Настройки»: Enter sends (Shift+Enter — a new line) or Ctrl+Enter sends (Enter — a new line)
    const ctrl = im.hasAttribute('data-ctrl-enter');
    text.addEventListener('keydown', e => {
      if (e.key !== 'Enter' || e.shiftKey || e.isComposing || (ctrl && !(e.ctrlKey || e.metaKey))) return;
      e.preventDefault(); form.requestSubmit();
    });
    text.addEventListener('input', () => { if (text.value.trim()) typing(true); });
    draft(form, text, where);
    text.addEventListener('keydown', e => { // Ctrl+B / I / U / Shift+X: the selection in BB codes
      if (!(e.ctrlKey || e.metaKey) || e.altKey) return;
      const tag = { b: 'b', i: 'i', u: 'u', x: e.shiftKey ? 's' : '' }[e.key.toLowerCase()];
      if (!tag || (e.shiftKey && tag !== 's')) return;
      e.preventDefault();
      const { selectionStart: a, selectionEnd: z, value } = text;
      text.setRangeText(`[${tag}]${value.slice(a, z)}[/${tag}]`, a, z, a === z ? 'start' : 'select');
      if (a === z) text.selectionStart = text.selectionEnd = a + tag.length + 2;
    });
    form.addEventListener('submit', async e => {
      e.preventDefault();
      if (!text.value.trim()) return;
      typing(false);
      const box = where === 'thread' ? tmsgs : msgs;
      if (form.dataset.edit) { // «Изменить»: the message takes its new text in place
        const { ok, data } = await post('/im/edit', { room, event: form.dataset.edit, body: text.value });
        if (!ok) { alert(data.error || failed); return; }
        has(box, data.id)?.replaceWith(node(data.html));
        form.reset(); unmode(form);
        return;
      }
      const params = { room, body: text.value };
      if (where === 'thread') params.thread = thread;
      if (form.dataset.reply) params.reply = form.dataset.reply;
      const { ok, data } = await post('/im/send', params);
      if (!ok) { alert(data.error || failed); return; }
      if (!has(box, data.id)) box.append(node(data.html));
      form.reset(); unmode(form); bottom(box);
      form.dispatchEvent(new Event('sent'));
    });
    text.addEventListener('keydown', e => { if (e.key === 'Escape' && (form.dataset.edit || form.dataset.reply)) { form.reset(); unmode(form); } });
    // ↑ in an empty field edits one's last message
    text.addEventListener('keydown', e => {
      if (e.key !== 'ArrowUp' || e.shiftKey || e.ctrlKey || e.altKey || e.metaKey || text.value !== '' || form.dataset.edit) return;
      const box = where === 'thread' ? tmsgs : msgs;
      const last = [...box.querySelectorAll('.msg')].reverse().find(m => m.querySelector(':scope > .bubble > .im-menu [data-act="edit"]'));
      if (!last) return;
      e.preventDefault();
      mode(form, 'edit', last);
      last.scrollIntoView({ block: 'nearest' });
    });
  };
  // the text of a message as typed: its line breaks (<br>) back to new lines
  const plain = el => {
    if (!el) return '';
    const c = el.cloneNode(true);
    c.querySelectorAll('br').forEach(b => b.replaceWith('\n'));
    return c.textContent.trim();
  };
  // a form answers a message (a quote) or edits one; the line above the main field says which
  const banner = im.querySelector('[data-replying]');
  const unmode = form => {
    const editing = form.dataset.edit;
    delete form.dataset.reply; delete form.dataset.edit;
    if (editing) form.dispatchEvent(new Event('edited'));
    if (banner && form.matches('[data-send]')) banner.hidden = true;
  };
  const mode = (form, kind, msg) => {
    unmode(form);
    if (kind === 'edit') form.dispatchEvent(new Event('editing'));
    form.dataset[kind] = msg.dataset.id;
    const text = plain(msg.querySelector('.text')) || plain(msg.querySelector('.im-file'));
    if (kind === 'edit') form.querySelector('textarea').value = msg.querySelector('.text')?.dataset.body ?? text;
    if (banner && form.matches('[data-send]')) {
      const who = msg.querySelector('.who')?.textContent || '';
      banner.querySelector('[data-replying-text]').textContent = kind === 'edit' ? im.dataset.editHint
        : `${im.dataset.replyHint} ${who}: ${text.slice(0, 80)}`;
      banner.hidden = false;
    }
    form.querySelector('textarea').focus();
  };
  // «Цитировать»: the message with its author and time goes into the field as a quote (its own quotes
  // left out), the cursor after it
  const quote = (form, msg) => {
    const f = form.querySelector('textarea');
    const at = new Date(+msg.dataset.ts).toLocaleString(document.documentElement.lang || undefined,
      { day: '2-digit', month: '2-digit', year: 'numeric', hour: '2-digit', minute: '2-digit' });
    const body = (msg.querySelector('.text')?.dataset.body ?? (plain(msg.querySelector('.text')) || plain(msg.querySelector('.im-file'))))
      .replace(/\[quote\][\s\S]*?\[\/quote\]/gi, '').split('\n').filter(l => !l.startsWith('>')).join('\n').trim();
    const q = `[quote]*${msg.dataset.sender}*, ${at}\n${body}[/quote]\n`;
    f.value = f.value && !f.value.endsWith('\n') ? `${f.value}\n${q}` : f.value + q;
    f.dispatchEvent(new Event('input', { bubbles: true }));
    f.focus();
    f.setSelectionRange(f.value.length, f.value.length);
  };
  // a click on the author's name: «@Name » at the caret (go24 writes mentions so, see mention.js)
  const mention = (form, name) => {
    const f = form?.querySelector('textarea');
    if (!f || !name) return;
    const a = f.selectionStart ?? f.value.length, z = f.selectionEnd ?? a;
    const pre = a && !/\s/.test(f.value[a - 1]) ? ' ' : '';
    f.focus();
    f.setRangeText(`${pre}@${name} `, a, z, 'end');
    f.dispatchEvent(new Event('input', { bubbles: true }));
  };
  // «Поделиться картинкой»: the message drawn as a card (photo, name, time, text, the picture or the file) — a PNG for
  // Telegram, Max, VK…, not a link; the phone's own «Поделиться», on a computer «Скачать» / «Копировать»
  const loadImg = src => new Promise(ok => {
    if (!src) { ok(null); return; }
    const i = new Image();
    i.onload = () => ok(i); i.onerror = () => ok(null);
    i.src = src;
  });
  const wrap = (c, text, width) => {
    const out = [];
    String(text).split('\n').forEach(par => {
      let line = '';
      par.split(/(\s+)/).forEach(w => {
        if (line && c.measureText(line + w).width > width) { out.push(line.trimEnd()); line = w.trimStart(); } else line += w;
        while (c.measureText(line).width > width && line.length > 1) { // a word wider than the card
          let n = line.length - 1;
          while (n > 1 && c.measureText(line.slice(0, n)).width > width) n--;
          out.push(line.slice(0, n)); line = line.slice(n);
        }
      });
      out.push(line);
    });
    return out;
  };
  const picture = async msg => {
    const W = 560, P = 20, A = 40, S = 2, css = getComputedStyle(document.body);
    const font = css.fontFamily || 'sans-serif';
    const name = msg.dataset.sender || '', at = msg.dataset.when || ''; // the time as the chat shows it
    const t = msg.querySelector('.text'), file = msg.querySelector('.im-file');
    const text = plain(t);
    const fname = file?.querySelector('.im-filecard span')?.textContent.trim() || '';
    const [ava, pic] = await Promise.all([loadImg(msg.dataset.photo), loadImg(file?.querySelector('img')?.src)]);
    const c = document.createElement('canvas').getContext('2d');
    if (!c) return null;
    c.font = `15px ${font}`;
    const lines = text ? wrap(c, text, W - 2 * P - A - 12) : [];
    let ph = 0, pw = 0;
    if (pic) { pw = Math.min(W - 2 * P - A - 12, pic.width); ph = Math.min(320, pic.height * pw / pic.width); pw = pic.width * ph / pic.height; }
    const H = P + 22 + lines.length * 21 + (ph ? ph + 8 : 0) + (fname && !pic ? 34 : 0) + 24 + P;
    c.canvas.width = W * S; c.canvas.height = H * S;
    c.scale(S, S);
    c.fillStyle = '#ffffff'; c.fillRect(0, 0, W, H);
    c.fillStyle = '#f1f5f9'; c.beginPath(); c.roundRect?.(P + A + 4, P - 6, W - 2 * P - A - 4, H - 2 * P - 6, 12); c.fill();
    c.save(); c.beginPath(); c.arc(P + A / 2, P + A / 2, A / 2, 0, Math.PI * 2); c.clip();
    if (ava) c.drawImage(ava, P, P, A, A);
    else {
      c.fillStyle = `hsl(${[...name].reduce((h, ch) => h + ch.charCodeAt(0), 0) % 360} 55% 55%)`; c.fillRect(P, P, A, A);
      c.fillStyle = '#fff'; c.font = `600 16px ${font}`; c.textAlign = 'center'; c.textBaseline = 'middle';
      c.fillText(name.split(/\s+/).filter(w => /^\p{L}/u.test(w)).map(w => w[0]).join('').slice(0, 2).toUpperCase(), P + A / 2, P + A / 2 + 1);
    }
    c.restore();
    const x = P + A + 16;
    let y = P + 10;
    c.textAlign = 'left'; c.textBaseline = 'alphabetic';
    c.fillStyle = '#2563eb'; c.font = `600 14px ${font}`; c.fillText(name, x, y + 4);
    c.fillStyle = '#64748b'; c.font = `12px ${font}`; c.fillText(at, x + c.measureText(name).width + 50, y + 4);
    y += 26;
    c.fillStyle = '#0f172a'; c.font = `15px ${font}`;
    lines.forEach(l => { c.fillText(l, x, y); y += 21; });
    if (pic) { c.drawImage(pic, x, y - 8, pw, ph); y += ph + 8; }
    else if (fname) { c.fillStyle = '#334155'; c.font = `14px ${font}`; c.fillText('📎 ' + fname, x, y + 6); y += 34; }
    c.fillStyle = '#94a3b8'; c.font = `11px ${font}`; c.fillText('go24', x, H - P + 2);
    return new Promise(ok => c.canvas.toBlob(ok, 'image/png'));
  };
  const shareDlg = im.querySelector('[data-share-dlg]');
  let shareURL = '';
  const share = async msg => {
    const blob = await picture(msg);
    if (!blob) { alert(failed); return; }
    const file = new File([blob], 'message.png', { type: 'image/png' });
    const native = !!navigator.canShare?.({ files: [file] });
    if (native && matchMedia('(pointer: coarse)').matches) { // a phone: its own «Поделиться» with Telegram, Max…
      try { await navigator.share({ files: [file] }); return; } catch (e) { if (e.name === 'AbortError') return; }
    }
    if (shareURL) URL.revokeObjectURL(shareURL);
    shareURL = URL.createObjectURL(blob);
    shareDlg.querySelector('[data-share-img]').src = shareURL;
    shareDlg.querySelector('[data-share-save]').href = shareURL;
    shareDlg.querySelector('[data-share-note]').textContent = '';
    const nb = shareDlg.querySelector('[data-share-native]');
    nb.hidden = !native;
    nb.onclick = () => navigator.share({ files: [file] }).catch(() => {});
    const cb = shareDlg.querySelector('[data-share-copy]');
    cb.hidden = !(navigator.clipboard?.write && window.ClipboardItem); // only over https, as the system sharing
    cb.onclick = async () => {
      try {
        await navigator.clipboard.write([new ClipboardItem({ 'image/png': blob })]);
        shareDlg.querySelector('[data-share-note]').textContent = shareDlg.dataset.copied;
      } catch { shareDlg.querySelector('[data-share-note]').textContent = failed; }
    };
    shareDlg.showModal ? shareDlg.showModal() : shareDlg.setAttribute('open', '');
  };
  // a message's «…» opens over everything (the chat and thread panes would clip it), under its button or above it
  // where the window has more room, its right edge at the button's and moved left or right to stay whole in the window,
  // scrolled when even that is short; scrolling the chat closes it
  im.addEventListener('toggle', e => {
    const d = e.target;
    if (!d.matches?.('.im-menu') || !d.open) return;
    const pop = d.querySelector('.pop'), sum = d.querySelector('summary');
    if (!pop || !sum) return;
    pop.classList.add('fixed'); pop.style.maxHeight = '';
    const at = sum.getBoundingClientRect(), gap = 8, vw = document.documentElement.clientWidth, vh = window.innerHeight;
    const below = vh - at.bottom - gap, above = at.top - gap;
    let r = pop.getBoundingClientRect();
    const up = r.height > below && above > below;
    if (r.height > (up ? above : below)) pop.style.maxHeight = Math.max(above, below) + 'px';
    r = pop.getBoundingClientRect();
    pop.style.top = (up ? at.top - r.height : at.bottom) + 'px';
    pop.style.left = Math.max(gap, Math.min(at.right - r.width, vw - r.width - gap)) + 'px';
  }, true);
  im.addEventListener('scroll', e => {
    if (e.target.matches?.('.im-msgs')) e.target.querySelectorAll('.im-menu[open]').forEach(d => d.removeAttribute('open'));
  }, true);
  // on a phone a long press on a message opens its «…», as hovering does with a mouse
  let pressTimer = 0;
  im.addEventListener('touchstart', e => {
    const msg = e.target.closest('.msg');
    const menu = msg?.querySelector(':scope > .bubble > .im-menu');
    if (!menu || e.target.closest('a, button, summary, details, audio, video, input, textarea')) return;
    pressTimer = setTimeout(() => { im.querySelectorAll('.im-menu[open]').forEach(d => d.removeAttribute('open')); menu.setAttribute('open', ''); }, 500);
  }, { passive: true });
  ['touchend', 'touchmove', 'touchcancel'].forEach(t => im.addEventListener(t, () => clearTimeout(pressTimer), { passive: true }));
  const toast = (text, href) => {
    let box = document.querySelector('.toasts');
    if (!box) { box = document.createElement('div'); box.className = 'toasts'; document.body.append(box); }
    const t = document.createElement('a');
    t.className = 'toast im-toast'; t.textContent = text; t.href = href;
    box.append(t);
    setTimeout(() => t.remove(), 5000);
  };
  banner?.querySelector('[data-reply-cancel]').addEventListener('click', () => { const f = im.querySelector('[data-send]'); f.reset(); unmode(f); });

  // pins: the bar under the header comes back from the server; the message's menu offers the other way then
  const pins = im.querySelector('[data-pins]');
  const pin = async (id, on) => {
    const { ok, data } = await post('/im/pin', { room, event: id, on: on ? '1' : '0' });
    if (!ok) { alert(data.error || failed); return; }
    pins.innerHTML = data.html;
    [msgs, tmsgs].forEach(box => {
      const b = box && has(box, id)?.querySelector('[data-act="pin"], [data-act="unpin"]');
      if (!b) return;
      b.dataset.act = on ? 'unpin' : 'pin';
      b.lastChild.textContent = ' ' + (on ? im.dataset.unpinLabel : im.dataset.pinLabel);
    });
  };

  // the search of messages: in the open chat, or in all of them (also from the list's search box)
  const spane = im.querySelector('[data-search-pane]');
  const sform = im.querySelector('[data-search]');
  const hits = im.querySelector('[data-hits]');
  const sq = sform?.querySelector('[name="q"]');
  const openSearch = (q, all) => {
    closeThread();
    spane.hidden = false;
    sform.hidden = false;
    const box = sform.querySelector('[name="all"]');
    if (box) box.checked = all;
    spane.querySelector('[data-search-title]').textContent = all || !room ? im.dataset.searchAllTitle : im.dataset.searchHere;
    sq.value = q || '';
    hits.innerHTML = '';
    if (q) sform.requestSubmit(); else sq.focus();
  };
  im.querySelector('[data-search-open]')?.addEventListener('click', () => openSearch('', false));
  im.querySelector('[data-close-search]')?.addEventListener('click', () => { spane.hidden = true; });
  sform?.addEventListener('submit', async e => {
    e.preventDefault();
    const all = !room || sform.querySelector('[name="all"]')?.checked;
    spane.querySelector('[data-search-title]').textContent = all ? im.dataset.searchAllTitle : im.dataset.searchHere;
    const { ok, data } = await get('/im/search?q=' + encodeURIComponent(sq.value) + (all ? '' : '&room=' + encodeURIComponent(room)));
    hits.innerHTML = '';
    if (!ok) { hits.textContent = data.error || failed; return; }
    hits.append(node(data.html));
  });
  sform?.querySelector('[name="all"]')?.addEventListener('change', () => { if (sq.value.trim()) sform.requestSubmit(); });
  hits?.addEventListener('click', e => {
    const h = e.target.closest('[data-hit]');
    if (!h || h.dataset.hitRoom !== room) return; // another chat: the link opens it at the message
    e.preventDefault();
    goto(h.dataset.hit, h.dataset.hitThread);
  });
  const searchAll = im.querySelector('[data-search-all]');
  filter?.addEventListener('input', () => {
    const q = filter.value.trim();
    if (!searchAll) return;
    searchAll.hidden = q.length < 2;
    searchAll.querySelector('span').textContent = `${im.dataset.searchIn} «${q}»`;
  });
  searchAll?.addEventListener('click', () => openSearch(filter.value.trim(), true));
  filter?.addEventListener('keydown', e => { if (e.key === 'Enter' && filter.value.trim().length >= 2) { e.preventDefault(); openSearch(filter.value.trim(), true); } });

  // «Избранное» and «Напомнить»: a mark beside the time of the message
  const mark = (msg, cls, text, title) => {
    let el = msg.querySelector('.' + cls);
    if (!text) { el?.remove(); return; }
    if (!el) { el = document.createElement('span'); el.className = cls; msg.querySelector('.time')?.prepend(el, ' '); }
    el.textContent = text; el.title = title;
  };
  const remindDlg = im.querySelector('[data-remind]');
  remindDlg?.addEventListener('click', async e => {
    const b = e.target.closest('[data-remind-at]');
    if (!b) return;
    const at = b.dataset.remindAt === 'pick' ? remindDlg.querySelector('[data-remind-pick]').value : b.dataset.remindAt;
    if (b.dataset.remindAt === 'pick' && !at) return;
    const { ok, data } = await post('/im/remind', { room, event: remindDlg.dataset.event, at });
    if (!ok) { alert(data.error || failed); return; }
    remindDlg.close ? remindDlg.close() : remindDlg.removeAttribute('open');
    const msg = has(msgs, remindDlg.dataset.event) || has(tmsgs, remindDlg.dataset.event);
    if (msg) mark(msg, 'im-reminder', data.when ? '⏰' : '', data.when);
  });
  // «Избранное»: the person's kept messages in the side pane, each a link to its place
  const settings = im.querySelector('[data-settings]'); // «Настройки» of the messenger
  im.querySelector('[data-settings-open]')?.addEventListener('click', () => settings.showModal());
  settings?.querySelectorAll('[data-settings-close]').forEach(b => b.addEventListener('click', () => settings.close()));

  im.querySelector('[data-saved-open]')?.addEventListener('click', async () => {
    openSearch('', false);
    spane.querySelector('[data-search-title]').textContent = im.dataset.savedTitle;
    sform.hidden = true;
    const { ok, data } = await get('/im/saved');
    hits.innerHTML = '';
    if (!ok) { hits.textContent = data.error || failed; return; }
    hits.append(node(data.html));
  });

  // «Медиа, файлы и ссылки» of the chat in the side pane: tabs of what was sent to it, each a link to its message
  im.querySelector('[data-media-open]')?.addEventListener('click', async () => {
    openSearch('', false);
    spane.querySelector('[data-search-title]').textContent = im.dataset.mediaTitle;
    sform.hidden = true;
    const { ok, data } = await get('/im/media?room=' + encodeURIComponent(room));
    hits.innerHTML = '';
    if (!ok) { hits.textContent = data.error || failed; return; }
    hits.append(node(data.html));
  });
  // «Треды» of the chat in the side pane: those with new replies first; a click opens the thread
  im.querySelector('[data-threads-open]')?.addEventListener('click', async () => {
    openSearch('', false);
    spane.querySelector('[data-search-title]').textContent = im.dataset.threadsTitle;
    sform.hidden = true;
    const { ok, data } = await get('/im/threads?room=' + encodeURIComponent(room));
    hits.innerHTML = '';
    if (!ok) { hits.textContent = data.error || failed; return; }
    hits.append(node(data.html));
  });
  hits?.addEventListener('click', e => {
    const t = e.target.closest('[data-media-tab]');
    if (!t) return;
    e.preventDefault();
    hits.querySelectorAll('[data-media-tab]').forEach(a => a.classList.toggle('on', a === t));
    hits.querySelectorAll('[data-media-list]').forEach(l => { l.hidden = l.dataset.mediaList !== t.dataset.mediaTab; });
  });

  // smileys: ☺ beside the field opens the choice (the recent ones first, kept in this browser); a click puts the
  // smiley where the cursor is
  const emojiPop = im.querySelector('[data-emoji-pop]');
  const EMOJI = {
    smileys: '😀 😃 😄 😁 😆 😅 😂 🤣 😊 😇 🙂 🙃 😉 😌 😍 🥰 😘 😋 😛 😜 🤪 🤨 🧐 🤓 😎 🥳 😏 😒 😞 😔 😟 🙁 😣 😫 😩 🥺 😢 😭 😤 😠 😡 🤯 😳 😱 😨 😰 😓 🤗 🤔 🤭 🤫 😶 😐 😑 😬 🙄 😮 😲 🥱 😴 🤤 😵 🤐 🥴 🤢 🤧 😷 🤒 🤕',
    gestures: '👍 👎 👌 ✌️ 🤞 🤟 🤘 🤙 👈 👉 👆 👇 ☝️ ✋ 🖐️ 👋 🤝 🙏 💪 👏 🙌 👐 ✍️',
    hearts: '❤️ 🧡 💛 💚 💙 💜 🖤 🤍 🤎 💔 ❣️ 💕 💞 💓 💗 💖 💘 💝',
    objects: '🎉 🎊 🎁 🏆 🥇 🔥 ⭐ 🌟 ✨ ⚡ 💡 📌 📎 📅 📈 📉 ✅ ❌ ⚠️ ❓ ❗ 💯 🚀 ☕ 🍕 🍰 🎂',
  };
  const recentKey = 'go24.emoji.recent';
  const recent = () => { try { return JSON.parse(localStorage.getItem(recentKey) || '[]'); } catch { return []; } };
  let emojiField = null, emojiMsg = null; // where a smiley goes: the field, or a reaction to the message
  const drawEmoji = () => {
    emojiPop.innerHTML = '';
    const groups = [['recent', recent()], ...Object.entries(EMOJI).map(([k, v]) => [k, v.split(' ')])];
    groups.forEach(([k, list]) => {
      if (!list.length) return;
      const b = document.createElement('b');
      b.className = 'small muted';
      b.textContent = emojiPop.dataset[k];
      const row = document.createElement('div');
      row.className = 'im-emoji-row';
      list.forEach(e => { const x = document.createElement('button'); x.type = 'button'; x.dataset.emojiPick = e; x.textContent = e; row.append(x); });
      emojiPop.append(b, row);
    });
  };
  im.addEventListener('click', e => {
    if (!emojiPop) return;
    const open = e.target.closest('[data-emoji]');
    const pick = e.target.closest('[data-emoji-pick]');
    const more = e.target.closest('[data-react-more]');
    if (more) { // «Другие реакции» of a message's menu: the whole choice beside the message
      emojiMsg = more.closest('.msg'); emojiField = null;
      more.closest('details')?.removeAttribute('open');
      drawEmoji();
      emojiMsg.querySelector('.bubble').append(emojiPop);
      emojiPop.hidden = false;
    } else if (open) {
      const form = open.closest('form');
      if (!emojiPop.hidden && emojiField === form.querySelector('textarea')) { emojiPop.hidden = true; return; }
      emojiField = form.querySelector('textarea'); emojiMsg = null;
      drawEmoji();
      form.append(emojiPop);
      emojiPop.hidden = false;
    } else if (pick && emojiMsg) {
      const v = pick.dataset.emojiPick;
      emojiPop.hidden = true;
      react(emojiMsg, v);
      emojiMsg = null;
      try { localStorage.setItem(recentKey, JSON.stringify([v, ...recent().filter(x => x !== v)].slice(0, 16))); } catch { /* private mode */ }
    } else if (pick && emojiField) {
      const v = pick.dataset.emojiPick, t = emojiField;
      const at = t.selectionStart ?? t.value.length, end = t.selectionEnd ?? at;
      t.value = t.value.slice(0, at) + v + t.value.slice(end);
      t.selectionStart = t.selectionEnd = at + v.length;
      t.focus();
      t.dispatchEvent(new Event('input', { bubbles: true }));
      try { localStorage.setItem(recentKey, JSON.stringify([v, ...recent().filter(x => x !== v)].slice(0, 16))); } catch { /* private mode */ }
    } else if (!e.target.closest('[data-emoji-pop]')) emojiPop.hidden = true;
  });
  im.addEventListener('keydown', e => { if (e.key === 'Escape' && emojiPop && !emojiPop.hidden) emojiPop.hidden = true; });

  // polls: «Опрос» beside the field opens the form (2–10 answers); a click on an answer votes (with several answers —
  // turns it on or off), «Отменить голос», «Завершить опрос»; the poll comes back drawn anew
  const pollDlg = im.querySelector('[data-poll-dlg]');
  const pollForm = pollDlg?.querySelector('[data-poll-form]');
  const shut = dlg => { dlg.close ? dlg.close() : dlg.removeAttribute('open'); };
  im.addEventListener('click', e => {
    if (!pollDlg || !e.target.closest('[data-poll-new]')) return;
    pollForm.reset();
    pollForm.querySelectorAll('[name="answer"]').forEach((a, i) => { if (i > 1) a.remove(); });
    pollDlg.dataset.where = e.target.closest('[data-send-thread]') ? 'thread' : 'chat';
    pollDlg.showModal ? pollDlg.showModal() : pollDlg.setAttribute('open', '');
  });
  pollDlg?.querySelector('[data-poll-add]').addEventListener('click', e => {
    const box = pollForm.querySelector('[data-poll-answers]');
    const all = box.querySelectorAll('[name="answer"]');
    if (all.length >= +e.currentTarget.dataset.max) return;
    const a = all[0].cloneNode();
    a.value = '';
    box.append(a);
    a.focus();
  });
  pollDlg?.querySelector('[data-poll-cancel]').addEventListener('click', () => shut(pollDlg));
  pollForm?.addEventListener('submit', async e => {
    e.preventDefault();
    const params = new URLSearchParams(new FormData(pollForm));
    params.set('room', room);
    const box = pollDlg.dataset.where === 'thread' ? tmsgs : msgs;
    if (box === tmsgs) params.set('thread', thread);
    const { ok, data } = await post('/im/poll', params);
    if (!ok) { alert(data.error || failed); return; }
    shut(pollDlg);
    if (!has(box, data.id)) box.append(node(data.html));
    bottom(box);
  });
  const pollPost = async (msg, action, answers) => {
    const params = new URLSearchParams({ room, event: msg.dataset.id });
    answers.forEach(a => params.append('answer', a));
    const { ok, data } = await post('/im/poll/' + action, params);
    if (!ok) { alert(data.error || failed); return; }
    msg.replaceWith(node(data.html));
  };
  im.addEventListener('click', e => {
    const msg = e.target.closest('.msg');
    const poll = msg?.querySelector('[data-poll]');
    if (!poll) return;
    const a = e.target.closest('[data-poll-answer]');
    if (a) {
      const mine = [...poll.querySelectorAll('[data-poll-answer][data-mine="true"]')].map(b => b.dataset.pollAnswer);
      const id = a.dataset.pollAnswer;
      if (poll.dataset.multi !== 'true') { if (!mine.includes(id)) pollPost(msg, 'vote', [id]); return; }
      pollPost(msg, 'vote', mine.includes(id) ? mine.filter(x => x !== id) : [...mine, id]);
    } else if (e.target.closest('[data-poll-retract]')) pollPost(msg, 'vote', []);
    else if (e.target.closest('[data-poll-end]') && confirm(im.dataset.pollEndConfirm)) pollPost(msg, 'end', []);
  });

  // the menu of a message: reply, copy, forward, pin, save, remind, edit, delete
  const pick = im.querySelector('[data-forward-pick]');
  im.addEventListener('click', async e => {
    const to = e.target.closest('[data-goto]');
    if (to) {
      e.preventDefault();
      to.closest('details')?.removeAttribute('open');
      goto(to.dataset.goto, to.dataset.gotoThread);
      return;
    }
    const un = e.target.closest('[data-unpin]');
    if (un) { pin(un.dataset.unpin, false); return; }
    const who = e.target.closest('.who[data-mention-name]');
    if (who) { mention(im.querySelector(tmsgs.contains(who) ? '[data-send-thread]' : '[data-send]'), who.textContent); return; }
    const b = e.target.closest('[data-act]');
    if (!b) return;
    const msg = b.closest('.msg');
    b.closest('details')?.removeAttribute('open');
    const form = im.querySelector(tmsgs.contains(msg) ? '[data-send-thread]' : '[data-send]');
    if (!form && ['reply', 'edit', 'quote'].includes(b.dataset.act)) return; // a channel one only reads
    switch (b.dataset.act) {
      case 'reply': mode(form, 'reply', msg); break;
      case 'edit': mode(form, 'edit', msg); break;
      case 'copy': window.go24Copy?.(plain(msg.querySelector('.text'))); break;
      case 'quote': quote(form, msg); break;
      case 'mail': { // «Переслать по почте»: a new letter with the message (and its file) opens in the mail
        const { ok, data } = await post('/im/mail', { room, event: msg.dataset.id });
        if (!ok) { alert(data.error || failed); return; }
        location.assign(data.url);
        break;
      }
      case 'share': share(msg); break;
      case 'drive': { // «Сохранить на Диск»: the toast leads to the folder
        const { ok, data } = await post('/im/file/drive', { room, event: msg.dataset.id });
        if (!ok) { alert(data.error || failed); return; }
        toast(data.text, data.url);
        break;
      }
      case 'link': { // the chat opened at this message; a reply of a thread — in its thread
        const root = msg.dataset.threadRoot, q = new URLSearchParams({ room, at: root || msg.dataset.id });
        if (root) q.set('thread', root);
        window.go24Copy?.(`${location.origin}/im?${q}`);
        break;
      }
      case 'pin': case 'unpin': pin(msg.dataset.id, b.dataset.act === 'pin'); break;
      case 'save': case 'unsave': {
        const on = b.dataset.act === 'save';
        const { ok, data } = await post('/im/save', { room, event: msg.dataset.id, on: on ? '1' : '0' });
        if (!ok) { alert(data.error || failed); return; }
        b.dataset.act = on ? 'unsave' : 'save';
        b.lastChild.textContent = ' ' + (on ? im.dataset.unsaveLabel : im.dataset.saveLabel);
        mark(msg, 'im-saved', on ? '★' : '', im.dataset.savedTitle);
        break;
      }
      case 'remind': remindDlg.dataset.event = msg.dataset.id; remindDlg.showModal ? remindDlg.showModal() : remindDlg.setAttribute('open', ''); break;
      case 'delete': {
        if (!confirm(im.dataset.deleteConfirm)) return;
        const { ok, data } = await post('/im/delete', { room, event: msg.dataset.id });
        if (ok) msg.remove(); else alert(data.error || failed);
        break;
      }
      case 'forward': {
        const list = pick.querySelector('[data-forward-list]');
        list.innerHTML = '';
        im.querySelectorAll('.im-room[data-room-id]').forEach(r => {
          const o = document.createElement('button');
          o.type = 'button'; o.className = 'ghost'; o.dataset.to = r.dataset.roomId; o.textContent = r.dataset.name;
          list.append(o);
        });
        pick.dataset.event = msg.dataset.id;
        pick.querySelector('[data-forward-filter]').value = '';
        pick.showModal ? pick.showModal() : pick.setAttribute('open', '');
        break;
      }
    }
  });
  pick?.querySelector('[data-forward-filter]').addEventListener('input', e => {
    const q = e.target.value.toLowerCase();
    pick.querySelectorAll('[data-to]').forEach(o => { o.hidden = !o.textContent.toLowerCase().includes(q); });
  });
  pick?.addEventListener('click', async e => {
    const o = e.target.closest('[data-to]');
    if (!o) return;
    const { ok, data } = await post('/im/forward', { room, event: pick.dataset.event, to: o.dataset.to });
    if (!ok) { alert(data.error || failed); return; }
    pick.close ? pick.close() : pick.removeAttribute('open');
    if (data.room === room && data.id) get('/im/messages?room=' + encodeURIComponent(room)).then(({ ok, data: d }) => { if (ok) { msgs.innerHTML = ''; msgs.append(node(d.html)); bottom(msgs); } });
  });
  sender(im.querySelector('[data-send]'), 'main');
  sender(im.querySelector('[data-send-thread]'), 'thread');

  // files: the paper clip, a file dropped on the chat, a picture pasted into the field — each goes as its own message
  const upload = async (files, where, extra = {}) => {
    const box = where === 'thread' ? tmsgs : msgs;
    for (const f of files) {
      const wait = node(`<div class="msg mine uploading"><div class="bubble muted small"></div></div>`).firstChild;
      wait.querySelector('.bubble').textContent = `${im.dataset.uploading} ${f.name || ''}`;
      box.append(wait); bottom(box);
      const fd = new FormData();
      fd.append('room', room);
      if (where === 'thread') fd.append('thread', thread);
      Object.entries(extra).forEach(([k, v]) => fd.append(k, v));
      fd.append('file', f, f.name || 'file');
      let res = { ok: false, data: {} };
      try { const r = await fetch('/im/upload', { method: 'POST', headers: { Accept: 'application/json' }, body: fd }); res = { ok: r.ok, data: await r.json() }; } catch { /* below */ }
      wait.remove();
      if (!res.ok) { alert(res.data.error || failed); continue; }
      if (!has(box, res.data.id)) box.append(node(res.data.html));
      bottom(box);
    }
  };

  // voice messages: the microphone records (MediaRecorder), a second click sends it with its length, Esc cancels.
  // A browser gives the microphone to https pages only: on http go24 offers its https address (as calls do).
  const voice = async (btn, where) => {
    if (btn.rec) { btn.rec.stop(); return; }
    if (!window.isSecureContext || !navigator.mediaDevices?.getUserMedia || !window.MediaRecorder) {
      if (im.dataset.secure && confirm(im.dataset.voiceHttps)) location.href = im.dataset.secure + location.pathname + location.search;
      else if (!im.dataset.secure) alert(im.dataset.micDenied);
      return;
    }
    let stream;
    try { stream = await navigator.mediaDevices.getUserMedia({ audio: true }); } catch { alert(im.dataset.micDenied); return; }
    const type = ['audio/ogg;codecs=opus', 'audio/webm;codecs=opus', 'audio/mp4'].find(t => MediaRecorder.isTypeSupported?.(t)) || '';
    const rec = new MediaRecorder(stream, type ? { mimeType: type } : {});
    const chunks = [], start = Date.now(), time = btn.querySelector('[data-voice-time]');
    const tick = setInterval(() => { const s = Math.floor((Date.now() - start) / 1000); time.textContent = ` ${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`; }, 250);
    const esc = e => { if (e.key === 'Escape') { rec.cancelled = true; rec.stop(); } };
    document.addEventListener('keydown', esc);
    rec.ondataavailable = e => { if (e.data.size) chunks.push(e.data); };
    rec.onstop = () => {
      clearInterval(tick); document.removeEventListener('keydown', esc);
      stream.getTracks().forEach(t => t.stop());
      btn.rec = null; btn.classList.remove('recording'); time.textContent = '';
      const ms = Date.now() - start;
      if (rec.cancelled || ms < 500 || !chunks.length) return;
      const mime = (rec.mimeType || type || 'audio/webm').split(';')[0];
      const ext = { 'audio/ogg': 'ogg', 'audio/webm': 'webm', 'audio/mp4': 'm4a' }[mime] || 'webm';
      const stamp = new Date().toISOString().slice(0, 19).replace(/[T:]/g, '-');
      upload([new File(chunks, `voice-${stamp}.${ext}`, { type: mime })], where, { duration: ms });
    };
    btn.rec = rec; btn.classList.add('recording');
    rec.start();
  };
  [['[data-send]', 'main'], ['[data-send-thread]', 'thread']].forEach(([sel, where]) => {
    im.querySelector(sel)?.querySelector('[data-voice]')?.addEventListener('click', e => voice(e.currentTarget, where));
  });
  // A picture, a PDF or a document of the chat opens in the viewer over the chat (and as on «Диск»):
  // ‹ › and ← → go through the files of the chat (or of the thread), with the sender and the time, «К сообщению»,
  // «Скачать»; a document comes as HTML from the server (/im/file/preview). Ctrl / ⌘ + click — a new tab, as before.
  let viewer = null;
  const closeViewer = () => { viewer?.remove(); viewer = null; };
  const vl = k => im.dataset['view' + k[0].toUpperCase() + k.slice(1)] || k;
  const el = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };
  const showFile = async (links, i) => {
    i = (i + links.length) % links.length;
    closeViewer();
    const a = links[i], msg = a.closest('.msg'), kind = a.dataset.imView, name = a.dataset.name;
    const src = a.getAttribute('href');
    viewer = el('div', 'mviewer im-viewer');
    viewer.setAttribute('role', 'dialog');
    viewer.setAttribute('aria-label', name);
    const btn = (text, title, fn, cls) => { const b = el('button', 'ghost small' + (cls ? ' ' + cls : ''), text); b.type = 'button'; b.title = title; b.addEventListener('click', fn); return b; };
    const prev = btn('‹', vl('prev'), () => showFile(links, i - 1), 'mv-prev');
    const next = btn('›', vl('next'), () => showFile(links, i + 1), 'mv-next');
    prev.disabled = next.disabled = links.length < 2;
    const who = el('span', 'small muted', [msg?.dataset.sender, msg?.dataset.when].filter(Boolean).join(' · '));
    const to = btn(vl('goto'), vl('goto'), () => {
      closeViewer();
      if (msg?.closest('[data-thread-msgs]')) msg.scrollIntoView({ block: 'center' });
      else goto(msg.dataset.id);
    });
    const dl = el('a', 'button ghost small', vl('download'));
    dl.href = src + '&dl=1';
    const bar = el('div', 'row mviewer-bar');
    bar.append(prev, el('b', 'mviewer-name', name), el('span', 'small muted', `${i + 1} / ${links.length}`), next, who, el('span', 'grow'), to, dl,
      btn('×', vl('close'), closeViewer, 'mviewer-x'));
    const body = el('div', 'mviewer-body');
    viewer.append(bar, body);
    viewer.addEventListener('click', e => { if (e.target === viewer || e.target === body) closeViewer(); });
    document.body.append(viewer);
    if (kind === 'image') { const img = el('img'); img.src = src; img.alt = name; body.append(img); }
    else if (kind === 'pdf') { const f = el('iframe', 'mviewer-pdf'); f.src = src; f.title = name; body.append(f); }
    else {
      body.append(el('p', 'mviewer-none', vl('loading')));
      const shown = viewer;
      let d = {};
      try { d = await fetch('/im/file/preview?' + new URLSearchParams({ room, event: msg.dataset.id }), { headers: { Accept: 'application/json' } }).then(r => r.json()); } catch { d = {}; }
      if (viewer !== shown) return; // gone on meanwhile
      body.textContent = '';
      if (d.kind === 'html' && d.html) {
        const doc = el('div', 'mviewer-doc');
        doc.innerHTML = d.html; // built and escaped by the server (mail.Preview)
        body.append(doc);
      } else body.append(el('p', 'mviewer-none', vl('none')));
    }
  };
  im.addEventListener('click', e => {
    const a = e.target.closest('a[data-im-view]');
    if (!a || e.ctrlKey || e.metaKey || e.shiftKey || e.button !== 0) return;
    e.preventDefault();
    const links = [...(a.closest('[data-thread-msgs]') || msgs).querySelectorAll('a[data-im-view]')];
    showFile(links, links.indexOf(a));
  });
  document.addEventListener('keydown', e => {
    if (!viewer) return;
    if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); closeViewer(); }
    if (e.key === 'ArrowLeft') viewer.querySelector('.mv-prev')?.click();
    if (e.key === 'ArrowRight') viewer.querySelector('.mv-next')?.click();
  }, true);
  // «Текст» under a voice message: Whisper on the server, once; then it stays under the message
  im.addEventListener('click', async e => {
    const b = e.target.closest('[data-transcribe]');
    if (!b) return;
    const msg = b.closest('.msg'), out = msg.querySelector('[data-transcript]');
    b.disabled = true; out.textContent = im.dataset.voiceWait;
    const { ok, data } = await post('/im/transcribe', { room, event: msg.dataset.id });
    out.textContent = ok ? data.text : (data.error || failed);
    if (ok) b.remove(); else b.disabled = false;
  });
  [['[data-send]', 'main'], ['[data-send-thread]', 'thread']].forEach(([sel, where]) => {
    const form = im.querySelector(sel);
    if (!form) return;
    const input = form.querySelector('[data-attach]');
    input?.addEventListener('change', () => { upload([...input.files], where); input.value = ''; });
    form.querySelector('textarea').addEventListener('paste', e => {
      const files = [...(e.clipboardData?.files || [])];
      if (files.length) { e.preventDefault(); upload(files, where); }
    });
    const zone = where === 'thread' ? pane : im.querySelector('.im-chat');
    zone?.addEventListener('dragover', e => { if ([...e.dataTransfer.types].includes('Files')) { e.preventDefault(); zone.classList.add('dropping'); zone.dataset.dropHint = im.dataset.drop; } });
    zone?.addEventListener('dragleave', e => { if (!zone.contains(e.relatedTarget)) zone.classList.remove('dropping'); });
    zone?.addEventListener('drop', e => {
      if (!e.dataTransfer.files.length) return;
      e.preventDefault(); zone.classList.remove('dropping');
      upload([...e.dataTransfer.files], where);
    });
  });

  // read receipts: one's own messages up to the one read (or, when it is not on the screen, sent before the moment
  // it was read) take ✓✓ and the reader's name
  const readUpTo = (id, at, name) => {
    [msgs, tmsgs].forEach(box => {
      if (!box) return;
      const upto = has(box, id);
      box.querySelectorAll('.msg.mine').forEach(m => {
        const before = upto ? (m === upto || (m.compareDocumentPosition(upto) & Node.DOCUMENT_POSITION_FOLLOWING)) : Number(m.dataset.ts) <= at;
        const tick = m.querySelector('[data-tick]');
        if (!before || !tick) return;
        const names = (tick.dataset.readers || '').split(', ').filter(Boolean);
        if (!names.includes(name)) names.push(name);
        tick.dataset.readers = names.join(', ');
        tick.textContent = '✓✓'; tick.classList.add('read');
        tick.title = im.dataset.readBy.replace('%s', tick.dataset.readers);
      });
    });
  };
  const typingLine = im.querySelector('[data-typing]');
  let typingOff;
  const showTyping = names => {
    if (!typingLine) return;
    clearTimeout(typingOff);
    typingLine.hidden = !names;
    if (!names) return;
    typingLine.textContent = (names.includes(', ') ? im.dataset.typingMany : im.dataset.typingOne).replace('%s', names);
    typingOff = setTimeout(() => { typingLine.hidden = true; }, 7000); // Matrix says when it stops; this is in case it does not
  };

  // live: new messages in the open chat or thread; previews and unread counters in the list
  if (window.EventSource) {
    const es = new EventSource('/im/live');
    es.addEventListener('msg', e => {
      let d; try { d = JSON.parse(e.data); } catch { return; }
      if (d.room === room) {
        if (!d.mine && looking()) { if (!d.thread) markRead(); else if (d.thread === thread) post('/im/read', { room, thread }); }
        if (d.thread) { // a new reply: its root counts it (new — unless the thread is open before one's eyes)
          const seen = d.mine || (d.thread === thread && looking());
          const b = has(msgs, d.thread)?.querySelector('[data-open-thread]');
          setReplies(d.thread, 1, seen ? null : (parseInt(b?.dataset.new, 10) || 0) + 1);
        }
        if (!d.mine && typingLine && !typingLine.hidden && typingLine.textContent.startsWith(d.name)) showTyping('');
        if (d.thread && d.thread === thread && !has(tmsgs, d.id)) { tmsgs.append(node(d.html)); bottom(tmsgs); }
        if (!d.thread && !has(msgs, d.id)) {
          const near = msgs.scrollHeight - msgs.scrollTop - msgs.clientHeight < 80;
          msgs.append(node(d.html)); if (near) bottom(msgs);
          if (d.tome && !near) { atQueue.push(d.id); showAt(); }
        }
      }
      const item = im.querySelector(`.im-room[data-room-id="${CSS.escape(d.room)}"]`);
      if (item) {
        const pv = item.querySelector('[data-preview]');
        if (!pv.hasAttribute('data-draft')) pv.textContent = d.name ? d.name + ': ' + d.preview : d.preview; // a draft stays in sight
        const row = item.closest('[data-row]');
        if (row?.dataset.hidden === '1') { row.dataset.hidden = ''; applyFilter(); } // a new message brings a hidden chat back
        if (row) place(row); else list.prepend(item);
        if (d.thread && !d.mine && row?.dataset.muted !== '1' && !(d.room === room && d.thread === thread)) { // a reply in a thread
          setThreads(d.room, (parseInt(item.querySelector('[data-threads]')?.dataset.threads, 10) || 0) + 1);
        } else if (!d.thread && d.room !== room && !d.mine && row?.dataset.muted !== '1') { // at once; the next «unread» event brings Matrix's own count
          const c = item.querySelector('[data-unread]');
          c.textContent = (parseInt(c.textContent, 10) || 0) + 1; c.hidden = false;
          tabCounts();
          if (d.tome) setAt(d.room, (parseInt(item.querySelector('[data-mention]')?.dataset.mention, 10) || 0) + 1);
        }
      }
    });
    // someone edited or took back a message that is on the screen, read the chat, is typing
    es.addEventListener('change', e => {
      let d; try { d = JSON.parse(e.data); } catch { return; }
      if (d.room !== room) return;
      if (d.change === 'read') { readUpTo(d.id, d.at, d.body); return; }
      if (d.change === 'typing') { showTyping(d.body); return; }
      [msgs, tmsgs].forEach(box => {
        const m = box && has(box, d.id);
        if (!m) return;
        if (d.change === 'delete') { m.remove(); return; }
        if (d.html) { m.replaceWith(node(d.html)); return; } // drawn anew (a call that is over)
        const t = m.querySelector('.text');
        if (!t) return;
        t.dataset.body = d.body;
        if (d.text != null) t.innerHTML = d.text; // formatted on the server, as the message was drawn
        else { t.textContent = ''; d.body.split('\n').forEach((line, i) => { if (i) t.append(document.createElement('br')); t.append(line); }); }
        m.classList.add('edited');
        linkCard(m);
      });
    });
    es.addEventListener('unread', e => {
      let d; try { d = JSON.parse(e.data); } catch { return; }
      Object.entries(d.rooms || {}).forEach(([id, n]) => { setBadge(id, n); setAt(id, d.mentions?.[id] || 0); setThreads(id, d.threads?.[id] || 0); });
    });
  }

  // the buttons of a bot's message (KEYBOARD) and its items of the message's menu (MENU): a command
  // goes to the bot, an action works here; a blocking button takes them all off after one press
  im.addEventListener('click', async e => {
    const b = e.target.closest('[data-kbd]');
    if (!b || b.disabled) return;
    const kbd = b.closest('.im-kbd'), menu = b.closest('details.im-menu'), msg = b.closest('.msg'), v = b.dataset.value || '';
    const field = (b.closest('[data-thread-pane]') || im).querySelector('form.chatform textarea');
    const lock = on => kbd?.querySelectorAll('button').forEach(x => { x.disabled = on; });
    if (menu) menu.open = false;
    if (b.dataset.block) lock(true);
    switch (b.dataset.kbd) {
      case 'command': {
        const { ok, data } = await post('/im/button', { room, event: msg.dataset.id, command: b.dataset.command, params: b.dataset.params || '', ...(menu ? { menu: '1' } : {}) });
        if (!ok) { lock(false); alert(data.error || failed); }
        break;
      }
      case 'PUT': if (field) { field.value = v; field.focus(); field.dispatchEvent(new Event('input', { bubbles: true })); } break;
      case 'SEND': if (field) { field.value = v; field.form.requestSubmit(); } break;
      case 'COPY': window.go24Copy?.(v); break;
      case 'CALL': location.href = 'tel:' + v.replace(/[^\d+]/g, ''); break;
      case 'DIALOG': location.href = '/im/direct?user=' + encodeURIComponent(v); break;
    }
  });

  // «/» at the start of the field: the commands of the chat's bots; a click, Enter or Tab puts
  // «/command » into the field
  let cmds = null, cbox = null, cfield = null, citems = [], csel = 0;
  const cclose = () => { cbox?.remove(); cbox = null; cfield = null; };
  const cpick = c => {
    const f = cfield;
    f.value = '/' + c.command + ' ';
    cclose();
    f.focus();
    f.setSelectionRange(f.value.length, f.value.length);
  };
  const cshow = () => {
    if (!citems.length) { cclose(); return; }
    if (!cbox) {
      cbox = document.createElement('div');
      cbox.className = 'mention-pop im-cmds';
      cbox.addEventListener('mousedown', e => {
        const el = e.target.closest('[data-i]');
        if (el) { e.preventDefault(); cpick(citems[Number(el.dataset.i)]); }
      });
      document.body.append(cbox);
      window.go24Pop?.(cbox, cclose, cfield);
    }
    cbox.innerHTML = '';
    citems.forEach((c, i) => {
      const el = document.createElement('div');
      el.className = 'mention-item' + (i === csel ? ' on' : '');
      el.dataset.i = i;
      const b = document.createElement('b');
      b.textContent = '/' + c.command + (c.params ? ' ' + c.params : '');
      const d = document.createElement('span');
      d.className = 'muted small';
      d.textContent = [c.title, c.bot].filter(Boolean).join(' · ');
      el.append(b, ' ', d);
      cbox.append(el);
    });
    const r = cfield.getBoundingClientRect();
    const h = Math.min(citems.length, 8) * 34 + 8;
    cbox.style.left = Math.max(8, Math.min(r.left, innerWidth - 368)) + 'px';
    cbox.style.top = Math.max(8, r.top - h - 4) + 'px';
  };
  im.addEventListener('input', async e => {
    const f = e.target;
    if (!f.matches?.('form.chatform textarea')) return;
    const m = f.value.match(/^\/([\p{L}\p{N}_.-]{0,50})$/u);
    if (!m || !room) { if (cfield === f) cclose(); return; }
    if (!cmds) {
      const { ok, data } = await get('/im/commands?room=' + encodeURIComponent(room));
      cmds = ok && Array.isArray(data) ? data : [];
    }
    if (f.value !== m[0]) return; // typed on meanwhile
    const q = m[1].toLowerCase();
    cfield = f;
    citems = cmds.filter(c => c.command.toLowerCase().startsWith(q)).slice(0, 8);
    csel = 0;
    cshow();
  });
  // capture: before the chat's own Enter-to-send
  document.addEventListener('keydown', e => {
    if (!cbox || e.target !== cfield) return;
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      csel = (csel + (e.key === 'ArrowDown' ? 1 : citems.length - 1)) % citems.length;
      cshow();
    } else if (e.key === 'Enter' || e.key === 'Tab') {
      e.preventDefault();
      e.stopImmediatePropagation();
      cpick(citems[csel]);
    } else if (e.key === 'Escape') {
      e.stopImmediatePropagation();
      cclose();
    }
  }, true);
  document.addEventListener('focusout', e => { if (e.target === cfield) setTimeout(cclose, 150); });
})();
