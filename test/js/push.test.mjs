// Push notifications in jsdom (GO24DEV-743). pwa.js in «Настройки уведомлений»: «Включить на этом устройстве» asks
// the permission, subscribes with the company's key and hands the subscription to go24; a device go24 knows is marked
// «это устройство»; a subscription of another key is dropped first; a browser without push or with notifications
// blocked says so. sw.js: a push shows the notification with the portal's name, a tap opens its page in a go24 tab.
// node test/js/push.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const pwa = readFileSync(new URL('../../internal/web/static/js/pwa.js', import.meta.url), 'utf8');
const sw = readFileSync(new URL('../../internal/web/static/js/sw.js', import.meta.url), 'utf8');
const tick = () => new Promise((r) => setTimeout(r, 20));

const KEY = Buffer.from(Uint8Array.from({ length: 65 }, (_, i) => i + 1)).toString('base64url');
const OTHER = Buffer.from(Uint8Array.from({ length: 65 }, (_, i) => 200 - i)).toString('base64url');
const html = (devices) => `<!doctype html><body><div class="nchan-row" id="ch-push" data-push-key="${KEY}"
  data-push-off="нет push" data-push-denied="запрещено" data-push-fail="не удалось">
  <button type="button" data-push-on hidden>Включить</button><span data-push-here hidden>включено</span>
  <span data-push-state>подсказка</span>
  <ul>${devices.map((e) => `<li data-push-endpoint="${e}"><span data-push-this hidden>это</span></li>`).join('')}</ul></div></body>`;

// page runs pwa.js with a browser whose push subscription is sub (null — none) and the permission perm.
const page = async ({ devices = [], sub = null, perm = 'default', grant = 'granted', push = true, secure = true } = {}) => {
  const dom = new JSDOM(html(devices), { runScripts: 'outside-only', url: 'https://go24.example/settings/notifications' });
  const w = dom.window;
  const log = { posted: [], subscribed: [], unsubscribed: 0, reloaded: 0 };
  const made = (endpoint, key) => ({
    endpoint, options: { applicationServerKey: Uint8Array.from(Buffer.from(key, 'base64url')).buffer },
    toJSON: () => ({ endpoint, keys: { p256dh: 'P', auth: 'A' } }),
    unsubscribe: () => { log.unsubscribed++; return Promise.resolve(true); },
  });
  let current = sub && made(sub.endpoint, sub.key);
  const reg = { pushManager: {
    getSubscription: () => Promise.resolve(current),
    subscribe: (o) => { log.subscribed.push(o); current = made('https://fcm.example/new', KEY); return Promise.resolve(current); },
  } };
  Object.defineProperty(w, 'isSecureContext', { value: secure });
  Object.defineProperty(w.navigator, 'serviceWorker', { value: { register: () => Promise.resolve(reg), ready: Promise.resolve(reg) } });
  w.matchMedia = () => ({ matches: false });
  if (push) w.PushManager = function () {};
  w.Notification = { permission: perm, requestPermission: () => { w.Notification.permission = grant; return Promise.resolve(grant); } };
  w.fetch = (url, o) => { log.posted.push([url, Object.fromEntries(o.body)]); return Promise.resolve({ ok: true }); };
  w.fakeLocation = { reload: () => { log.reloaded++; } }; // jsdom's own location.reload cannot be replaced
  w.eval(`((location) => {${pwa}\n})(window.fakeLocation)`);
  await tick();
  const $ = (s) => w.document.querySelector(s);
  return { w, $, log };
};

// a new device: the button subscribes with the company's key and posts the subscription
let p = await page();
assert.equal(p.$('[data-push-on]').hidden, false, 'the button shows');
p.$('[data-push-on]').click();
await tick();
assert.equal(p.log.subscribed.length, 1);
assert.equal(p.log.subscribed[0].userVisibleOnly, true);
assert.deepEqual([...p.log.subscribed[0].applicationServerKey], [...Buffer.from(KEY, 'base64url')], 'subscribed with the company\'s key');
assert.equal(p.log.posted[0][0], '/settings/notifications/push/devices');
assert.equal(p.log.posted[0][1].endpoint, 'https://fcm.example/new');
assert.equal(p.log.posted[0][1].p256dh, 'P');
assert.equal(p.log.reloaded, 1, 'the page shows the new device');

// a device go24 knows: marked, no button
p = await page({ devices: ['https://fcm.example/a', 'https://fcm.example/me'], sub: { endpoint: 'https://fcm.example/me', key: KEY }, perm: 'granted' });
assert.equal(p.$('[data-push-on]').hidden, true);
assert.equal(p.$('[data-push-here]').hidden, false);
assert.equal(p.$('[data-push-endpoint="https://fcm.example/me"] [data-push-this]').hidden, false, '«это устройство»');
assert.equal(p.$('[data-push-endpoint="https://fcm.example/a"] [data-push-this]').hidden, true);

// subscribed with another key (the company's key changed): dropped, subscribes anew
p = await page({ devices: ['https://fcm.example/me'], sub: { endpoint: 'https://fcm.example/me', key: OTHER }, perm: 'granted' });
assert.equal(p.log.unsubscribed, 1, 'the old subscription dropped');
assert.equal(p.$('[data-push-on]').hidden, false);
p.$('[data-push-on]').click();
await tick();
assert.equal(p.log.subscribed.length, 1);

// the person declines: told, nothing posted
p = await page({ grant: 'denied' });
p.$('[data-push-on]').click();
await tick();
assert.equal(p.log.posted.length, 0);
assert.equal(p.$('[data-push-state]').textContent, 'запрещено');
assert.equal(p.$('[data-push-on]').disabled, false);

// blocked before; no push in the browser; not https
p = await page({ perm: 'denied' });
assert.equal(p.$('[data-push-on]').hidden, true);
assert.equal(p.$('[data-push-state]').textContent, 'запрещено');
p = await page({ push: false });
assert.equal(p.$('[data-push-state]').textContent, 'нет push');
p = await page({ secure: false });
assert.equal(p.$('[data-push-state]').textContent, 'нет push');

// sw.js: the push is shown with the portal's name; a tap navigates a go24 tab to the notification
const handlers = {};
const shown = [];
const navigated = [];
const opened = [];
let tabs = [];
const self = {
  location: new URL('https://go24.example/sw.js'),
  addEventListener: (t, f) => { handlers[t] = f; },
  registration: { showNotification: (title, o) => { shown.push([title, o]); return Promise.resolve(); } },
  clients: {
    matchAll: () => Promise.resolve(tabs),
    openWindow: (u) => { opened.push(u); return Promise.resolve(); },
    claim: () => Promise.resolve(),
  },
  skipWaiting: () => Promise.resolve(),
};
new Function('self', 'caches', 'fetch', 'const GO24_VERSION = "t";\nconst GO24_TITLE = "Рога и копыта";\n' + sw)(self, {}, () => {});
const wait = [];
const ev = (extra) => ({ waitUntil: (pr) => wait.push(pr), ...extra });
handlers.push(ev({ data: { json: () => ({ body: 'Борис поставил задачу', url: 'https://go24.example/notifications/7', tag: 'n7' }) } }));
await Promise.all(wait);
assert.equal(shown[0][0], 'Рога и копыта');
assert.equal(shown[0][1].body, 'Борис поставил задачу');
assert.equal(shown[0][1].tag, 'n7');
assert.equal(shown[0][1].data.url, 'https://go24.example/notifications/7');

let closed = 0;
const tab = { url: 'https://go24.example/im', navigate: (u) => { navigated.push(u); return Promise.resolve(tab); }, focus: () => Promise.resolve() };
tabs = [{ url: 'https://other.example/' }, tab];
handlers.notificationclick(ev({ notification: { close: () => closed++, data: { url: 'https://go24.example/notifications/7' } } }));
await Promise.all(wait);
assert.equal(closed, 1);
assert.deepEqual(navigated, ['https://go24.example/notifications/7'], 'the go24 tab opens the notification');
tabs = [];
handlers.notificationclick(ev({ notification: { close: () => closed++, data: { url: 'http://go24.test:8080/notifications/8' } } }));
await Promise.all(wait);
assert.deepEqual(opened, ['https://go24.example/notifications/8'], 'no tab: a new window, on this portal\'s address');

console.log('push.test.mjs: ok');
