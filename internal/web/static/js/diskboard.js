// «Доски»: the board in Excalidraw. The scene comes from data-scene (X-Board-Stamp names its state); changes are
// saved there a moment after drawing stops, with ?stamp= of the state they were made on. Someone saved meanwhile
// (409) — the newer scene is merged in (Excalidraw's reconcileElements, element by element) and saved again.
// While the board is open, ?live tells what the others do: who is on the board, where their pointers are, and
// that they saved — then their scene is fetched and merged at once (and every half a minute anyway). On a public
// link (data-alone) the board is only shown.
(function () {
  const box = document.getElementById('board-editor');
  if (!box) return;
  const L = box.dataset, edit = L.edit === 'true';
  const status = document.querySelector('[data-board-status]'), people = document.querySelector('[data-board-people]');
  window.EXCALIDRAW_ASSET_PATH = L.assets;
  const SAVE_AFTER = 1500, SAVE_TOGETHER = 600, POLL = 30000, GONE = 60000, POINTER = 120;
  const TOMBSTONE = 24 * 3600e3; // deleted elements are kept a day for merging

  let X, api, stamp = '', saved = -1, saving = null, dirty = false, behind = false, timer = 0;
  let conn = '', me = '', lastPtr = 0;
  const peers = new Map(); // connection → {u, n, seen, pointer, button}

  const say = (text, bad) => { if (status) { status.textContent = text || ''; status.classList.toggle('err', !!bad); } };

  const fetchScene = (cond) => fetch(L.scene, {headers: cond && stamp ? {'If-None-Match': '"' + stamp + '"'} : {}})
    .then((r) => {
      if (r.status === 304) return null;
      if (!r.ok) throw new Error(r.status);
      const s = r.headers.get('X-Board-Stamp') || '';
      return r.json().then((data) => ({data, stamp: s}));
    });

  // scene is what is saved: every element, the deleted ones too (so that a deletion merges), the pictures in use
  function scene() {
    const now = Date.now();
    const elements = api.getSceneElementsIncludingDeleted().filter((e) => !e.isDeleted || now - (e.updated || 0) < TOMBSTONE);
    const used = new Set(elements.filter((e) => !e.isDeleted && e.fileId).map((e) => e.fileId));
    const all = api.getFiles(), files = {};
    used.forEach((id) => { if (all[id]) files[id] = all[id]; });
    const st = api.getAppState();
    return {type: 'excalidraw', version: 2, source: 'go24', elements,
      appState: {viewBackgroundColor: st.viewBackgroundColor, gridSize: st.gridSize, gridModeEnabled: st.gridModeEnabled}, files};
  }

  // merge brings a scene saved by someone else into the editor
  function merge(data) {
    const r = X.restore(data, null, null);
    const els = X.reconcileElements(api.getSceneElementsIncludingDeleted(), r.elements, api.getAppState());
    const files = Object.values(r.files || {});
    if (files.length) api.addFiles(files);
    api.updateScene({elements: els, captureUpdate: X.CaptureUpdateAction.NEVER});
  }

  function save() {
    clearTimeout(timer);
    if (!edit || saving) return saving;
    const version = X.hashElementsVersion(api.getSceneElementsIncludingDeleted());
    if (version === saved) { dirty = false; return Promise.resolve(); }
    say(L.lSaving);
    const body = JSON.stringify(scene());
    saving = fetch(L.scene.replace(/\?scene$/, '/save?stamp=') + encodeURIComponent(stamp) + '&c=' + conn, {
      method: 'POST', headers: {'Content-Type': 'application/json', 'Accept': 'application/json'}, body,
    }).then((r) => r.json().then((j) => ({r, j}))).then(({r, j}) => {
      saving = null;
      if (r.status === 409) { // saved by someone else: merge theirs and save the whole again
        return fetchScene(false).then((s) => { stamp = s.stamp; merge(s.data); say(L.lMerged); return save(); });
      }
      if (!r.ok) throw new Error(j.error || r.status);
      stamp = j.stamp;
      saved = version;
      dirty = X.hashElementsVersion(api.getSceneElementsIncludingDeleted()) !== saved;
      say(L.lSaved);
      if (dirty) schedule();
      else if (behind) pull();
    }).catch((e) => { saving = null; say(L.lFailed + (e && e.message ? ': ' + e.message : ''), true); schedule(5000); });
    return saving;
  }

  function schedule(after) {
    clearTimeout(timer);
    timer = setTimeout(save, after || (peers.size ? SAVE_TOGETHER : SAVE_AFTER));
  }

  // pull merges the scene the others saved; while a save of ours is on its way it waits for it
  function pull() {
    if (saving) { behind = true; return; }
    behind = false;
    fetchScene(true).then((s) => {
      if (!s) return;
      if (saving) { behind = true; return; }
      stamp = s.stamp;
      merge(s.data);
      if (!dirty) saved = X.hashElementsVersion(api.getSceneElementsIncludingDeleted());
      else schedule();
    }).catch(() => {});
  }

  function poll() {
    if (!document.hidden) pull();
    const now = Date.now();
    let gone = false;
    peers.forEach((p, c) => { if (now - p.seen > GONE) { peers.delete(c); gone = true; } });
    if (gone) showPeers();
  }

  // showPeers: who else is on the board (in the bar) and their pointers on it
  function showPeers() {
    if (people) {
      const names = [...new Map([...peers.values()].filter((p) => p.u !== me).map((p) => [p.u, p.n])).values()];
      people.textContent = names.length ? L.lHere + ' ' + names.join(', ') : '';
      people.hidden = !names.length;
    }
    if (!api) return;
    const collaborators = new Map();
    peers.forEach((p, c) => {
      collaborators.set(c, {username: p.n, id: p.u, socketId: c, button: p.button || 'up',
        pointer: p.pointer ? {x: p.pointer.x, y: p.pointer.y, tool: 'pointer'} : undefined});
    });
    api.updateScene({collaborators, captureUpdate: X.CaptureUpdateAction.NEVER});
  }

  function listen() {
    if (!window.EventSource) return;
    const es = new EventSource(L.scene.replace(/\?scene$/, '?live'));
    es.addEventListener('hello', (e) => {
      const h = JSON.parse(e.data);
      conn = h.c; me = h.u;
      peers.clear();
      showPeers();
      pull(); // what was saved while the stream was down
    });
    es.addEventListener('board', (e) => {
      const ev = JSON.parse(e.data);
      if (ev.c === conn) return;
      if (ev.t === 'saved') { if (ev.s !== stamp) pull(); return; }
      if (ev.t === 'leave') { peers.delete(ev.c); showPeers(); return; }
      const p = peers.get(ev.c) || {u: ev.u, n: ev.n};
      p.seen = Date.now();
      if (ev.t === 'ptr') { p.pointer = {x: ev.x, y: ev.y}; p.button = ev.b; }
      const fresh = !peers.has(ev.c);
      peers.set(ev.c, p);
      if (fresh || ev.t === 'ptr') showPeers();
    });
  }

  // pointer tells the others where the pointer is, a few times a second and only when someone else is here
  function pointer(payload) {
    if (!conn || !peers.size || !payload.pointer) return;
    const now = Date.now();
    if (now - lastPtr < POINTER && payload.button !== 'down') return;
    lastPtr = now;
    const f = new URLSearchParams({x: payload.pointer.x.toFixed(1), y: payload.pointer.y.toFixed(1), b: payload.button, c: conn});
    fetch(L.scene.replace(/\?scene$/, '/pointer'), {method: 'POST', body: f, headers: {'Accept': 'application/json'}}).catch(() => {});
  }

  function start(first) {
    const R = X.React, restored = X.restore(first.data, null, null);
    stamp = first.stamp;
    const props = {
      initialData: {elements: restored.elements, appState: Object.assign({}, restored.appState, {viewModeEnabled: !edit}),
        files: restored.files, scrollToContent: true},
      langCode: L.lang === 'ru-RU' ? 'ru-RU' : 'en',
      viewModeEnabled: !edit,
      excalidrawAPI: (a) => {
        api = a;
        setTimeout(() => { saved = X.hashElementsVersion(api.getSceneElementsIncludingDeleted()); }, 0);
      },
      onChange: () => {
        if (!api || !edit) return;
        if (X.hashElementsVersion(api.getSceneElementsIncludingDeleted()) === saved) return;
        dirty = true;
        schedule();
      },
      onPointerUpdate: pointer,
      isCollaborating: true,
      UIOptions: {canvasActions: {loadScene: false, saveToActiveFile: false}},
    };
    X.createRoot(box).render(R.createElement(X.Excalidraw, props));
    if (L.alone === 'true') return; // a public link: the board as it is, nobody to work with
    setInterval(poll, POLL);
    listen();
  }

  window.addEventListener('beforeunload', (e) => {
    if (!dirty) return;
    save();
    e.preventDefault();
    e.returnValue = L.lUnsaved;
  });

  const ready = new Promise((res) => {
    if (window.Go24Excalidraw) return res();
    window.addEventListener('go24-excalidraw', res, {once: true});
  });
  Promise.all([ready, fetchScene(false)]).then(([, first]) => { X = window.Go24Excalidraw; start(first); })
    .catch(() => { box.textContent = L.lLoadFailed; box.classList.add('empty'); });
})();
