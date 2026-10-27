// The page around an application's frame (app.html, the tabs of a card — parts/apps.html), as the portal's side of
// BX24.js: go24.js inside the frame asks it by postMessage for the tokens, the place and the settings
// (/apps/frame), calls REST with the application's token, resizes the frame, sets the title, opens go24's pages and
// the people picker. Messages are taken from the frames of applications only and answered only on their hosts.
(() => {
  const frames = [...document.querySelectorAll('iframe[data-app], iframe[data-tab]')];
  // the frame's name tells go24.js go24's address, and stays with the frame while the application moves between pages
  frames.forEach((f, i) => { f.name = 'go24|' + location.origin + '|' + (i + 1); });
  const main = document.querySelector('iframe[data-app]');
  const post = document.querySelector('form[data-app-post]');
  if (main && post) { post.target = main.name; post.submit(); }

  const state = new Map(); // a frame → what /apps/frame gave it
  const load = async f => {
    const body = new URLSearchParams(f.dataset.tab ? { tab: f.dataset.tab, id: f.dataset.card || '' }
      : { app: f.dataset.app, placement: f.dataset.placement || '', options: f.dataset.options || '' });
    const res = await fetch('/apps/frame', { method: 'POST', body, headers: { Accept: 'application/json' } });
    if (!res.ok) throw new Error('HTTP ' + res.status);
    const d = await res.json();
    d.got = Date.now();
    state.set(f, d);
    return d;
  };
  // the tokens a few minutes before they run out are taken anew
  const fresh = f => {
    const d = state.get(f);
    return d && Date.now() - d.got < (d.auth.expires_in - 300) * 1000 ? d : load(f);
  };
  const info = d => ({ auth: d.auth, placement: d.placement, options: d.options, lang: d.lang, user_id: d.user_id,
    admin: d.admin, title: d.title, app_options: d.app_options, user_options: d.user_options });

  // a go24 REST call with the application's token: path is /api/v1/… or relative to it, GET's params go to the query
  const call = async (f, { method = 'GET', path = '', params } = {}) => {
    method = String(method).toUpperCase();
    path = String(path);
    if (/^[a-z][a-z0-9+.-]*:|^\/\//i.test(path)) throw new Error('path'); // an address of another site
    const u = new URL(path.startsWith('/') ? path : '/api/v1/' + path, location.origin);
    if (u.origin !== location.origin || !u.pathname.startsWith('/api/v1/')) throw new Error('path');
    let body;
    if (params != null && (method === 'GET' || method === 'DELETE') && typeof params === 'object') {
      Object.entries(params).forEach(([k, v]) => u.searchParams.set(k, v));
    } else if (params != null) body = JSON.stringify(params);
    for (let again = true; ; again = false) {
      const d = await fresh(f);
      const res = await fetch(u, { method, body, credentials: 'omit', headers: { Authorization: 'Bearer ' + d.auth.access_token,
        Accept: 'application/json', ...(body ? { 'Content-Type': 'application/json' } : {}) } });
      if (res.status === 401 && again) { state.delete(f); continue; }
      const text = await res.text();
      let data = text;
      try { data = text ? JSON.parse(text) : null; } catch { /* not JSON: the text as it is */ }
      return { status: res.status, data, error: res.ok ? null : (data && data.error) || res.statusText };
    }
  };

  // selectUser / selectUsers: the picker of the page, the chosen people as [{id, name}]; nobody — cancelled
  const dlg = document.querySelector('dialog[data-app-select]');
  const pick = multiple => new Promise(resolve => {
    if (!dlg) return resolve([]);
    const kind = multiple ? 'many' : 'one';
    dlg.querySelector('h3').textContent = dlg.querySelector('h3').dataset['title' + (multiple ? 'Many' : 'One')];
    dlg.querySelectorAll('div[data-one], div[data-many]').forEach(el => { el.hidden = !(kind in el.dataset); });
    const box = dlg.querySelector(`div[data-${kind}] [data-upick]`);
    box.querySelectorAll('.upick-chip').forEach(c => c.remove());
    dlg.returnValue = '';
    dlg.addEventListener('close', () => {
      resolve(dlg.returnValue !== 'ok' ? [] : [...box.querySelectorAll('.upick-chip')].map(c =>
        ({ id: c.dataset.id, name: c.querySelector('.upick-name')?.textContent.trim() || '' })));
    }, { once: true });
    dlg.showModal();
    box.querySelector('.upick-q')?.focus();
  });

  const commands = {
    init: async f => info(await fresh(f)),
    refreshAuth: async f => (await load(f)).auth,
    call,
    resize: async (f, { width, height } = {}) => {
      if (+height > 0) f.style.height = Math.min(Math.max(+height, 50), 10000) + 'px';
      if (+width > 0) f.style.width = Math.min(+width, f.parentElement.clientWidth) + 'px';
      return { width: f.clientWidth, height: f.clientHeight };
    },
    size: async f => ({ width: f.clientWidth, height: f.clientHeight, scrollWidth: document.documentElement.scrollWidth,
      scrollHeight: document.documentElement.scrollHeight }),
    setTitle: async (f, { title = '' } = {}) => {
      if (f === main && title) document.title = String(title).slice(0, 200);
      return true;
    },
    // a page of go24 (its path) in the main window, never another site
    openPath: async (f, { path = '' } = {}) => {
      const u = new URL(String(path), location.origin);
      if (u.origin !== location.origin) throw new Error('path');
      location.assign(u.pathname + u.search + u.hash);
      return true;
    },
    reload: async f => {
      if (f === main) location.reload(); else f.src = f.dataset.src;
      return true;
    },
    close: async f => {
      if (f === main) location.assign('/apps'); else { const d = f.closest('details'); if (d) d.open = false; }
      return true;
    },
    scroll: async (f, { y = 0 } = {}) => { window.scrollTo({ top: f.getBoundingClientRect().top + scrollY + (+y || 0) }); return true; },
    selectUsers: async (f, { multiple } = {}) => pick(!!multiple),
  };

  addEventListener('message', async e => {
    const m = e.data;
    if (!m || m.go24 !== 'call' || typeof m.cmd !== 'string') return;
    const f = frames.find(x => x.contentWindow === e.source);
    if (!f || !Object.hasOwn(commands, m.cmd)) return;
    let reply;
    try {
      const d = state.get(f) || await load(f);
      if (!d.origins.includes(e.origin)) return; // the frame went to another site: it gets nothing
      reply = { result: await commands[m.cmd](f, m.args || {}) };
    } catch (err) {
      reply = { error: String(err && err.message || err) };
    }
    // a static application's sandbox has no origin to name («null»): the reply goes to that frame's window only
    e.source.postMessage({ go24: 'reply', id: m.id, ...reply }, e.origin === 'null' ? '*' : e.origin);
  });
})();
