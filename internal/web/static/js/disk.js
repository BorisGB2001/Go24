// «Диск»: files and folders are selected with checkboxes for the actions of the bar above the list
// (download as ZIP, move, copy, delete); «Переместить» / «Копировать» pick the drive and the folder in a dialog;
// files dragged from the computer upload into the folder shown (or into the folder row they are dropped on), with
// the progress of each; rows dragged onto a folder or onto the path above move there, onto «Корзина» — go to the trash.
// A click on a file opens the viewer (pictures, PDF, video, sound, documents as HTML) with ‹ › through the files of
// the folder; «Список» / «Плитка» is remembered in this browser.
(() => {
  const root = document.querySelector('[data-disk]');
  if (!root) return;
  const base = root.dataset.disk;
  const folder = root.dataset.folder;
  const write = root.dataset.write === 'true';
  const l = k => root.dataset['l' + k.replace(/(^|-)(\w)/g, (_, __, c) => c.toUpperCase())] || k;
  const which = base === '/disk/my' ? 'my' : base === '/disk/company' ? 'company' :
    base.startsWith('/disk/sh/') ? 'sh:' + base.slice(9) : base.startsWith('/disk/u/') ? 'u:' + base.slice(8) : base.replace('/disk/p/', '');
  // the filter by type applies as soon as it is picked
  document.querySelector('[data-disk-filter] select')?.addEventListener('change', e => e.target.form.requestSubmit());

  // ---- selection ----
  const rows = () => [...root.querySelectorAll('tr[data-item]')];
  const selected = () => rows().filter(r => r.querySelector('[data-sel]')?.checked);
  const bulk = root.querySelector('[data-disk-bulk]');
  const all = root.querySelector('[data-disk-all]');
  const sync = () => {
    const n = selected().length;
    bulk.hidden = n === 0;
    root.querySelector('[data-disk-count]').textContent = n;
    if (all) { all.checked = n > 0 && n === rows().length; all.indeterminate = n > 0 && n < rows().length; }
  };
  root.addEventListener('change', e => {
    if (e.target.matches('[data-disk-all]')) rows().forEach(r => { r.querySelector('[data-sel]').checked = e.target.checked; });
    if (e.target.matches('[data-disk-all], [data-sel]')) sync();
  });
  // the selection as form fields: file=ID, folder_id=ID
  const fields = list => list.map(r => [r.dataset.item === 'file' ? 'file' : 'folder_id', r.dataset.id]);
  const putFields = (form, list) => {
    form.querySelectorAll('[data-picked]').forEach(i => i.remove());
    for (const [k, v] of fields(list)) {
      const i = document.createElement('input');
      i.type = 'hidden'; i.name = k; i.value = v; i.dataset.picked = '';
      form.append(i);
    }
  };
  root.querySelectorAll('[data-disk-del], [data-disk-sel-form]').forEach(f => f.addEventListener('submit', e => putFields(e.target, selected()), true));

  // ---- move / copy dialog ----
  const dlg = document.querySelector('[data-disk-dlg]');
  const form = dlg?.querySelector('[data-disk-move]');
  const field = n => form.elements.namedItem(n);
  const pick = { which, folder: '' };
  const show = async () => {
    let d;
    try {
      const res = await fetch('/mail/drive?which=' + encodeURIComponent(pick.which) + '&folder=' + encodeURIComponent(pick.folder));
      d = await res.json();
      if (!res.ok) throw new Error(d.error);
    } catch { pick.which = 'my'; pick.folder = ''; if (pick.retry !== false) { pick.retry = false; return show(); } return; }
    pick.retry = true;
    const sel = dlg.querySelector('[data-disk-drives]');
    sel.replaceChildren(...d.drives.map(x => new Option(x.name, x.which, false, x.which === pick.which)));
    const path = dlg.querySelector('[data-disk-path]');
    const crumb = (name, id) => {
      const a = document.createElement('a');
      a.href = '#'; a.textContent = name; a.dataset.go = id;
      return a;
    };
    path.replaceChildren(crumb(l('top'), ''), ...d.path.flatMap(f => [' › ', crumb(f.name, f.id)]));
    // the folders being moved are not offered as the place to move them into
    const moving = new Set(field('copy').value === '1' ? [] : [...form.querySelectorAll('[data-picked][name=folder_id]')].map(i => i.value));
    dlg.querySelector('[data-disk-folders]').replaceChildren(...d.folders.filter(f => !moving.has(f.id)).map(f => {
      const a = crumb('📁 ' + f.name, f.id);
      a.className = 'disk-pick-row';
      return a;
    }));
    const here = dlg.querySelector('[data-disk-here]');
    here.disabled = !d.write;
    here.title = d.write ? '' : l('no-write');
    field('to').value = pick.which;
    field('to_folder').value = pick.folder;
  };
  const open = (list, copy) => {
    if (!list.length || !dlg) return;
    field('copy').value = copy ? '1' : '';
    putFields(form, list);
    dlg.querySelector('[data-disk-dlg-title]').textContent = l(copy ? 'copy-title' : 'move-title');
    dlg.querySelector('[data-disk-here]').textContent = l(copy ? 'copy' : 'move');
    pick.which = which; pick.folder = folder;
    show();
    dlg.showModal?.();
  };
  dlg?.addEventListener('click', e => {
    const go = e.target.closest('[data-go]');
    if (go) { e.preventDefault(); pick.folder = go.dataset.go; show(); }
    if (e.target.closest('[data-disk-close]')) dlg.close();
  });
  dlg?.querySelector('[data-disk-drives]').addEventListener('change', e => { pick.which = e.target.value; pick.folder = ''; show(); });

  root.addEventListener('click', e => {
    const b = e.target.closest('[data-disk-act]');
    if (!b) return;
    const row = b.closest('tr[data-item]');
    const list = row ? [row] : selected();
    b.closest('details')?.removeAttribute('open');
    if (b.dataset.diskAct === 'zip') {
      location.href = base + '?zip&' + new URLSearchParams(fields(list));
      return;
    }
    open(list, b.dataset.diskAct === 'copy');
  });

  // ---- moving rows by dragging ----
  const post = async (url, body) => {
    document.documentElement.classList.add('go24-busy');
    try {
      const res = await fetch(url, { method: 'POST', headers: { 'X-Go24-Form': '1' }, body });
      const data = await res.json().catch(() => null);
      if (!res.ok) { alert(data?.error || res.statusText); return false; }
      return true;
    } finally { document.documentElement.classList.remove('go24-busy'); }
  };
  let dragged = null;
  root.addEventListener('dragstart', e => {
    const row = e.target.closest?.('tr[data-item]');
    if (!row) return;
    const sel = selected();
    dragged = sel.includes(row) ? sel : [row];
    e.dataTransfer.effectAllowed = 'move';
    e.dataTransfer.setData('text/plain', row.dataset.name);
  });
  root.addEventListener('dragend', () => { dragged = null; });
  // a drop target: a folder row (not one of those dragged), a folder of the path above, the trash (rows only)
  const target = e => {
    const t = e.target.closest?.(dragged ? 'tr[data-item=folder], [data-drop-folder], [data-drop-trash]' : 'tr[data-item=folder], [data-drop-folder]');
    if (!t || (dragged && dragged.includes(t))) return null;
    return t;
  };
  const targetID = t => t.dataset.dropFolder ?? t.dataset.id;
  const files = e => [...(e.dataTransfer?.types || [])].includes('Files');

  // ---- uploading ----
  const progress = root.querySelector('[data-disk-progress]');
  // list: files, or {file, path} for the files of an uploaded folder — the server makes the folders of the path
  const upload = async (list, into) => {
    if (!list.length) return;
    progress.hidden = false;
    const storage = document.querySelector('[data-disk-upload] [name=storage]')?.value || '';
    let failed = false;
    for (const it of list) {
      const { file: f, path: dir = '' } = it.file ? it : { file: it };
      const line = document.createElement('div');
      line.className = 'disk-up';
      line.innerHTML = '<span></span><progress max="100" value="0"></progress><small></small>';
      line.firstChild.textContent = dir ? dir + '/' + f.name : f.name;
      progress.append(line);
      const body = new FormData();
      body.append('folder', into);
      body.append('path', dir);
      body.append('storage', storage);
      body.append('file', f, f.name);
      const ok = await new Promise(resolve => {
        const x = new XMLHttpRequest();
        x.open('POST', base + '/upload');
        x.setRequestHeader('Accept', 'application/json');
        x.upload.onprogress = ev => { if (ev.lengthComputable) line.querySelector('progress').value = 100 * ev.loaded / ev.total; };
        x.onload = () => {
          let err = '';
          if (x.status !== 200) { try { err = JSON.parse(x.responseText).error; } catch { err = x.statusText; } }
          line.querySelector('small').textContent = err ? l('failed') + ': ' + err : '';
          resolve(!err);
        };
        x.onerror = () => { line.querySelector('small').textContent = l('failed'); resolve(false); };
        x.send(body);
      });
      if (ok) line.querySelector('progress').value = 100;
      failed ||= !ok;
    }
    if (!failed) location.reload();
  };
  window.go24DiskUpload = upload; // for tests

  // a dropped folder: all its files with their paths, read through the entries of the drop
  const readEntry = async (entry, dir, out) => {
    if (entry.isFile) {
      out.push({ file: await new Promise((ok, no) => entry.file(ok, no)), path: dir });
      return;
    }
    if (!entry.isDirectory) return;
    const sub = dir ? dir + '/' + entry.name : entry.name;
    const reader = entry.createReader();
    for (;;) { // readEntries gives a folder by parts until an empty one
      const part = await new Promise((ok, no) => reader.readEntries(ok, no));
      if (!part.length) return;
      for (const e of part) await readEntry(e, sub, out);
    }
  };
  const dropped = async dt => {
    const entries = [...(dt.items || [])].map(i => i.webkitGetAsEntry?.()).filter(Boolean); // only while the drop lasts
    if (!entries.some(e => e.isDirectory)) return [...dt.files];
    const out = [];
    for (const e of entries) await readEntry(e, '', out);
    return out;
  };

  // «Загрузить папку»: the files come with their paths from the folder picked
  const dirPick = document.querySelector('[data-disk-dir]');
  if (dirPick && 'webkitdirectory' in document.createElement('input')) {
    dirPick.hidden = false;
    dirPick.querySelector('input').addEventListener('change', e => {
      upload([...e.target.files].map(f => ({ file: f, path: (f.webkitRelativePath || '').split('/').slice(0, -1).join('/') })), folder);
    });
  }

  document.querySelector('[data-disk-upload]')?.addEventListener('submit', e => {
    if (!window.XMLHttpRequest || !window.FormData) return;
    e.preventDefault();
    upload([...e.target.elements.namedItem('file').files], folder);
  });

  const drop = root.querySelector('[data-disk-drop]');
  let mark = null;
  const unmark = () => { mark?.classList.remove('drop-on'); mark = null; };
  document.addEventListener('dragover', e => {
    if (!write || !(files(e) || dragged)) return;
    const t = target(e);
    if (!t && !files(e)) { unmark(); return; }
    e.preventDefault();
    e.dataTransfer.dropEffect = dragged ? 'move' : 'copy';
    if (t !== mark) { unmark(); mark = t; mark?.classList.add('drop-on'); }
    if (files(e)) drop.hidden = !!t;
  });
  document.addEventListener('dragleave', e => { if (!e.relatedTarget) { unmark(); drop.hidden = true; } });
  document.addEventListener('drop', async e => {
    if (!write || !(files(e) || dragged)) return;
    const t = target(e);
    unmark(); drop.hidden = true;
    if (!t && !files(e)) return;
    e.preventDefault();
    if (t && 'dropTrash' in t.dataset) {
      const body = new URLSearchParams([['folder', folder], ...fields(dragged)]);
      dragged = null;
      if (await post(base + '/delete', body)) location.reload();
      return;
    }
    const into = t ? targetID(t) : folder;
    if (files(e)) { upload(await dropped(e.dataTransfer), into); return; }
    if (into === folder) return;
    const body = new URLSearchParams([['folder', folder], ['to', which], ['to_folder', into], ...fields(dragged)]);
    dragged = null;
    if (await post(base + '/move', body)) location.reload();
  });

  // ---- list or tiles ----
  const viewKey = 'go24.disk.view';
  const setView = v => {
    root.classList.toggle('tiles', v === 'tiles');
    document.querySelectorAll('[data-view-mode]').forEach(b => b.classList.toggle('on', b.dataset.viewMode === v));
  };
  let view = 'list';
  try { view = localStorage.getItem(viewKey) || 'list'; } catch { /* not remembered */ }
  setView(view);
  document.querySelector('[data-disk-view]')?.addEventListener('click', e => {
    const b = e.target.closest('[data-view-mode]');
    if (!b) return;
    setView(b.dataset.viewMode);
    try { localStorage.setItem(viewKey, b.dataset.viewMode); } catch { /* private mode */ }
  });

  // ---- the viewer ----
  const el = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };
  let viewer = null;
  const closeViewer = () => { viewer?.remove(); viewer = null; };
  const fileLinks = () => rows().filter(r => r.dataset.item === 'file').map(r => r.querySelector('td:not(.thumb) a[data-view]')).filter(Boolean);
  const showFile = async i => {
    const links = fileLinks();
    if (!links.length) return;
    i = (i + links.length) % links.length;
    closeViewer();
    const a = links[i];
    const id = a.dataset.view, name = a.dataset.name, kind = a.dataset.kind;
    const raw = '/files/' + encodeURIComponent(id) + '/raw';
    viewer = el('div', 'mviewer');
    viewer.setAttribute('role', 'dialog');
    viewer.setAttribute('aria-label', name);
    viewer.dataset.index = i;
    const btn = (text, title, fn, cls) => { const b = el('button', 'ghost small' + (cls ? ' ' + cls : ''), text); b.type = 'button'; b.title = title; b.addEventListener('click', fn); return b; };
    const prev = btn('‹', l('prev'), () => showFile(i - 1), 'mv-prev');
    const next = btn('›', l('next'), () => showFile(i + 1), 'mv-next');
    prev.disabled = next.disabled = links.length < 2;
    const dl = el('a', 'button ghost small', l('download'));
    dl.href = '/files/' + encodeURIComponent(id);
    const about = el('a', 'button ghost small', l('about')); // «Подробнее»: the file's card with «Обсуждение»
    about.href = '/files/' + encodeURIComponent(id) + '/about';
    const bar = el('div', 'row mviewer-bar');
    bar.append(prev, el('b', 'mviewer-name', name), el('span', 'small muted', `${i + 1} / ${links.length}`), next, el('span', 'grow'), about, dl,
      btn('×', l('close'), closeViewer, 'mviewer-x'));
    const body = el('div', 'mviewer-body');
    viewer.append(bar, body);
    viewer.addEventListener('click', e => { if (e.target === viewer || e.target === body) closeViewer(); });
    document.body.append(viewer);
    const media = (tag, cls) => { const m = el(tag, cls); m.src = raw; if (tag !== 'img') { m.controls = true; } else m.alt = name; if (tag === 'iframe') m.title = name; body.append(m); };
    if (kind === 'image') media('img');
    else if (kind === 'pdf') media('iframe', 'mviewer-pdf');
    else if (kind === 'video') media('video', 'mviewer-media');
    else if (kind === 'audio') media('audio', 'mviewer-audio');
    else if (kind === 'html') {
      body.append(el('p', 'muted', l('loading')));
      try {
        const d = await fetch('/files/' + encodeURIComponent(id) + '/preview', { headers: { Accept: 'application/json' } }).then(r => r.json());
        body.textContent = '';
        if (d.kind === 'html' && d.html) {
          const doc = el('div', 'mviewer-doc');
          doc.innerHTML = d.html; // built and escaped by the server (mail.Preview)
          body.append(doc);
        } else body.append(el('p', 'muted', l('none')));
      } catch { body.textContent = l('none'); }
    } else body.append(el('p', 'mviewer-none', l('none')));
  };
  root.addEventListener('click', e => {
    const a = e.target.closest('a[data-view]');
    if (!a || e.ctrlKey || e.metaKey || e.shiftKey || e.button !== 0) return;
    e.preventDefault();
    const row = a.closest('tr[data-item]');
    showFile(fileLinks().findIndex(x => x.closest('tr') === row));
  });
  document.addEventListener('keydown', e => {
    if (!viewer) return;
    if (e.key === 'Escape') { e.preventDefault(); closeViewer(); }
    if (e.key === 'ArrowLeft') viewer.querySelector('.mv-prev')?.click();
    if (e.key === 'ArrowRight') viewer.querySelector('.mv-next')?.click();
  }, true);

  // ---- «История версий»: the file's versions, each to download; a former one comes back with «Восстановить» ----
  const ver = document.querySelector('[data-disk-ver]');
  const verL = k => ver.dataset['l' + k[0].toUpperCase() + k.slice(1)] || k;
  root.addEventListener('click', async e => {
    const b = e.target.closest('[data-disk-versions]');
    if (!b || !ver) return;
    b.closest('details')?.removeAttribute('open');
    const id = b.dataset.diskVersions;
    let d;
    try {
      const res = await fetch('/files/' + id + '/versions');
      d = await res.json();
      if (!res.ok) throw new Error(d.error);
    } catch { return; }
    ver.querySelector('[data-disk-ver-name]').textContent = d.name;
    const vform = ver.querySelector('[data-disk-ver-form]');
    vform.elements.namedItem('file').value = id;
    ver.querySelector('[data-disk-ver-list]').replaceChildren(...d.versions.map(v => {
      const tr = document.createElement('tr');
      const td = (...kids) => { const c = document.createElement('td'); c.append(...kids); tr.append(c); return c; };
      td(String(v.no) + (v.id ? '' : ' (' + verL('current') + ')'));
      const when = td(v.date);
      const who = document.createElement('div');
      who.className = 'muted'; who.textContent = v.author;
      when.append(who);
      td(v.size_text);
      const act = td();
      if (v.id) {
        const a = document.createElement('a');
        a.className = 'button ghost'; a.href = '/files/' + id + '/versions/' + v.id; a.textContent = l('download');
        act.append(a);
        if ('mine' in b.dataset) {
          const r = document.createElement('button');
          r.type = 'button'; r.className = 'ghost'; r.textContent = verL('restore');
          r.addEventListener('click', () => { vform.elements.namedItem('version').value = v.id; vform.requestSubmit(); });
          act.append(r);
        }
      }
      return tr;
    }));
    ver.showModal?.();
  });
  ver?.addEventListener('click', e => { if (e.target.closest('[data-disk-close]')) ver.close(); });
})();
