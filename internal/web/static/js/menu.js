// go24 left menu: sections open and close by a click on their name or icon (<details>). The section of the
// current page is always open; the others keep the state the viewer left them in (stored in this browser only).
(() => {
  const key = 'go24.menu.closed';
  let closed = [];
  try { closed = JSON.parse(localStorage.getItem(key) || '[]'); } catch { closed = []; }
  document.querySelectorAll('[data-menu] details.msec').forEach(d => {
    const sec = d.dataset.sec;
    const current = d.open; // the server opened the section of this page
    if (!current) d.open = !closed.includes(sec) && d.dataset.sec !== 'settings';
    d.addEventListener('toggle', () => {
      closed = closed.filter(s => s !== sec);
      if (!d.open) closed.push(sec);
      try { localStorage.setItem(key, JSON.stringify(closed)); } catch { /* private mode: not remembered */ }
    });
  });
})();

// Person pop-up: a click on [data-user] (the name in the top bar, people in lists) shows the person's card.
// Ctrl/Cmd-click still opens the full profile page.
(() => {
  let pop = null;
  const close = () => { pop?.remove(); pop = null; };
  document.addEventListener('click', async e => {
    const el = e.target.closest('[data-user]');
    if (!el) {
      if (pop && !pop.contains(e.target)) close();
      return;
    }
    if (e.metaKey || e.ctrlKey || e.shiftKey) return;
    e.preventDefault();
    if (pop && pop.dataset.for === el.dataset.user) { close(); return; }
    close();
    let html = '';
    try {
      const res = await fetch('/u/' + encodeURIComponent(el.dataset.user) + '/card');
      if (!res.ok) { if (el.href) location.href = el.href; return; }
      html = await res.text();
    } catch { return; }
    pop = document.createElement('div');
    pop.className = 'pcard-pop';
    pop.dataset.for = el.dataset.user;
    pop.innerHTML = html;
    document.body.append(pop);
    window.go24Presence?.(); // «В сети» / «Был(а) в сети …» of the person
    window.go24Pop?.(pop, close, el);
    const r = el.getBoundingClientRect();
    const w = pop.offsetWidth;
    pop.style.top = (window.scrollY + r.bottom + 6) + 'px';
    pop.style.left = Math.max(8, Math.min(window.scrollX + r.left, window.scrollX + document.documentElement.clientWidth - w - 8)) + 'px';
  });
  document.addEventListener('keydown', e => { if (e.key === 'Escape') close(); });
})();


// New letters, as Outlook's: the «Почта» counter of the menu (and of the mail tab's title), a toast with the
// letter's quick actions (open, read, delete, flag) in the tab in view, a desktop notification and a sound from the
// tab that holds the live stream (lead) when go24 is not in view; the mail page refreshes its list (go24:mail).
// d: {unread, letters: [{id, folder, from, subject, preview}], sound, desktop} — the «mail» event of the stream.
function go24Mail(d, lead) {
  const labels = document.querySelector('template[data-mail-labels]')?.content;
  const l = k => labels?.querySelector(`[data-l="${k}"]`)?.textContent || k;
  if (typeof d.unread === 'number') {
    document.querySelectorAll('[data-counter="mail"]').forEach(c => { c.textContent = d.unread > 99 ? '99+' : d.unread; c.hidden = !d.unread; });
  }
  window.dispatchEvent(new CustomEvent('go24:mail', { detail: d }));
  const letters = d.letters || [];
  if (!letters.length || document.body.hasAttribute('data-dnd')) return; // «Не беспокоить»: counters only
  const seen = document.visibilityState === 'visible';
  if (lead && d.sound) {
    try { // a short two-tone chime, no file needed
      const ctx = new (window.AudioContext || window.webkitAudioContext)();
      [[880, 0], [1320, 0.12]].forEach(([hz, at]) => {
        const o = ctx.createOscillator(), g = ctx.createGain();
        o.frequency.value = hz;
        g.gain.setValueAtTime(0.0001, ctx.currentTime + at);
        g.gain.exponentialRampToValueAtTime(0.15, ctx.currentTime + at + 0.02);
        g.gain.exponentialRampToValueAtTime(0.0001, ctx.currentTime + at + 0.3);
        o.connect(g).connect(ctx.destination);
        o.start(ctx.currentTime + at);
        o.stop(ctx.currentTime + at + 0.32);
      });
      setTimeout(() => ctx.close?.(), 1000);
    } catch { /* no sound here */ }
  }
  if (!d.desktop) return;
  const link = m => `/mail?folder=${encodeURIComponent(m.folder)}&letter=${encodeURIComponent(m.id)}`;
  if (!seen) {
    if (lead && window.Notification?.permission === 'granted') {
      for (const m of letters) {
        try {
          const n = new Notification(m.from || l('new'), { body: (m.subject || l('no_subject')) + (m.preview ? '\n' + m.preview : ''), tag: 'go24-mail-' + m.id });
          n.onclick = () => { window.focus(); location.href = link(m); n.close(); };
        } catch { /* the browser would not show it */ }
      }
    }
    return;
  }
  let box = document.querySelector('.toasts');
  if (!box) { box = document.createElement('div'); box.className = 'toasts'; document.body.append(box); }
  for (const m of letters) {
    const t = document.createElement('div');
    t.className = 'toast mtoast';
    t.dataset.mailToast = m.id;
    const head = document.createElement('a');
    head.className = 'mtoast-head';
    head.href = link(m);
    const who = document.createElement('b');
    who.textContent = m.from || l('new');
    const subj = document.createElement('span');
    subj.className = 'mtoast-subj';
    subj.textContent = m.subject || l('no_subject');
    const prev = document.createElement('span');
    prev.className = 'small muted mtoast-prev';
    prev.textContent = m.preview || '';
    head.append(who, subj, prev);
    const acts = document.createElement('div');
    acts.className = 'row mtoast-acts';
    const act = (what, text) => {
      const b = document.createElement('button');
      b.type = 'button';
      b.className = 'ghost small';
      b.textContent = text;
      b.addEventListener('click', async () => {
        const body = new URLSearchParams({ letter: m.id, do: what, back: '/mail' });
        try {
          const res = await fetch('/mail/act', { method: 'POST', body, headers: { Accept: 'application/json', 'X-Go24-Form': '1' } });
          if (res.ok) {
            t.remove();
            const u = await fetch('/mail/unread', { headers: { Accept: 'application/json' } }).then(r => r.json()).catch(() => null);
            if (u) go24Mail({ unread: u.unread });
          }
        } catch { /* stays */ }
      });
      acts.append(b);
    };
    const open = document.createElement('a');
    open.className = 'button ghost small';
    open.href = link(m);
    open.textContent = l('open');
    acts.append(open);
    act('read', l('read'));
    act('flag:none', l('flag'));
    act('delete', l('delete'));
    const x = document.createElement('button');
    x.type = 'button';
    x.className = 'ghost small mtoast-x';
    x.textContent = '×';
    x.title = l('close');
    x.addEventListener('click', () => t.remove());
    t.append(x, head, acts);
    box.append(t);
    while (box.children.length > 3) box.firstChild.remove();
    setTimeout(() => t.remove(), 12000);
  }
}

// Bell («Уведомления»): the pop-up with the newest notifications, the unread counter and live toasts.
// One browser tab holds the live stream and shares what it gets with the other tabs (BroadcastChannel),
// so many open tabs do not use up the browser's connections to the server.
(() => {
  const btn = document.querySelector('[data-bell]');
  if (!btn) return;
  const pop = document.querySelector('[data-bellpop]');
  const counters = () => document.querySelectorAll('[data-counter="notify"]');
  const setUnread = n => counters().forEach(c => { c.textContent = n > 99 ? '99+' : n; c.hidden = !n; });
  const bc = 'BroadcastChannel' in window ? new BroadcastChannel('go24-notify') : null;

  const load = async () => {
    try {
      const res = await fetch('/notifications/pop', { headers: { Accept: 'application/json' } });
      if (!res.ok) return;
      const d = await res.json();
      pop.innerHTML = d.html;
      setUnread(d.unread);
    } catch { /* offline: the pop-up keeps what it had */ }
  };
  btn.addEventListener('click', e => {
    e.stopPropagation();
    pop.hidden = !pop.hidden;
    if (!pop.hidden) { window.go24Pop?.(pop, () => { pop.hidden = true; }, btn); load(); }
  });
  document.addEventListener('click', e => {
    if (!pop.hidden && !pop.contains(e.target)) pop.hidden = true;
  });
  document.addEventListener('keydown', e => { if (e.key === 'Escape') pop.hidden = true; });
  pop.addEventListener('click', async e => {
    if (!e.target.closest('[data-bell-readall]')) return;
    try {
      const res = await fetch('/notifications/read', { method: 'POST', headers: { Accept: 'application/json' } });
      if (!res.ok) return;
      const d = await res.json();
      setUnread(d.unread);
      pop.querySelectorAll('.nitem.unread').forEach(i => i.classList.remove('unread'));
      bc?.postMessage({ unread: d.unread });
    } catch { /* stays unread */ }
  });

  let toasts = null;
  const toast = html => {
    if (document.visibilityState !== 'visible') return;
    if (!toasts) { toasts = document.createElement('div'); toasts.className = 'toasts'; document.body.append(toasts); }
    const t = document.createElement('div');
    t.className = 'toast';
    t.innerHTML = html;
    toasts.append(t);
    while (toasts.children.length > 3) toasts.firstChild.remove();
    setTimeout(() => t.remove(), 7000);
  };
  // the task whose card is open in this tab: its news are seen already (task.js reads them)
  const openTask = document.querySelector('[data-task]:not(.nitem)')?.dataset.task;
  const apply = d => {
    if (d.mail) { go24Mail(d.mail); return; }
    if (d.call) { dispatchEvent(new CustomEvent('go24:call', { detail: d.call })); return; } // ring.js, call.js
    if (typeof d.unread === 'number') setUnread(d.unread);
    if (typeof d.invites === 'number') { // meeting invitations not answered yet: the «Календарь» counter
      document.querySelectorAll('[data-counter="calendar"]').forEach(c => { c.textContent = d.invites > 99 ? '99+' : d.invites; c.hidden = !d.invites; });
    }
    if (!d.html) return;
    // a new letter pops up as the mail's own toast (go24Mail), with its actions
    if ((!openTask || !d.html.includes(`data-task="${openTask}"`)) && !d.html.includes('data-kind="mail"') && !document.body.hasAttribute('data-dnd')) toast(d.html);
    const list = pop.querySelector('.nlist');
    if (!pop.hidden && list && !list.querySelector(`[data-nid="${d.id}"]`)) {
      list.querySelector('.nempty')?.remove();
      list.insertAdjacentHTML('afterbegin', d.html);
    }
  };
  if (bc) bc.onmessage = e => apply(e.data);

  // leader election over localStorage: the tab with a fresh heartbeat holds the stream
  const me = Math.random().toString(36).slice(2);
  const key = 'go24.notify.leader';
  let es = null;
  const open = () => {
    if (es || !window.EventSource) return;
    es = new EventSource('/notifications/live');
    es.addEventListener('notify', ev => {
      let d;
      try { d = JSON.parse(ev.data); } catch { return; }
      apply(d);
      bc?.postMessage(d);
    });
    es.addEventListener('mail', ev => {
      let d;
      try { d = JSON.parse(ev.data); } catch { return; }
      go24Mail(d, true);
      bc?.postMessage({ mail: d });
    });
    es.addEventListener('call', ev => { // a call rings, or an answer to one's own call: every tab hears it
      let d;
      try { d = JSON.parse(ev.data); } catch { return; }
      apply({ call: d });
      bc?.postMessage({ call: d });
    });
  };
  const closeStream = () => { es?.close(); es = null; prev = null; };
  const tick = () => {
    let cur = null;
    try { cur = JSON.parse(localStorage.getItem(key) || 'null'); } catch { cur = null; }
    if (!cur || cur.id === me || Date.now() - cur.at > 6000) {
      try { localStorage.setItem(key, JSON.stringify({ id: me, at: Date.now() })); } catch { open(); return; } // no storage: every tab streams
      open();
    } else if (es) {
      closeStream(); // another tab took over
    }
  };
  if (!bc) { open(); return; } // no way to share: this tab streams itself
  tick();
  setInterval(tick, 2000);
  addEventListener('pagehide', () => {
    closeStream();
    try { if (JSON.parse(localStorage.getItem(key) || 'null')?.id === me) localStorage.removeItem(key); } catch { /* ignore */ }
  });
})();

// «Мессенджер» counter in the menu: unread messages of all chats, live. Like the bell, one tab of the browser
// holds the stream (/im/unread) and shares the counts with the others; a new tab asks for the last ones.
(() => {
  const item = document.querySelector('[data-im-unread]');
  if (!item) return;
  const counter = item.querySelector('[data-counter="im"]');
  const openRoom = document.querySelector('[data-im]')?.dataset.room || '';
  let last = null;
  const show = d => {
    last = d;
    let total = d.total || 0;
    // the chat open in this tab and in view is read as it comes
    if (openRoom && document.visibilityState === 'visible') total -= (d.rooms || {})[openRoom] || 0;
    counter.textContent = total > 99 ? '99+' : total;
    counter.hidden = total <= 0;
    collabs(d);
  };
  // the collabs' chats: the counter of a chat on its collab's card and their sum added to «Коллабы» in the menu
  const unreadOf = (d, room) => room === openRoom && document.visibilityState === 'visible' ? 0
    : ((d.rooms || {})[room] || 0) + ((d.threads || {})[room] || 0);
  const collabs = d => {
    document.querySelectorAll('[data-room-unread]').forEach(c => {
      const n = unreadOf(d, c.dataset.roomUnread);
      c.textContent = n > 99 ? '99+' : n;
      c.hidden = n <= 0;
    });
    document.querySelectorAll('[data-counter="collabs"]').forEach(c => {
      const n = (+c.dataset.base || 0) + (c.dataset.rooms || '').split(' ').filter(Boolean).reduce((s, r) => s + unreadOf(d, r), 0);
      c.textContent = n > 99 ? '99+' : n;
      c.hidden = n <= 0;
    });
  };
  document.addEventListener('visibilitychange', () => { if (last) show(last); });
  const bc = 'BroadcastChannel' in window ? new BroadcastChannel('go24-im') : null;
  if (bc) bc.onmessage = e => { if (e.data.ask) { if (last && es) bc.postMessage(last); } else show(e.data); };

  const me = Math.random().toString(36).slice(2);
  const key = 'go24.im.leader';
  let es = null, prev = null, ctx = null;
  // a short sound when a chat gets a new message («Настройки» of the messenger): only this tab holds the stream, so it
  // sounds once; not in «Не беспокоить», not for the chat open and in view
  const ding = d => {
    const was = prev;
    prev = d;
    if (!was || !d.sound || document.body.hasAttribute('data-dnd')) return;
    const grew = Object.entries(d.rooms || {}).some(([id, n]) => n > ((was.rooms || {})[id] || 0) &&
      !(id === openRoom && document.visibilityState === 'visible'));
    if (!grew) return;
    try {
      ctx = ctx || new (window.AudioContext || window.webkitAudioContext)();
      const o = ctx.createOscillator(), g = ctx.createGain();
      o.frequency.setValueAtTime(990, ctx.currentTime);
      o.frequency.exponentialRampToValueAtTime(660, ctx.currentTime + 0.15);
      g.gain.setValueAtTime(0.0001, ctx.currentTime);
      g.gain.exponentialRampToValueAtTime(0.15, ctx.currentTime + 0.02);
      g.gain.exponentialRampToValueAtTime(0.0001, ctx.currentTime + 0.25);
      o.connect(g).connect(ctx.destination);
      o.start(); o.stop(ctx.currentTime + 0.3);
    } catch { /* no sound */ }
  };
  const open = () => {
    if (es || !window.EventSource) return;
    es = new EventSource('/im/unread');
    es.addEventListener('unread', ev => {
      let d;
      try { d = JSON.parse(ev.data); } catch { return; }
      ding(d);
      show(d);
      bc?.postMessage(d);
    });
  };
  const closeStream = () => { es?.close(); es = null; };
  const tick = () => {
    let cur = null;
    try { cur = JSON.parse(localStorage.getItem(key) || 'null'); } catch { cur = null; }
    if (!cur || cur.id === me || Date.now() - cur.at > 6000) {
      try { localStorage.setItem(key, JSON.stringify({ id: me, at: Date.now() })); } catch { open(); return; }
      open();
    } else if (es) {
      closeStream();
    }
  };
  if (!bc) { open(); return; }
  bc.postMessage({ ask: 1 });
  tick();
  setInterval(tick, 2000);
  addEventListener('pagehide', () => {
    closeStream();
    try { if (JSON.parse(localStorage.getItem(key) || 'null')?.id === me) localStorage.removeItem(key); } catch { /* ignore */ }
  });
})();

// Presence: [data-presence="<user id>"] gets the class pr-online | pr-away | pr-dnd | pr-offline
// (the dot at an avatar), with data-presence-text — also the words («В сети», «Был(а) в сети 5 мин назад»).
// Asked once a minute for everyone on the page; new elements (a chat added to the list) are picked up next time.
(() => {
  const states = ['pr-online', 'pr-away', 'pr-dnd', 'pr-offline'];
  const ask = async () => {
    const els = [...document.querySelectorAll('[data-presence]')];
    const ids = [...new Set(els.map(el => el.dataset.presence).filter(Boolean))];
    if (!ids.length) return;
    try {
      const res = await fetch('/presence?' + ids.map(id => 'user=' + encodeURIComponent(id)).join('&'));
      if (!res.ok) return;
      const all = await res.json();
      for (const el of els) {
        const p = all[el.dataset.presence];
        if (!p) continue;
        el.classList.remove(...states);
        el.classList.add('pr-' + p.state);
        el.title = p.text;
        if ('presenceText' in el.dataset) el.textContent = p.text;
      }
    } catch { /* offline: the dots stay as they were */ }
  };
  window.go24Presence = ask;
  ask();
  setInterval(ask, 60000);
})();

// «Настроить меню»: each employee sets up the left menu for themselves — drags the items into their
// order (any time), hides them into «Скрытые» and back, makes one the first item (the page opened after signing in),
// adds links of their own (also from any page by the star at its title), resets it all; an administrator makes their
// menu the company's default («Применить для всех»). The menu is kept on the server (/menu), the page only sends it.
(() => {
  const nav = document.querySelector('.side nav[data-menu]');
  const tools = document.querySelector('[data-menu-tools]');
  const hid = nav?.querySelector('[data-menu-hidden]');
  if (!nav || !tools || !hid) return; // a guest's menu is not set up
  const side = nav.closest('.side');
  const hidSub = hid.querySelector('.msub');
  const labels = nav.querySelector('template[data-menu-labels]')?.content;
  const l = k => labels?.querySelector(`[data-l="${k}"]`)?.textContent || k;
  const entries = box => [...box.children].filter(e => e.dataset.mid);
  const isCustom = e => 'url' in e.dataset;

  const state = () => {
    const shown = entries(nav).map(e => e.dataset.mid), hidden = entries(hidSub).map(e => e.dataset.mid);
    const custom = [...entries(nav), ...entries(hidSub)].filter(isCustom)
      .map(e => ({ id: e.dataset.mid, title: e.dataset.title, url: e.dataset.url, new_tab: 'newtab' in e.dataset }));
    return { order: [...shown, ...hidden], hidden, custom };
  };
  const post = async (url, body) => {
    try {
      const res = await fetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        body: body ? JSON.stringify(body) : undefined });
      if (res.ok) return true;
      let msg = res.statusText;
      try { msg = (await res.json()).error || msg; } catch { /* not JSON */ }
      alert(msg);
    } catch (err) { alert(err.message); }
    return false;
  };
  const save = (st = state()) => post('/menu', st);

  // «Скрытые»: shown while it has items, and while the menu is set up (a place to drop an item into)
  const editing = () => side.classList.contains('menu-editing');
  const fixHidden = () => {
    const n = entries(hidSub).length;
    hid.hidden = !n && !editing();
    const cnt = hid.querySelector('summary .n');
    if (cnt) cnt.textContent = n;
  };

  // the mode «Настроить меню»: a «⋯» at each item and the tools below; it stays on over a reload of the page
  const EDIT = 'go24.menu.editing';
  const more = e => {
    if (e.querySelector(':scope > .mi-more, :scope > summary > .mi-more')) return;
    const b = document.createElement('button');
    b.type = 'button';
    b.className = 'mi-more';
    b.title = l('more');
    b.textContent = '⋯';
    (e.querySelector(':scope > summary') || e).append(b);
  };
  const setEditing = on => {
    side.classList.toggle('menu-editing', on);
    tools.hidden = !on; // «Настроить меню» itself is a link of «Настройки»
    if (on) [...entries(nav), ...entries(hidSub)].forEach(more);
    else side.querySelectorAll('.mi-more').forEach(b => b.remove());
    if (on) hid.open = true;
    fixHidden();
    try { on ? sessionStorage.setItem(EDIT, '1') : sessionStorage.removeItem(EDIT); } catch { /* private mode */ }
  };
  try { if (sessionStorage.getItem(EDIT)) setEditing(true); } catch { /* private mode */ }

  // the pop-up of an item: «⋯» in the mode, or a right click any time
  let pop = null;
  const closePop = () => { pop?.remove(); pop = null; };
  const openPop = (e, x, y) => {
    closePop();
    pop = document.createElement('div');
    pop.className = 'menu-pop';
    const hidden = hidSub.contains(e);
    const act = (key, fn) => {
      const b = document.createElement('button');
      b.type = 'button';
      b.textContent = l(key);
      b.addEventListener('click', async () => { closePop(); await fn(); });
      pop.append(b);
    };
    if (hidden || entries(nav)[0] !== e) act('first', async () => { nav.prepend(e); fixHidden(); await save(); });
    if (hidden) act('show', async () => { nav.insertBefore(e, hid); fixHidden(); await save(); });
    else act('hide', async () => { hidSub.append(e); fixHidden(); await save(); });
    if (isCustom(e)) {
      act('item_edit', () => dialog(e));
      act('item_delete', async () => {
        if (!confirm(l('delete_confirm').replace('%s', e.dataset.title))) return;
        e.remove();
        fixHidden();
        await save();
      });
    }
    document.body.append(pop);
    window.go24Pop?.(pop, closePop, e);
    const w = pop.offsetWidth, h = pop.offsetHeight;
    pop.style.left = Math.max(4, Math.min(x, innerWidth - w - 4)) + 'px';
    pop.style.top = Math.max(4, Math.min(y, innerHeight - h - 4)) + 'px';
  };
  side.addEventListener('click', ev => {
    const b = ev.target.closest('.mi-more');
    if (!b) return;
    ev.preventDefault(); // not a toggle of the section, not a link
    ev.stopPropagation();
    const r = b.getBoundingClientRect();
    openPop(b.closest('[data-mid]'), r.right, r.bottom);
  });
  side.addEventListener('contextmenu', ev => {
    const e = ev.target.closest('[data-mid]');
    if (!e || e.querySelector(':scope > .msub')?.contains(ev.target)) return; // the links of a section: the browser's menu
    ev.preventDefault();
    openPop(e, ev.clientX, ev.clientY);
  });
  document.addEventListener('click', ev => { if (pop && !pop.contains(ev.target)) closePop(); });
  document.addEventListener('keydown', ev => { if (ev.key === 'Escape') closePop(); });
  addEventListener('scroll', closePop, true);

  // an own link: added (e — none) or changed; the page is drawn again with it
  const dialog = e => {
    const d = document.createElement('dialog');
    d.className = 'menu-dlg card';
    const f = document.createElement('form');
    f.method = 'dialog';
    const field = (key, input) => {
      const lab = document.createElement('label');
      lab.textContent = l(key);
      lab.append(input);
      f.append(lab);
      return input;
    };
    const title = field('title', Object.assign(document.createElement('input'), { required: true, maxLength: 80, value: e?.dataset.title || '' }));
    const url = field('url', Object.assign(document.createElement('input'), { required: true, value: e?.dataset.url || '', placeholder: '/crm/deals' }));
    const hint = document.createElement('div');
    hint.className = 'small muted';
    hint.textContent = l('url_hint');
    f.append(hint);
    const lab = document.createElement('label');
    lab.className = 'check';
    const tab = Object.assign(document.createElement('input'), { type: 'checkbox', checked: e ? 'newtab' in e.dataset : false });
    lab.append(tab, ' ' + l('newtab'));
    f.append(lab);
    const row = document.createElement('div');
    row.className = 'row';
    const ok = Object.assign(document.createElement('button'), { type: 'submit', className: 'primary', textContent: l('save') });
    const no = Object.assign(document.createElement('button'), { type: 'button', className: 'ghost', textContent: l('cancel') });
    no.addEventListener('click', () => d.close());
    row.append(ok, no);
    f.append(row);
    d.append(f);
    f.addEventListener('submit', async ev => {
      ev.preventDefault();
      const st = state();
      const link = { id: e?.dataset.mid || '', title: title.value.trim(), url: url.value.trim(), new_tab: tab.checked };
      if (e) st.custom = st.custom.map(c => c.id === link.id ? link : c);
      else st.custom.push(link); // the server gives it an id and puts it last
      if (await save(st)) location.reload();
    });
    d.addEventListener('close', () => d.remove());
    document.body.append(d);
    d.showModal();
    title.focus();
  };

  side.addEventListener('click', async ev => {
    const b = ev.target.closest('[data-menu-act]');
    if (!b) return;
    ev.preventDefault(); // «Настроить меню» is a link (to «Настройки» without scripts)
    switch (b.dataset.menuAct) {
      case 'edit':
        setEditing(true);
        tools.scrollIntoView({ block: 'nearest' });
        break;
      case 'done': setEditing(false); break;
      case 'add': dialog(null); break;
      case 'reset':
        if (confirm(l('reset_confirm')) && await post('/menu/reset')) location.reload();
        break;
      case 'apply-all':
        if (confirm(l('apply_all_confirm')) && await post('/menu?all=1', state())) location.reload();
        break;
      case 'reset-all':
        if (confirm(l('reset_all_confirm')) && await post('/menu/reset?all=1')) location.reload();
        break;
    }
  });

  // dragging: an item of the menu (not a link inside a section) goes where it is dropped, «Скрытые» hide it
  let drag = null, before = '';
  side.addEventListener('dragstart', ev => {
    const e = ev.target.closest?.('[data-mid]');
    if (!e || !side.contains(e) || e.querySelector(':scope > .msub')?.contains(ev.target)) return;
    drag = e;
    before = JSON.stringify(state());
    ev.dataTransfer.effectAllowed = 'move';
    ev.dataTransfer.setData('text/plain', e.dataset.mid);
    e.classList.add('dragging');
    if (!hid.open || hid.hidden) { hid.hidden = false; }
  });
  side.addEventListener('dragover', ev => {
    if (!drag) return;
    let box = null;
    if (ev.target.closest('[data-menu-hidden] > summary')) box = hidSub;
    else if (hidSub.contains(ev.target)) box = hidSub;
    else if (nav.contains(ev.target)) box = nav;
    if (!box) return;
    ev.preventDefault();
    ev.dataTransfer.dropEffect = 'move';
    if (box === hidSub && !hid.open) hid.open = true;
    const next = entries(box).find(e => e !== drag && !drag.contains(e) && ev.clientY < e.getBoundingClientRect().top + e.getBoundingClientRect().height / 2);
    if (next) { if (drag.nextElementSibling !== next) box.insertBefore(drag, next); }
    else if (box === nav) { if (drag.nextElementSibling !== hid) nav.insertBefore(drag, hid); }
    else if (hidSub.lastElementChild !== drag) hidSub.append(drag);
  });
  side.addEventListener('drop', ev => { if (drag) ev.preventDefault(); });
  side.addEventListener('dragend', () => {
    if (!drag) return;
    drag.classList.remove('dragging');
    drag = null;
    fixHidden();
    if (JSON.stringify(state()) !== before) save();
  });

  // the star at the page's title: «Добавить в левое меню» (an own link to this page), or out of it again
  const h1 = document.querySelector('main.content h1');
  if (!h1) return;
  const here = location.pathname + location.search;
  // a page go24's menu has already (its section's link) needs no star
  if ([...side.querySelectorAll('a[href]')].some(a => a.getAttribute('href') === here && !a.closest('[data-url]'))) return;
  const own = () => [...entries(nav), ...entries(hidSub)].find(e => isCustom(e) && e.dataset.url === here);
  const star = document.createElement('button');
  star.type = 'button';
  star.className = 'ghost menu-star';
  star.innerHTML = '<i class="i i-star" aria-hidden="true"></i>';
  const mark = () => { const on = !!own(); star.classList.toggle('on', on); star.title = l(on ? 'star_in' : 'star_add'); };
  mark();
  star.addEventListener('click', async () => {
    const e = own();
    if (e) {
      e.remove();
      fixHidden();
      await save();
      mark();
      return;
    }
    const st = state();
    st.custom.push({ id: '', title: (h1.textContent || '').trim().slice(0, 80) || document.title, url: here });
    if (await save(st)) location.reload();
  });
  h1.append(star);
})();
