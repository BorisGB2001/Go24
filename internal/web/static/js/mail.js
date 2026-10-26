// The mail as in Outlook ([data-mail], mail.html): a letter (a conversation) opens in the reading pane without
// leaving the list and is marked read there; letters are ticked for the toolbar's actions; dragged onto a folder of
// their mailbox they move there; the reading pane is on the right, at the bottom or off (cookie mail_pane), the list
// shows conversations or letters (cookie mail_conv); Outlook's hot keys work (mail.keys_help); the search box
// suggests recent searches, people and operators, and the words found are marked in the list.
(() => {
  const root = document.querySelector('[data-mail]');
  if (!root) return;
  const pane = root.querySelector('[data-mail-read]');
  const list = root.querySelector('.maillist');
  const bar = root.querySelector('[data-mail-toolbar]');
  const folder = root.dataset.folder || '';
  const mode = () => ['right', 'bottom', 'off'].find(p => root.classList.contains('pane-' + p)) || 'right';
  const cookie = (name, v) => { document.cookie = `${name}=${v}; path=/mail; max-age=31536000; samesite=lax`; };

  // the unread counters of a folder (and of «Все входящие»): +n / −n — of both its links, when it is in «Избранное» too
  const count = (el, d) => {
    if (!el) return;
    const twins = el.dataset.folderId ? root.querySelectorAll(`.mfolder[data-folder-id="${CSS.escape(el.dataset.folderId)}"]`)
      : root.querySelectorAll(`.mfolder[href="${CSS.escape(el.getAttribute('href'))}"]`);
    twins.forEach(a => {
      let c = a.querySelector('[data-unseen]');
      const n = (c ? Number(c.textContent) : 0) + d;
      if (n <= 0) { c?.remove(); return; }
      if (!c) { c = document.createElement('span'); c.className = 'cnt'; c.dataset.unseen = ''; a.append(c); }
      c.textContent = n;
    });
  };
  const folderLink = id => root.querySelector(`.mfolder[data-folder-id="${CSS.escape(id)}"]`);
  const allLink = () => root.querySelector('.mfolder[href="/mail?folder=all"]');
  const unread = row => Number(row.dataset.unread) || 1;
  const read = row => { // the row was unread: now it is not, and the counters follow
    if (!row.classList.contains('unread')) return;
    const n = unread(row);
    row.classList.remove('unread');
    row.dataset.unread = '0';
    if (folder === 'all') { count(allLink(), -n); return; }
    count(folderLink(folder), -n);
    if (folderLink(folder)?.querySelector('.i-inbox')) count(allLink(), -n);
  };
  const rows = () => [...root.querySelectorAll('.mailrow[data-letter]')];

  const open = async row => {
    rows().forEach(r => r.classList.remove('on'));
    row.classList.add('on');
    const link = row.querySelector('a.mrow-link');
    try {
      const html = await busy(async () => {
        const res = await fetch('/mail/letters/' + encodeURIComponent(row.dataset.letter) + '?folder=' + encodeURIComponent(folder));
        if (!res.ok) throw new Error(res.status);
        return res.text();
      });
      pane.innerHTML = html;
      frames(pane);
      window.go24Compose?.(pane); // a draft opens in the editor
    } catch { location.href = link.href; return; }
    if (!later(row)) read(row);
    root.classList.add('reading');
    history.replaceState(null, '', link.href);
    row.scrollIntoView?.({ block: 'nearest' });
  };
  // «Обработка сообщений»: a letter not read on opening is marked read after so many seconds in the pane (or never,
  // -1); after the open letter is moved or deleted, the next one opens, the previous one or none (data-after-move)
  let readTimer = 0;
  const later = row => {
    clearTimeout(readTimer);
    const art = pane?.querySelector('article[data-read-after]');
    if (!art) return false;
    const n = Number(art.dataset.readAfter);
    if (n > 0) {
      readTimer = setTimeout(async () => {
        if (!art.isConnected || !await post(art.dataset.unread, 'read')) return;
        delete art.dataset.readAfter;
        if (row?.isConnected) read(row);
      }, n * 1000);
    }
    return true;
  };
  const neighbor = row => {
    const all = rows(), i = all.indexOf(row);
    const next = all[i + 1], prev = all[i - 1];
    if (i < 0) return null;
    return { next: next || prev, prev: prev || next }[root.dataset.afterMove] || null;
  };
  const inPane = () => mode() !== 'off' && pane && !matchMedia?.('(max-width: 900px)').matches; // a phone shows the letter on its own page

  // an HTML letter is a page of its own in a sandboxed frame, as tall as it is; pictures from elsewhere load only
  // when asked («Показать содержимое»: the frame's policy lets them in); «Макет → Размер текста» scales it too
  const zoom = { small: '0.9', large: '1.15' }[root.dataset.textSize] || '';
  const fit = f => {
    try {
      const doc = f.contentDocument;
      if (!doc?.documentElement) return;
      doc.documentElement.style.zoom = zoom;
      f.style.height = Math.max(40, doc.documentElement.scrollHeight) + 'px';
      if (!f.dataset.watched && window.ResizeObserver && doc.body) {
        f.dataset.watched = '1';
        new ResizeObserver(() => { f.style.height = Math.max(40, doc.documentElement.scrollHeight) + 'px'; }).observe(doc.body);
      }
    } catch { /* not ours to measure */ }
  };
  const frames = el => el.querySelectorAll('iframe.mailhtml').forEach(f => {
    f.addEventListener('load', () => { delete f.dataset.watched; fit(f); });
    fit(f);
  });
  frames(root);
  later(rows().find(r => r.classList.contains('on')));
  root.addEventListener('toggle', e => { if (e.target.matches?.('details.mmsg')) e.target.querySelectorAll('iframe.mailhtml').forEach(fit); }, true);
  root.addEventListener('click', e => {
    const b = e.target.closest('[data-show-pics]');
    if (!b) return;
    b.closest('article, [data-mail-read]')?.querySelectorAll('iframe.mailhtml').forEach(f => {
      f.srcdoc = f.srcdoc.replace("img-src 'self' data:", 'img-src * data:');
    });
    b.closest('[data-pics]')?.remove();
  });

  // a click anywhere on the row opens (its picture, its margins, beside its files — not its box, buttons and files);
  // Ctrl+click ticks a letter, Shift+click the letters from the last ticked one (as in Outlook)
  let last = null;
  root.addEventListener('click', e => {
    const box = e.target.closest('.msel');
    if (box) { pick(box.closest('.mailrow'), box.checked, e.shiftKey); return; }
    const on = e.target.closest('.mailrow[data-letter]');
    const link = e.target.closest('a.mrow-link') ||
      (on && !e.target.closest('a, button, input, label, select, summary, details') ? on.querySelector('a.mrow-link') : null);
    if (!link || e.button !== 0) return;
    const row = link.closest('.mailrow');
    if (e.ctrlKey || e.metaKey || e.shiftKey) {
      e.preventDefault();
      const b = row.querySelector('.msel');
      pick(row, e.shiftKey ? true : !b.checked, e.shiftKey);
      return;
    }
    if (!inPane()) { if (!e.target.closest('a.mrow-link')) location.href = link.href; return; }
    e.preventDefault();
    open(row);
  });
  const pick = (row, on, range) => {
    const all = rows();
    const from = range && last ? all.indexOf(last) : -1;
    const to = all.indexOf(row);
    const span = from < 0 ? [row] : all.slice(Math.min(from, to), Math.max(from, to) + 1);
    span.forEach(r => { const b = r.querySelector('.msel'); if (b) b.checked = on; });
    last = row;
    sync();
  };

  // ticked letters: the toolbar shows with their number
  const ticked = () => [...root.querySelectorAll('.msel:checked')];
  const sync = () => {
    const n = ticked().length;
    bar?.classList.toggle('has-sel', n > 0);
    list?.classList.toggle('selecting', n > 0);
    const c = bar?.querySelector('[data-sel-count]');
    if (c) c.textContent = n ? String(n) : '';
    const all = bar?.querySelector('.msel-all');
    if (all) { all.checked = n > 0 && n === rows().length; all.indeterminate = n > 0 && n < rows().length; }
  };
  root.addEventListener('change', e => {
    if (e.target.matches('.msel-all')) root.querySelectorAll('.msel').forEach(b => { b.checked = e.target.checked; });
    if (e.target.matches('.msel, .msel-all')) sync();
  });
  // a ticked letter is not dragged off by the click on its box
  root.addEventListener('mousedown', e => { if (e.target.closest('.msel')) e.stopPropagation(); });

  // the reading pane's place; conversations or letters
  document.querySelectorAll('[data-pane]').forEach(b => b.addEventListener('click', () => {
    const p = b.dataset.pane;
    cookie('mail_pane', p);
    root.classList.remove('pane-right', 'pane-bottom', 'pane-off');
    root.classList.add('pane-' + p);
    document.querySelectorAll('[data-pane]').forEach(x => x.classList.toggle('on', x === b));
  }));
  root.querySelector('[data-conv]')?.addEventListener('click', e => {
    cookie('mail_conv', e.currentTarget.dataset.conv);
    location.reload();
  });

  // the panes' sizes: the borders are dragged (double click — back to the usual), at the bottom the list is
  // resized by its corner; kept in the cookie mail_cols for the server to lay the page out the same
  const sizes = { nav: 250, list: 380, listh: 320 };
  const bounds = { nav: [160, 480], list: [240, 900], listh: [120, 1200] };
  for (const k of Object.keys(sizes)) {
    const v = parseInt(getComputedStyle(root).getPropertyValue('--mail-' + k), 10);
    if (v) sizes[k] = v;
  }
  const keep = () => cookie('mail_cols', `${sizes.nav}.${sizes.list}.${sizes.listh}`);
  const size = (k, v) => {
    sizes[k] = Math.round(Math.min(bounds[k][1], Math.max(bounds[k][0], v)));
    root.style.setProperty('--mail-' + k, sizes[k] + 'px');
  };
  root.querySelectorAll('.msplit').forEach(h => {
    const k = h.dataset.split;
    h.addEventListener('pointerdown', e => {
      e.preventDefault();
      const x0 = e.clientX, w0 = sizes[k];
      h.classList.add('on');
      h.setPointerCapture?.(e.pointerId);
      const move = ev => size(k, w0 + ev.clientX - x0);
      const up = () => {
        h.classList.remove('on');
        h.removeEventListener('pointermove', move);
        h.removeEventListener('pointerup', up);
        keep();
      };
      h.addEventListener('pointermove', move);
      h.addEventListener('pointerup', up);
    });
    h.addEventListener('dblclick', () => { size(k, { nav: 250, list: 380 }[k]); keep(); });
  });
  if (list && window.ResizeObserver) {
    let t = 0;
    new ResizeObserver(() => {
      if (mode() !== 'bottom' || !list.style.height) return; // only the user's resizing sets the height inline
      clearTimeout(t);
      t = setTimeout(() => { size('listh', list.offsetHeight); list.style.height = ''; keep(); }, 300);
    }).observe(list);
  }

  // «Избранное»: folded or not (remembered in the browser), its places reordered by dragging
  const favs = root.querySelector('[data-favs]');
  if (favs) {
    try { if (localStorage.getItem('mail_favs') === 'closed') favs.open = false; } catch { /* no storage: open */ }
    favs.addEventListener('toggle', () => { try { localStorage.setItem('mail_favs', favs.open ? 'open' : 'closed'); } catch { /* no storage */ } });
    let fav = null;
    favs.addEventListener('dragstart', e => {
      fav = e.target.closest?.('[data-fav]');
      if (!fav) return;
      e.dataTransfer.effectAllowed = 'move';
      e.dataTransfer.setData('text/plain', fav.dataset.fav);
      fav.classList.add('dragging');
    });
    favs.addEventListener('dragover', e => {
      const over = fav && e.target.closest?.('[data-fav]');
      if (!over || over === fav) return;
      e.preventDefault();
      const r = over.getBoundingClientRect();
      over[e.clientY > r.top + r.height / 2 ? 'after' : 'before'](fav);
    });
    favs.addEventListener('drop', e => { if (fav) e.preventDefault(); });
    favs.addEventListener('dragend', async () => {
      if (!fav) return;
      fav.classList.remove('dragging');
      fav = null;
      const body = new URLSearchParams({ order: [...favs.querySelectorAll('[data-fav]')].map(f => f.dataset.fav).join(',') });
      try {
        await fetch('/mail/favorites/move', { method: 'POST', body, headers: { Accept: 'application/json', 'X-Go24-Form': '1' } });
      } catch { /* the server could not be reached: the order is back on the next page */ }
    });
  }

  // drag and drop: a letter (a conversation) onto a folder of the same mailbox
  let dragged = null;
  root.addEventListener('dragstart', e => {
    const row = e.target.closest?.('.mailrow[data-letter]');
    if (!row) return;
    dragged = row;
    row.classList.add('dragging');
    e.dataTransfer.effectAllowed = 'move';
    e.dataTransfer.setData('text/plain', row.dataset.letter);
  });
  root.addEventListener('dragend', () => {
    dragged?.classList.remove('dragging');
    dragged = null;
    root.querySelectorAll('.mfolder.drop').forEach(f => f.classList.remove('drop'));
  });
  const target = e => {
    const f = e.target.closest?.('.mfolder[data-folder-id]');
    return f && dragged && f.dataset.box === dragged.dataset.box && f.dataset.folderId !== folder ? f : null;
  };
  root.addEventListener('dragover', e => {
    const f = target(e);
    if (!f) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
    root.querySelectorAll('.mfolder.drop').forEach(x => x !== f && x.classList.remove('drop'));
    f.classList.add('drop');
  });
  root.addEventListener('dragleave', e => { e.target.closest?.('.mfolder')?.classList.remove('drop'); });
  root.addEventListener('drop', async e => {
    const f = target(e);
    if (!f) return;
    e.preventDefault();
    f.classList.remove('drop');
    // a ticked letter takes the other ticked ones of its mailbox along
    const moving = dragged.querySelector('.msel:checked') ? ticked().map(b => b.closest('.mailrow')).filter(r => r.dataset.box === dragged.dataset.box) : [dragged];
    const body = new URLSearchParams();
    moving.forEach(r => body.append('letter', r.dataset.ids || r.dataset.letter));
    body.append('folder', f.dataset.folderId);
    let ok = false;
    await busy(async () => {
      try {
        const res = await fetch('/mail/move', { method: 'POST', body, headers: { Accept: 'application/json', 'X-Go24-Form': '1' } });
        ok = res.ok;
        if (!ok) alert((await res.json().catch(() => null))?.error || res.statusText);
      } catch { /* the server could not be reached: the letters stay */ }
    }, f);
    if (!ok) return;
    let next = null;
    for (const row of moving) {
      if (row.classList.contains('unread')) { const n = unread(row); read(row); count(f, n); }
      if (shown(row)) { next = neighbor(row); clearTimeout(readTimer); pane.innerHTML = ''; root.classList.remove('reading'); }
      row.remove();
    }
    sync();
    if (next?.isConnected) open(next);
  });

  // «Импорт писем»: files .eml and .msg dropped onto a folder (or onto the list — into the folder shown) or chosen
  // in the folder's menu become letters of the folder, as in Outlook
  const letterFile = f => /\.(eml|msg)$/i.test(f.name) || f.type === 'message/rfc822';
  const importInto = async (id, files, el) => {
    files = [...files].filter(letterFile);
    if (!id || !files.length) return;
    const body = new FormData();
    files.forEach(f => body.append('file', f));
    let out = null;
    await busy(async () => {
      try {
        const res = await fetch(`/mail/folders/${encodeURIComponent(id)}/import`, { method: 'POST', body, headers: { Accept: 'application/json', 'X-Go24-Form': '1' } });
        out = await res.json().catch(() => null);
        if (!res.ok) { alert(out?.error || res.statusText); out = null; }
      } catch { /* the server could not be reached: nothing is imported */ }
    }, el);
    if (!out) return;
    if (id === folder && out.done) { // the folder shown: anew, with what was done on top
      const u = new URL(location.href);
      u.searchParams.set('msg', out.msg);
      location.assign(u);
      return;
    }
    alert(out.msg);
  };
  const fileTarget = e => {
    if (dragged || !e.dataTransfer?.types?.includes('Files')) return null;
    const f = e.target.closest?.('.mfolder[data-folder-id]');
    if (f) return f.hasAttribute('data-import') ? f : null;
    if (!e.target.closest?.('.maillist')) return null;
    return root.querySelector(`.mfolder[data-import][data-folder-id="${CSS.escape(folder)}"]`);
  };
  root.addEventListener('dragover', e => {
    if (dragged || e.defaultPrevented || !e.dataTransfer?.types?.includes('Files')) return; // a letter, or files for a letter being written
    const f = fileTarget(e);
    root.querySelectorAll('.mfolder.drop').forEach(x => x !== f && x.classList.remove('drop'));
    e.preventDefault(); // files dropped elsewhere are not opened in place of the mail
    e.dataTransfer.dropEffect = f ? 'copy' : 'none';
    f?.classList.add('drop');
  });
  root.addEventListener('drop', e => {
    if (dragged || e.defaultPrevented || !e.dataTransfer?.types?.includes('Files')) return;
    e.preventDefault();
    const f = fileTarget(e);
    root.querySelectorAll('.mfolder.drop').forEach(x => x.classList.remove('drop'));
    if (f) importInto(f.dataset.folderId, e.dataTransfer.files, f);
  });
  root.addEventListener('change', e => {
    const input = e.target.closest?.('input[data-mail-import]');
    if (!input) return;
    const files = [...input.files];
    input.value = '';
    input.closest('details')?.removeAttribute('open');
    importInto(input.dataset.mailImport, files, folderLink(input.dataset.mailImport));
  });

  // the paper clip of a letter with files: on hover or click, its files with what can be done with them
  const label = k => root.querySelector(`template[data-clip-labels]`)?.content.querySelector(`[data-l="${k}"]`)?.textContent || k;
  let pop = null, popFor = null, hide = 0, hover = 0;
  const close = () => { pop?.remove(); pop = popFor = null; };
  const show = async clip => {
    clearTimeout(hide);
    if (popFor === clip) return;
    close();
    popFor = clip;
    let files = [];
    try {
      const res = await fetch('/mail/files?letter=' + encodeURIComponent(clip.dataset.clip));
      if (res.ok) files = await res.json();
    } catch { /* nothing to show */ }
    if (popFor !== clip) return;
    pop = document.createElement('div');
    pop.className = 'card mclip-pop';
    pop.setAttribute('role', 'menu');
    if (!files.length) pop.textContent = label('none');
    for (const f of files) {
      const row = document.createElement('div');
      row.className = 'mclip-file';
      const name = document.createElement('span');
      name.className = 'mclip-name';
      name.textContent = f.name;
      const size = document.createElement('span');
      size.className = 'small muted';
      size.textContent = f.size_text;
      const acts = document.createElement('span');
      acts.className = 'row mclip-acts';
      const link = (text, href, extra) => { const a = document.createElement('a'); a.className = 'button ghost small'; a.textContent = text; a.href = href; Object.assign(a, extra); acts.append(a); };
      if (f.image) link(label('open'), `/files/${encodeURIComponent(f.id)}?inline=1`, { target: '_blank', rel: 'noopener' });
      link(label('download'), `/files/${encodeURIComponent(f.id)}`, { download: f.name });
      row.append(name, size, acts);
      pop.append(row);
    }
    pop.addEventListener('mouseenter', () => clearTimeout(hide));
    pop.addEventListener('mouseleave', () => { hide = setTimeout(close, 300); });
    document.body.append(pop);
    const r = clip.getBoundingClientRect();
    pop.style.top = `${window.scrollY + r.bottom + 4}px`;
    pop.style.left = `${Math.max(8, window.scrollX + r.right - 320)}px`;
  };
  root.addEventListener('click', e => {
    const clip = e.target.closest('.mclip');
    if (!clip) return;
    e.preventDefault();
    e.stopPropagation();
    if (popFor === clip) close(); else show(clip);
  }, true);
  root.addEventListener('mouseover', e => {
    const clip = e.target.closest?.('.mclip');
    if (!clip) return;
    clearTimeout(hover);
    hover = setTimeout(() => show(clip), 250);
  });
  root.addEventListener('mouseout', e => {
    const clip = e.target.closest?.('.mclip');
    if (!clip || clip.contains(e.relatedTarget)) return;
    clearTimeout(hover);
    if (popFor === clip) hide = setTimeout(close, 300);
  });
  document.addEventListener('click', e => { if (pop && !pop.contains(e.target) && !e.target.closest?.('.mclip')) close(); });
  document.addEventListener('keydown', e => { if (e.key === 'Escape') close(); });

  // deleting asks first (how many; in «Удалённые» — for good), as Boris asked
  const sure = n => {
    const forever = root.dataset.role === 'trash';
    const text = n > 1 ? label(forever ? 'gonen' : 'deln').replace('{n}', n) : label(forever ? 'gone1' : 'del1');
    return confirm(text);
  };
  const lettersIn = form => new FormData(form).getAll('letter').join(',').split(',').filter(Boolean).length;
  document.addEventListener('submit', e => {
    const form = e.target;
    if (e.submitter?.value !== 'delete' || !(form.matches?.('[data-mail-toolbar], [data-mail-act]'))) return;
    const n = form.matches('[data-mail-act]') ? 1 : ticked().length; // the reading pane: the letter (its conversation)
    if (!sure(n)) { e.preventDefault(); e.stopImmediatePropagation(); }
  }, true);
  document.addEventListener('submit', e => {
    const form = e.target;
    if (!form.matches?.('form[data-mail-act], form.mread-move') || !pane?.contains(form)) return;
    const id = form.closest('article[data-letter]')?.dataset.letter;
    const next = neighbor(rows().find(r => r.dataset.letter === id));
    form.querySelector('input[name="next"]')?.remove();
    if (!next) return;
    const f = document.createElement('input');
    Object.assign(f, { type: 'hidden', name: 'next', value: next.dataset.letter });
    form.append(f);
  });
  // work in progress shows (as forms.js does): the running line on top
  const busy = async (fn, el) => {
    document.documentElement.classList.add('go24-busy');
    el?.classList.add('busy');
    try { return await fn(); } finally { document.documentElement.classList.remove('go24-busy'); el?.classList.remove('busy'); }
  };

  // the row's quick actions on hover (delete, read, flag, pin) and the right-click menu, as Outlook's
  const shown = row => !!pane?.querySelector(`[data-letter="${CSS.escape(row.dataset.letter)}"]`);
  const post = async (ids, what) => {
    const body = new URLSearchParams({ letter: ids, do: what, back: location.pathname + location.search });
    try {
      const res = await fetch('/mail/act', { method: 'POST', body, headers: { Accept: 'application/json', 'X-Go24-Form': '1' } });
      if (!res.ok) { alert((await res.json().catch(() => null))?.error || res.statusText); return false; }
      return true;
    } catch { return false; }
  };
  const gone = row => { // the letter left the folder
    if (row.classList.contains('unread')) read(row);
    const next = shown(row) ? neighbor(row) : null;
    if (shown(row)) { clearTimeout(readTimer); pane.innerHTML = ''; root.classList.remove('reading'); }
    row.remove();
    sync();
    if (next) open(next);
  };
  root.addEventListener('click', async e => {
    const b = e.target.closest('.mquick button[data-do]');
    if (!b) return;
    e.preventDefault();
    e.stopPropagation();
    const row = b.closest('.mailrow');
    const what = b.dataset.do;
    if (b.classList.contains('busy') || (what === 'delete' && !sure(1))) return;
    if (!await busy(() => post(row.dataset.ids || row.dataset.letter, what), b)) return;
    if (['delete', 'archive', 'junk', 'notjunk'].includes(what)) gone(row);
    else if (what === 'read') read(row);
    else location.reload();
  }, true);

  let menu = null;
  const closeMenu = () => { menu?.remove(); menu = null; };
  root.addEventListener('contextmenu', e => {
    const row = e.target.closest('.mailrow[data-letter]');
    if (!row || !bar) return;
    e.preventDefault();
    closeMenu();
    const box = row.querySelector('.msel');
    if (!box?.checked) { // the menu acts on this letter, or on the ticked ones it is one of
      root.querySelectorAll('.msel:checked').forEach(b => { b.checked = false; });
      if (box) box.checked = true;
      sync();
    }
    const one = ticked().length <= 1;
    menu = document.createElement('div');
    menu.className = 'card mctx';
    menu.setAttribute('role', 'menu');
    if (one) {
      const href = mode => `/mail?box=${encodeURIComponent(row.dataset.box)}&new=1&letter=${encodeURIComponent(row.dataset.letter)}&folder=${encodeURIComponent(folder)}&mode=${mode}`;
      for (const [k, mode] of [['reply', 'reply'], ['all', 'all'], ['fwd', 'fwd'], ['fwdatt', 'fwdatt']]) {
        const a = document.createElement('a');
        a.href = href(mode);
        a.textContent = label(k);
        menu.append(a);
      }
      const w = document.createElement('a');
      w.href = row.querySelector('a.mrow-link').href + '&pane=off';
      w.target = '_blank';
      w.textContent = label('window');
      menu.append(w, document.createElement('hr'));
    }
    for (const el of bar.querySelectorAll(':scope > button[name=do], :scope > details')) {
      const c = el.cloneNode(true);
      c.querySelectorAll('input').forEach(x => (x.closest('.row') || x).remove()); // own dates: in the toolbar only
      [c, ...c.querySelectorAll('button')].filter(x => x.matches('button')).forEach(x => {
        x.setAttribute('form', 'mailact');
        if (!x.querySelector('.mlabel') && x.title) x.append(' ' + x.title.replace(/ \(.*\)$/, ''));
      });
      c.querySelectorAll('.mlabel').forEach(x => { x.style.display = 'inline'; });
      if (c.matches('details')) { c.className = 'mctx-sub'; const sm = c.querySelector('summary'); sm.className = ''; sm.append(' ' + sm.title); c.querySelector('.mbox-pop').className = 'mctx-items'; }
      menu.append(c);
    }
    document.body.append(menu);
    window.go24Pop?.(menu, closeMenu, row);
    const w = menu.offsetWidth || 240, h = menu.offsetHeight || 300;
    menu.style.left = `${window.scrollX + Math.min(e.clientX, (window.innerWidth || 1e4) - w - 8)}px`;
    menu.style.top = `${window.scrollY + Math.min(e.clientY, (window.innerHeight || 1e4) - h - 8)}px`;
  });
  document.addEventListener('click', e => { if (menu && !menu.contains(e.target)) closeMenu(); });
  document.addEventListener('keydown', e => { if (e.key === 'Escape') closeMenu(); });
  document.addEventListener('submit', () => setTimeout(closeMenu), true);

  // a double click opens the letter in its own window
  root.addEventListener('dblclick', e => {
    const link = e.target.closest('a.mrow-link');
    if (link) window.open(link.href + '&pane=off', '_blank');
  });

  // hot keys, as Outlook's: on the ticked letters, else on the open one
  const act = key => {
    const form = ticked().length ? bar : pane?.querySelector('form[data-mail-act]');
    const b = form?.querySelector(`button[data-key="${key}"]`);
    if (!b) return false;
    form.requestSubmit(b);
    return true;
  };
  const go = sel => { const a = (pane || root).querySelector(sel) || document.querySelector(sel); if (!a) return false; a.click(); return true; };
  const step = d => {
    const all = rows();
    if (!all.length) return false;
    const i = all.findIndex(r => r.classList.contains('on'));
    const row = all[Math.min(all.length - 1, Math.max(0, i < 0 ? 0 : i + d))];
    if (inPane()) open(row); else location.href = row.querySelector('a.mrow-link').href;
    return true;
  };
  document.addEventListener('keydown', e => {
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { // send the letter being written
      const form = e.target.closest?.('form');
      if (form?.querySelector('[data-compose]')) { e.preventDefault(); form.requestSubmit(); }
      return;
    }
    if (e.altKey || e.defaultPrevented || e.target.closest?.('input, textarea, select, [contenteditable=""], [contenteditable=true]')) return;
    const k = e.key.toLowerCase();
    let done = false;
    if ((e.ctrlKey || e.metaKey) && e.shiftKey && k === 'r') done = go('a[data-key="all"]');
    else if (e.ctrlKey || e.metaKey) return;
    else done = ({
      n: () => go('.pagehead a.create'),
      r: () => go('a[data-key="r"]'),
      f: () => go('a[data-key="fwd"]'),
      delete: () => act('delete'),
      e: () => act('archive'),
      q: () => act('read'),
      u: () => act('unread'),
      insert: () => act('flag'),
      arrowdown: () => step(1), j: () => step(1),
      arrowup: () => step(-1), k: () => step(-1),
    })[k]?.() || false;
    if (done) e.preventDefault();
  });

  // the ribbon over the mail, as Outlook's «Главная»: each icon does what a button of the reading pane (the open
  // letter) or of the list's toolbar (the ticked letters) does; one with nothing to act on is grey
  const ribbon = document.querySelector('[data-mribbon]');
  if (ribbon) {
    const article = () => pane?.querySelector('article[data-letter]');
    const one = sel => (ticked().length ? null : article()?.querySelector(sel)); // the open letter's only
    const doButton = (...values) => {
      const form = ticked().length ? bar : article()?.querySelector('form[data-mail-act]');
      for (const v of values) {
        const b = form?.querySelector(`button[name="do"][value="${v}"]`);
        if (b) return [form, b];
      }
      return null;
    };
    const commands = {
      reply: () => one('a[data-key="r"]'),
      all: () => one('a[data-key="all"]'),
      fwd: () => one('a[data-key="fwd"]'),
      fwdatt: () => one('a[data-reply="fwdatt"]'),
      task: () => one('a[href^="/tasks/new?mail="]'),
      print: () => one('a[data-print]'),
      move: () => one('.mread-move select'),
      delete: () => doButton('delete'),
      archive: () => doButton('archive'),
      junk: () => doButton('junk', 'notjunk'),
      pin: () => doButton('unpin', 'pin'),
      flag: () => doButton('flag:none'),
      // ticked ones: read when one of them is unread; the open one: what its pane offers
      read: () => doButton(...(ticked().length && !ticked().some(b => b.closest('.mailrow')?.classList.contains('unread')) ? ['unread'] : ['read', 'unread'])),
    };
    const update = () => {
      ribbon.querySelectorAll('[data-rb]').forEach(b => {
        const t = commands[b.dataset.rb]?.();
        b.disabled = !t;
        const on = b.dataset.rb === 'pin' ? t?.[1]?.value === 'unpin'
          : b.dataset.rb === 'flag' ? !ticked().length && !!article()?.querySelector('.mread-act .mact-menu summary.on') : false;
        b.classList.toggle('on', !!on);
      });
    };
    ribbon.addEventListener('click', e => {
      const b = e.target.closest('[data-rb]');
      if (!b || b.disabled) return;
      b.closest('details')?.removeAttribute('open');
      const t = commands[b.dataset.rb]?.();
      if (!t) return;
      if (Array.isArray(t)) t[0].requestSubmit(t[1]);
      else if (t.tagName === 'SELECT') { t.focus(); try { t.showPicker?.(); } catch { /* not by this browser: the list is in focus */ } }
      else t.click();
    });
    // what does not fit goes into «…», in its order
    const more = ribbon.querySelector('.mribbon-more');
    const pop = ribbon.querySelector('[data-mribbon-more]');
    const fit = () => {
      if (!more || !pop) return;
      [...pop.children].forEach(x => ribbon.insertBefore(x, more));
      more.hidden = true;
      if (ribbon.scrollWidth <= ribbon.clientWidth + 1) return;
      more.hidden = false;
      while (ribbon.scrollWidth > ribbon.clientWidth + 1 && more.previousElementSibling) pop.prepend(more.previousElementSibling);
    };
    root.addEventListener('change', () => queueMicrotask(update));
    root.addEventListener('click', () => queueMicrotask(update));
    if (pane && window.MutationObserver) new MutationObserver(update).observe(pane, { childList: true, subtree: true });
    window.addEventListener('resize', fit);
    update();
    fit();
  }

  // the words searched for are marked in the letters found (data-hl: the words, as the server parsed the search)
  let terms = [];
  try { terms = JSON.parse(list?.dataset.hl || '[]').map(t => String(t).trim()).filter(Boolean); } catch { /* none */ }
  const mark = el => {
    if (!terms.length) return;
    const re = new RegExp('(' + terms.map(t => t.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('|') + ')', 'gi');
    const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
    const nodes = [];
    while (walker.nextNode()) nodes.push(walker.currentNode);
    for (const n of nodes) {
      const parts = n.nodeValue.split(re);
      if (parts.length < 2) continue;
      const frag = document.createDocumentFragment();
      parts.forEach((p, i) => {
        if (!p) return;
        if (i % 2) { const m = document.createElement('mark'); m.className = 'mhl'; m.textContent = p; frag.append(m); } else frag.append(p);
      });
      n.replaceWith(frag);
    }
  };
  root.querySelectorAll('.mailrow .mfrom, .mailrow .msubj, .mailrow .mprev').forEach(mark);

  // the search box suggests as Outlook's: the recent searches, the people (from: them), the operators; ↑ ↓ pick,
  // Enter takes, Escape closes; Ctrl+E or F3 goes to the box
  const sform = root.querySelector('[data-msearch]');
  const sbox = sform?.querySelector('[data-msearch-q]');
  const spop = sform?.querySelector('[data-msuggest]');
  const sl = k => sform?.querySelector(`template[data-suggest-labels]`)?.content.querySelector(`[data-l="${k}"]`)?.textContent || k;
  const ops = () => [...(sform?.querySelector('template[data-suggest-labels]')?.content.querySelectorAll('[data-op]') || [])].map(x => [x.dataset.op, x.textContent]);
  let sseq = 0, items = [];
  const shut = () => { if (spop) { spop.hidden = true; spop.innerHTML = ''; } items = []; sbox?.setAttribute('aria-expanded', 'false'); };
  const lastWord = v => v.slice(v.search(/\S*$/));
  const take = it => {
    if (it.q !== undefined) sbox.value = it.q;
    else sbox.value = sbox.value.slice(0, sbox.value.length - lastWord(sbox.value).length) + it.put;
    shut();
    sbox.focus();
    if (it.go) sform.requestSubmit ? sform.requestSubmit() : sform.submit();
  };
  const head = (text, btn) => {
    const h = document.createElement('div');
    h.className = 'msg-head';
    h.textContent = text;
    if (btn) h.append(btn);
    spop.append(h);
  };
  const item = (it, html) => {
    const d = document.createElement('div');
    d.className = 'msg-item';
    d.setAttribute('role', 'option');
    html(d);
    d.addEventListener('mousedown', e => { e.preventDefault(); take(it); });
    spop.append(d);
    items.push({ el: d, it });
  };
  const suggest = async () => {
    if (!sbox || !spop) return;
    const v = sbox.value, n = ++sseq;
    let data = { recent: [], people: [] };
    try {
      const res = await fetch('/mail/suggest?q=' + encodeURIComponent(v.trim()));
      if (res.ok) data = await res.json();
    } catch { /* offline: the operators only */ }
    if (n !== sseq || document.activeElement !== sbox) return;
    shut();
    const word = lastWord(v).toLowerCase();
    if (data.recent?.length) {
      const clear = document.createElement('button');
      clear.type = 'button';
      clear.textContent = sl('clear');
      clear.addEventListener('mousedown', async e => {
        e.preventDefault();
        await fetch('/mail/searches/recent/clear', { method: 'POST', headers: { Accept: 'application/json', 'X-Go24-Form': '1' } }).catch(() => null);
        suggest();
      });
      head(sl('recent'), clear);
      data.recent.filter(q => q !== v.trim()).forEach(q => item({ q, go: true }, d => { d.textContent = q; }));
    }
    const pre = word.includes(':') ? word.slice(0, word.indexOf(':') + 1) : 'from:';
    if (data.people?.length && word && (!word.includes(':') || pre === 'from:' || pre === 'to:' || pre === 'от:' || pre === 'кому:')) {
      head(sl('people'));
      data.people.forEach(p => item({ put: pre + p.email + ' ' }, d => {
        const c = document.createElement('code'); c.textContent = pre;
        const b = document.createElement('span'); b.textContent = p.name ? `${p.name} <${p.email}>` : p.email;
        d.append(c, b);
      }));
    }
    const fit = ops().filter(([op]) => !word || (op.startsWith(word) && op !== word));
    if (fit.length) {
      head(sl('ops'));
      fit.forEach(([op, text]) => item({ put: op + (op.endsWith(':') ? '' : ' ') }, d => {
        const c = document.createElement('code'); c.textContent = op;
        const t = document.createElement('span'); t.className = 'muted'; t.textContent = text;
        d.append(c, t);
      }));
    }
    if (items.length) { spop.hidden = false; sbox.setAttribute('aria-expanded', 'true'); }
  };
  let stimer = 0;
  sbox?.addEventListener('input', () => { clearTimeout(stimer); stimer = setTimeout(suggest, 150); });
  sbox?.addEventListener('focus', () => { clearTimeout(stimer); stimer = setTimeout(suggest, 0); });
  sbox?.addEventListener('blur', () => setTimeout(shut, 100));
  sbox?.addEventListener('keydown', e => {
    if (e.key === 'Escape') { if (!spop.hidden) { e.preventDefault(); shut(); } return; }
    if (!items.length || !['ArrowDown', 'ArrowUp', 'Enter'].includes(e.key)) return;
    const i = items.findIndex(x => x.el.classList.contains('on'));
    if (e.key === 'Enter') {
      if (i < 0) return; // the search as typed
      e.preventDefault();
      take(items[i].it);
      return;
    }
    e.preventDefault();
    const j = e.key === 'ArrowDown' ? (i + 1) % items.length : (i <= 0 ? items.length - 1 : i - 1);
    items.forEach((x, k) => x.el.classList.toggle('on', k === j));
    items[j].el.scrollIntoView?.({ block: 'nearest' });
  });
  document.addEventListener('keydown', e => {
    if (!sbox || e.defaultPrevented || e.target.closest?.('input, textarea, select, [contenteditable=""], [contenteditable=true]')) return;
    if (((e.ctrlKey || e.metaKey) && !e.shiftKey && e.key.toLowerCase() === 'e') || e.key === 'F3') {
      e.preventDefault();
      sbox.focus();
      sbox.select?.();
    }
  });
})();
