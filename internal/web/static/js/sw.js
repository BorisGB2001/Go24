// The service worker of the installable go24 (served as /sw.js by pwa.go, which puts GO24_VERSION, the build's
// version, and GO24_TITLE, the portal's name, before this file). It keeps the static files (their URLs carry the version, so a kept one never goes stale)
// and the «Нет связи» page: a page that can't be loaded without the network shows that page instead of the browser's
// error. Pages and the API always go to the server — nothing of the user's data is kept on the phone.
const CACHE = 'go24-' + GO24_VERSION;
const OFFLINE = '/offline';

self.addEventListener('install', (e) => {
  e.waitUntil(caches.open(CACHE).then((c) => c.add(new Request(OFFLINE, { cache: 'reload' }))).then(() => self.skipWaiting()));
});

self.addEventListener('activate', (e) => {
  e.waitUntil(caches.keys()
    .then((keys) => Promise.all(keys.filter((k) => k.startsWith('go24-') && k !== CACHE).map((k) => caches.delete(k))))
    .then(() => self.clients.claim()));
});

self.addEventListener('fetch', (e) => {
  const req = e.request;
  if (req.method !== 'GET') return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;
  if (req.mode === 'navigate') {
    e.respondWith(fetch(req).catch(() => caches.match(OFFLINE)));
    return;
  }
  if (url.pathname.startsWith('/static/') && url.searchParams.has('v')) {
    e.respondWith(caches.open(CACHE).then((c) => c.match(req).then((hit) => hit || fetch(req).then((res) => {
      if (res.ok) c.put(req, res.clone());
      return res;
    }))));
  }
});

// Push notifications (GO24DEV-743): go24-worker sends {body, url, tag} through the browser's push service; the
// notification shows with the portal's name (GO24_TITLE) and a tap opens its page — in a go24 tab already open, if any.
self.addEventListener('push', (e) => {
  let m = {};
  try { m = e.data ? e.data.json() : {}; } catch { m = { body: e.data.text() }; }
  e.waitUntil(self.registration.showNotification(GO24_TITLE, {
    body: m.body || '',
    tag: m.tag || undefined,
    icon: '/static/img/pwa/icon-192.png',
    badge: '/static/img/pwa/icon-96.png',
    data: { url: m.url || '/notifications' },
  }));
});

self.addEventListener('notificationclick', (e) => {
  e.notification.close();
  const url = new URL(e.notification.data && e.notification.data.url || '/notifications', self.location.origin);
  const target = url.origin === self.location.origin ? url.href : new URL(url.pathname + url.search + url.hash, self.location.origin).href;
  e.waitUntil(self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then((list) => {
    const tab = list.find((c) => new URL(c.url).origin === self.location.origin && 'navigate' in c);
    if (tab) return tab.navigate(target).then((c) => (c || tab).focus());
    return self.clients.openWindow(target);
  }));
});
