// go24.js — the library of an application's page in go24's frame, as BX24.js. The page puts
// <script src="https://<go24>/go24.js"></script> and calls go24.init(); then:
//   go24.getAuth(), go24.refreshAuth()          — the employee's tokens ({access_token, refresh_token, expires_in, domain, member_id})
//   go24.callMethod(method, path, params)       — go24 REST (/api/v1/…) with the application's token: {status, data, error}
//   go24.placement.info()                       — {placement, options}: where the frame is (DEFAULT, CRM_DEAL_DETAIL_TAB…)
//   go24.getLang(), getDomain(), getUserId(), isAdmin()
//   go24.resizeWindow(width, height), fitWindow(), getScrollSize(), scrollParentWindow(y)
//   go24.setTitle(title), openPath(path), reloadWindow(), closeApplication()
//   go24.selectUser(), selectUsers()            — go24's people picker: {id, name} / [{id, name}], null — cancelled
//   go24.appOption.get(name) / set(name, value) — the application's settings (set — administrators)
//   go24.userOption.get(name) / set(name, value) — the employee's
// Every call returns a Promise and takes a callback last as well. Messages go only to go24's page around the frame.
(() => {
  if (window.go24 && window.go24.version) return;
  // go24's address: the frame's name (go24|<origin>|n, kept while the application moves between its pages) or,
  // on the first page, DOMAIN and PROTOCOL in its address
  const origin = (() => {
    const parts = String(window.name || '').split('|');
    if (parts[0] === 'go24' && /^https?:\/\/[^/]+$/.test(parts[1] || '')) return parts[1];
    const q = new URLSearchParams(location.search);
    return q.get('DOMAIN') ? (q.get('PROTOCOL') === '1' ? 'https://' : 'http://') + q.get('DOMAIN') : '';
  })();
  const inFrame = window.parent !== window && origin !== '';

  let seq = 0, data = null, ready = null;
  const waiting = new Map();
  addEventListener('message', e => {
    const m = e.data;
    if (e.origin !== origin || e.source !== window.parent || !m || m.go24 !== 'reply' || !waiting.has(m.id)) return;
    const { resolve, reject } = waiting.get(m.id);
    waiting.delete(m.id);
    if (m.error) reject(new Error(m.error)); else resolve(m.result);
  });
  const ask = (cmd, args) => new Promise((resolve, reject) => {
    if (!inFrame) return reject(new Error('go24.js: the page is not in go24\'s frame'));
    const id = ++seq;
    waiting.set(id, { resolve, reject });
    window.parent.postMessage({ go24: 'call', id, cmd, args: args || {} }, origin);
  });
  // a callback last, as BX24.js takes it, besides the Promise
  const cb = (p, fn) => {
    if (typeof fn === 'function') p.then(fn, () => fn(undefined));
    return p;
  };
  const sizes = () => ({ scrollWidth: document.documentElement.scrollWidth,
    scrollHeight: Math.max(document.documentElement.scrollHeight, document.body ? document.body.scrollHeight : 0) });

  const option = (key, path) => ({
    get: name => (data && data[key] ? (name === undefined ? data[key] : data[key][name]) : undefined),
    set: (name, value, fn) => cb(ask('call', { method: 'POST', path, params: typeof name === 'object' ? name : { [name]: value === undefined ? null : value } })
      .then(r => {
        if (r.error) throw new Error(r.error);
        if (data) data[key] = r.data;
        return r.data;
      }), fn),
  });

  window.go24 = {
    version: 1,
    init(fn) {
      ready = ready || ask('init').then(d => { data = d; return d; });
      return cb(ready.then(() => window.go24), fn);
    },
    getAuth: () => (data ? { ...data.auth } : false),
    refreshAuth(fn) {
      return cb(ask('refreshAuth').then(a => { if (data) data.auth = a; return { ...a }; }), fn);
    },
    // method: GET, POST, PUT, PATCH, DELETE; path: "/api/v1/crm/deals" or "crm/deals"
    callMethod(method, path, params, fn) {
      if (typeof params === 'function') { fn = params; params = undefined; }
      return cb(ask('call', { method, path, params }), fn);
    },
    placement: {
      info: () => (data ? { placement: data.placement, options: data.options } : { placement: '', options: {} }),
    },
    getLang: () => (data ? data.lang : ''),
    getDomain: () => (data ? data.auth.domain : origin.replace(/^https?:\/\//, '')),
    getUserId: () => (data ? data.user_id : ''),
    isAdmin: () => !!(data && data.admin),
    resizeWindow: (width, height, fn) => cb(ask('resize', { width, height }), fn),
    fitWindow: fn => cb(ask('resize', { height: sizes().scrollHeight }), fn),
    getScrollSize: () => sizes(),
    scrollParentWindow: (y, fn) => cb(ask('scroll', { y }), fn),
    setTitle: (title, fn) => cb(ask('setTitle', { title }), fn),
    openPath: (path, fn) => cb(ask('openPath', { path }), fn),
    reloadWindow: fn => cb(ask('reload'), fn),
    closeApplication: fn => cb(ask('close'), fn),
    selectUser: fn => cb(ask('selectUsers', { multiple: false }).then(l => l[0] || null), fn),
    selectUsers: fn => cb(ask('selectUsers', { multiple: true }).then(l => (l.length ? l : null)), fn),
    appOption: option('app_options', '/api/v1/app/options'),
    userOption: option('user_options', '/api/v1/app/user-options'),
  };
})();
