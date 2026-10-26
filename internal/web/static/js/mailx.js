// The mail as in Outlook, step 7 (mail.html): the viewer of a letter's files (pictures, PDF, office documents —
// ← → between them, «Скачать», «Сохранить на Диск»), «Сохранить на Диск» without leaving the letter, the drive
// picker of the editor («С Диска»: a copy or a link), «Всегда показывать от …», the list and the counters following
// new letters without a reload (go24:mail from menu.js), «Печать» (Ctrl+P) and its page, the notification settings.
(() => {
  const labels = document.querySelector('template[data-mx-labels]')?.content;
  const l = k => labels?.querySelector(`[data-l="${k}"]`)?.textContent || k;
  const el = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text !== undefined) e.textContent = text; return e; };
  const form = (url, body) => fetch(url, { method: 'POST', body, headers: { Accept: 'application/json', 'X-Go24-Form': '1' } })
    .then(async r => { const d = await r.json().catch(() => ({})); if (!r.ok) throw new Error(d.error || r.statusText); return d; });
  const say = text => {
    let box = document.querySelector('.toasts');
    if (!box) { box = el('div', 'toasts'); document.body.append(box); }
    const t = el('div', 'toast mx-say', text);
    box.append(t);
    setTimeout(() => t.remove(), 5000);
  };

  // the print page prints itself
  if (document.querySelector('[data-mail-print]')) {
    document.querySelector('[data-print-now]')?.addEventListener('click', () => print());
    addEventListener('load', () => setTimeout(() => print(), 300));
    return;
  }

  // ---------- the mail's drop-down menus stay in the window, as Outlook's ----------
  // A menu opens under its button and inside the window: pushed left off the right edge, up when there is no room
  // below (a scroll of its own when there is none either way). Fixed, so a pane that scrolls does not cut it.
  const place = d => {
    const pop = d.querySelector(':scope > .mbox-pop'), btn = d.querySelector(':scope > summary');
    if (!pop || !btn) return;
    if (!d.open) { pop.removeAttribute('style'); return; }
    const gap = 8, b = btn.getBoundingClientRect();
    pop.style.cssText = 'position: fixed; left: 0; top: 0; right: auto; bottom: auto; max-height: none; max-width: calc(100vw - 16px)';
    const w = pop.offsetWidth, h = pop.offsetHeight, vw = document.documentElement.clientWidth, vh = innerHeight;
    const left = Math.max(gap, Math.min(b.left, vw - w - gap));
    let top = b.bottom + 4, max = 0;
    if (top + h > vh - gap) {
      if (b.top - 4 - h >= gap) top = b.top - 4 - h;
      else if (vh - b.bottom > b.top) max = vh - gap - top;
      else { top = gap; max = b.top - 4 - gap; }
    }
    pop.style.left = left + 'px';
    pop.style.top = top + 'px';
    if (max) { pop.style.maxHeight = max + 'px'; pop.style.overflowY = 'auto'; }
  };
  const menus = () => document.querySelectorAll('details[open] > .mbox-pop');
  document.addEventListener('toggle', e => { if (e.target.matches?.('details') && e.target.querySelector(':scope > .mbox-pop')) place(e.target); }, true);
  addEventListener('resize', () => menus().forEach(p => place(p.parentElement)));
  document.addEventListener('scroll', e => { if (!e.target.closest?.('.mbox-pop')) menus().forEach(p => place(p.parentElement)); }, true);

  // ---------- the notification settings: the browser's permission, a test ----------
  const desk = document.querySelector('input[name=desktop]');
  if (desk && document.querySelector('[data-notify-test]')) {
    const state = document.querySelector('[data-desktop-state]');
    const show = () => { if (state) state.hidden = !(window.Notification && Notification.permission === 'denied'); };
    show();
    desk.addEventListener('change', async () => { if (desk.checked && window.Notification?.permission === 'default') await Notification.requestPermission(); show(); });
    document.querySelector('[data-notify-test]').addEventListener('click', async () => {
      if (window.Notification?.permission === 'default') await Notification.requestPermission();
      show();
      const sound = document.querySelector('input[name=sound]')?.checked;
      const m = { id: 'test', folder: '', from: 'go24', subject: document.querySelector('[data-notify-test]').textContent.trim(), preview: '' };
      if (window.Notification?.permission === 'granted') try { new Notification(m.from, { body: m.subject }); } catch { /* none */ }
      window.go24Mail?.({ letters: [m], sound, desktop: true }, true);
    });
  }

  const root = document.querySelector('[data-mail]');
  if (!root) return;

  // ---------- the counters: the menu's and the tab's title ----------
  const title = document.title.replace(/^\(\d+\+?\) /, '');
  const setTitle = n => { document.title = (n > 0 ? `(${n > 99 ? '99+' : n}) ` : '') + title; };
  setTitle(Number(document.querySelector('[data-counter="mail"]')?.textContent) || 0);
  let recount = 0;
  const refresh = () => {
    clearTimeout(recount);
    recount = setTimeout(async () => {
      try {
        const d = await fetch('/mail/unread', { headers: { Accept: 'application/json' } }).then(r => r.json());
        document.querySelectorAll('[data-counter="mail"]').forEach(c => { c.textContent = d.unread > 99 ? '99+' : d.unread; c.hidden = !d.unread; });
        setTitle(d.unread);
      } catch { /* the counter stays */ }
    }, 400);
  };
  // a letter read (the row loses «unread») or acted on: the counters follow
  new MutationObserver(ms => { if (ms.some(m => m.attributeName === 'class' && m.oldValue?.includes('unread'))) refresh(); })
    .observe(root, { subtree: true, attributes: true, attributeFilter: ['class'], attributeOldValue: true });

  // ---------- new letters: the list follows without a reload ----------
  const list = root.querySelector('.maillist');
  const quiet = () => !root.querySelector('.msel:checked') && !document.querySelector('.mctx, .mclip-pop') && !root.querySelector('.mailrow.dragging');
  let pending = false;
  const reload = async () => {
    if (!list) return;
    if (!quiet()) { pending = true; return; }
    pending = false;
    const u = new URL(location.href);
    const open = u.searchParams.get('letter');
    u.searchParams.delete('letter');
    u.searchParams.delete('new');
    let doc;
    try {
      const res = await fetch(u, { headers: { Accept: 'text/html' } });
      if (!res.ok) return;
      doc = new DOMParser().parseFromString(await res.text(), 'text/html');
    } catch { return; }
    const fresh = doc.querySelector('.maillist');
    if (!fresh || !quiet()) return;
    const items = n => [...n.children].filter(c => c.matches('.mgroup, .mailrow, .empty, .mother-hint'));
    const old = items(list);
    const at = old[0] || null;
    const add = items(fresh);
    add.forEach(n => { if (open && n.dataset?.letter === open) n.classList.add('on'); });
    if (at) at.before(...add); else list.append(...add);
    old.forEach(n => n.remove());
    const nav = doc.querySelector('.mailnav');
    root.querySelectorAll('.mailnav .mfolder').forEach(a => { // the folders' unread counters
      const f = nav?.querySelector(`.mfolder[href="${CSS.escape(a.getAttribute('href'))}"]`);
      if (!f) return;
      a.querySelector('[data-unseen]')?.remove();
      const c = f.querySelector('[data-unseen]');
      if (c) a.append(c.cloneNode(true)); // a folder in «Избранное» is there twice
    });
  };
  addEventListener('go24:mail', e => {
    const d = e.detail || {};
    if (typeof d.unread === 'number') setTitle(d.unread);
    if (d.letters?.length || d.new) reload();
  });
  root.addEventListener('change', () => { if (pending) setTimeout(reload, 500); });
  document.addEventListener('visibilitychange', () => { if (pending && document.visibilityState === 'visible') reload(); });

  // ---------- the viewer of the files ----------
  let viewer = null;
  const closeViewer = () => { viewer?.remove(); viewer = null; };
  const showFile = async (links, i) => {
    closeViewer();
    const a = links[i];
    const id = a.dataset.preview, name = a.dataset.name, kind = a.dataset.kind;
    viewer = el('div', 'mviewer');
    viewer.setAttribute('role', 'dialog');
    viewer.setAttribute('aria-label', name);
    const bar = el('div', 'row mviewer-bar');
    const btn = (text, fn, cls) => { const b = el('button', 'ghost small' + (cls ? ' ' + cls : ''), text); b.type = 'button'; b.addEventListener('click', fn); return b; };
    const prev = btn('‹', () => showFile(links, (i - 1 + links.length) % links.length));
    const next = btn('›', () => showFile(links, (i + 1) % links.length));
    prev.title = l('prev'); next.title = l('next');
    prev.disabled = next.disabled = links.length < 2;
    const dl = el('a', 'button ghost small', l('download'));
    dl.href = '/files/' + encodeURIComponent(id);
    dl.download = name;
    const save = btn(l('drive'), async () => {
      try { say((await form('/mail/files/drive', new URLSearchParams({ file: id }))).text); } catch (err) { say(err.message); }
    });
    bar.append(prev, el('b', 'mviewer-name', name), el('span', 'small muted', `${i + 1} / ${links.length}`), next, el('span', 'grow'), dl, save,
      btn('×', closeViewer, 'mviewer-x'));
    const body = el('div', 'mviewer-body');
    viewer.append(bar, body);
    viewer.addEventListener('click', e => { if (e.target === viewer) closeViewer(); });
    document.body.append(viewer);
    if (kind === 'image') {
      const img = el('img');
      img.src = `/mail/files/${encodeURIComponent(id)}/raw`;
      img.alt = name;
      body.append(img);
    } else if (kind === 'pdf') {
      const f = el('iframe', 'mviewer-pdf');
      f.src = `/mail/files/${encodeURIComponent(id)}/raw`;
      f.title = name;
      body.append(f);
    } else {
      body.append(el('p', 'muted', l('loading')));
      try {
        const d = await fetch(`/mail/files/${encodeURIComponent(id)}/preview`, { headers: { Accept: 'application/json' } }).then(r => r.json());
        body.textContent = '';
        if (d.kind === 'html' && d.html) {
          const doc = el('div', 'mviewer-doc');
          doc.innerHTML = d.html; // built and escaped by the server (mail.Preview)
          body.append(doc);
        } else body.append(el('p', 'muted', l('none')));
      } catch { body.textContent = l('none'); }
    }
  };
  document.addEventListener('click', e => {
    const a = e.target.closest?.('a[data-preview]');
    if (!a || e.ctrlKey || e.metaKey || e.shiftKey || e.button !== 0) return;
    e.preventDefault();
    const links = [...a.closest('[data-mfiles]').querySelectorAll('a[data-preview]')];
    showFile(links, links.indexOf(a));
  });
  document.addEventListener('keydown', e => {
    if (!viewer) return;
    if (e.key === 'Escape') { e.preventDefault(); closeViewer(); }
    if (e.key === 'ArrowLeft') viewer.querySelector('.mviewer-bar button')?.click();
    if (e.key === 'ArrowRight') [...viewer.querySelectorAll('.mviewer-bar button')][1]?.click();
  }, true);

  // ---------- «Сохранить на Диск», «Всегда показывать от …» without leaving the letter ----------
  document.addEventListener('submit', async e => {
    const f = e.target;
    if (!f.matches?.('[data-to-drive], [data-trust]')) return;
    e.preventDefault();
    try {
      const d = await form(f.action, new URLSearchParams(new FormData(f)));
      if (f.matches('[data-trust]')) {
        f.closest('article, [data-mail-read]')?.querySelectorAll('iframe.mailhtml').forEach(fr => { fr.srcdoc = fr.srcdoc.replace("img-src 'self' data:", 'img-src * data:'); });
        f.closest('[data-pics]')?.remove();
      }
      say(d.text);
    } catch (err) { say(err.message); }
  });

  // ---------- «Отказаться от подписки»: a page of the newsletter opens at once (a tab opened later is blocked) ----------
  document.addEventListener('submit', async e => {
    const f = e.target;
    if (!f.matches?.('[data-unsub]')) return;
    e.preventDefault();
    if (f.dataset.page) window.open(f.dataset.page, '_blank', 'noopener,noreferrer');
    try {
      const d = await form(f.action, new URLSearchParams(new FormData(f)));
      const box = f.closest('[data-unsub-box]');
      box?.replaceWith(el('div', 'row small muted munsub-done', d.text));
      if (d.blocked) location.href = '/mail' + location.search.replace(/([?&])letter=[^&]*&?/, '$1');
      else say(d.text);
    } catch (err) { say(err.message); }
  });

  // ---------- «Печать»: Ctrl+P prints the open letter ----------
  document.addEventListener('keydown', e => {
    if (!(e.ctrlKey || e.metaKey) || e.key.toLowerCase() !== 'p' || e.shiftKey || e.altKey) return;
    const a = document.querySelector('[data-mail-read] a[data-print]');
    if (!a) return;
    e.preventDefault();
    window.open(a.href, '_blank', 'noopener');
  });

  // ---------- the drive picker of the editor («С Диска») ----------
  let picker = null;
  const pick = async (target, which = 'my', folder = '') => {
    let d;
    try {
      d = await fetch(`/mail/drive?which=${encodeURIComponent(which)}&folder=${encodeURIComponent(folder)}`, { headers: { Accept: 'application/json' } }).then(r => r.json());
    } catch { return; }
    picker?.remove();
    picker = el('div', 'mviewer mpicker');
    picker.setAttribute('role', 'dialog');
    const card = el('div', 'card mpicker-card');
    const head = el('div', 'row');
    const drives = el('select');
    drives.setAttribute('aria-label', l('pick'));
    for (const v of d.drives || []) { const o = el('option', '', v.name); o.value = v.which; o.selected = v.which === d.which; drives.append(o); }
    drives.addEventListener('change', () => pick(target, drives.value, ''));
    const x = el('button', 'ghost small', '×');
    x.type = 'button';
    x.title = l('cancel');
    x.addEventListener('click', () => { picker.remove(); picker = null; });
    head.append(el('b', '', l('pick')), drives, el('span', 'grow'), x);
    const crumbs = el('div', 'row small mpicker-path');
    const go = (text, id) => { const b = el('button', 'ghost small', text); b.type = 'button'; b.addEventListener('click', () => pick(target, d.which, id)); return b; };
    crumbs.append(go(l('top'), ''));
    (d.path || []).forEach(p => crumbs.append('/', go(p.name, p.id)));
    const items = el('div', 'mpicker-items');
    (d.folders || []).forEach(f => { const b = go('📁 ' + f.name, f.id); b.className = 'ghost mpicker-item'; items.append(b); });
    (d.files || []).forEach(f => {
      const row = el('div', 'row mpicker-item');
      row.append(el('span', 'grow', f.name), el('span', 'small muted', f.size_text));
      const put = (mode, text) => {
        const b = el('button', 'ghost small', text);
        b.type = 'button';
        b.addEventListener('click', async () => {
          let link = {};
          if (mode === 'link') {
            try { link = await fetch(`/mail/drive/link?file=${encodeURIComponent(f.id)}`, { headers: { Accept: 'application/json' } }).then(r => r.json()); } catch { return; }
          }
          picker.remove();
          picker = null;
          target.dispatchEvent(new CustomEvent('go24:drive', { detail: { id: f.id, name: f.name, mode, url: link.url } }));
        });
        row.append(b);
      };
      put('copy', l('copy'));
      put('link', l('link'));
      items.append(row);
    });
    if (!items.children.length) items.append(el('p', 'muted', l('empty')));
    card.append(head, crumbs, items);
    picker.append(card);
    picker.addEventListener('click', e => { if (e.target === picker) { picker.remove(); picker = null; } });
    document.body.append(picker);
  };
  document.addEventListener('go24:drivepick', e => pick(e.detail.form));
  document.addEventListener('keydown', e => { if (picker && e.key === 'Escape') { picker.remove(); picker = null; } });
})();
