// «Видеовстреча через …» (static/js/callext.js) in jsdom: hidden while no service is there; the list of services
// once per page; in a task the meeting is made by go24 (the invitation goes into the task) and opens in the window
// opened by the click; in a form the link goes into its field (after its text), a CRM activity becomes a meeting;
// a refusal is told and the window closed.
// node test/js/callext.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const js = readFileSync(new URL('../../internal/web/static/js/callext.js', import.meta.url), 'utf8');
const tick = () => new Promise(r => setTimeout(r, 20));
const box = (q, fill) => `<details data-call-ext="${q}"${fill ? ` data-fill="${fill}"` : ''} hidden><summary title="Видеовстреча через …">Встреча</summary><div data-call-ext-list></div></details>`;
const page = services => {
  const dom = new JSDOM(`<!doctype html><body>
    <div id="task">${box('task=CE-1')}</div>
    <form id="act"><input type="radio" name="kind" value="call" checked><input type="radio" name="kind" value="meeting">
      <input name="subject" value="Встреча с клиентом"><textarea name="description">Обсудить договор</textarea>${box('', 'description')}</form>
    <form id="ev"><input name="where" value="">${box('', 'where')}</form></body>`, { runScripts: 'outside-only', url: 'https://go24.test/t/CE-1' });
  const w = dom.window;
  const t = { requests: [], opened: [], alerts: [], answer: { url: 'https://telemost.yandex.ru/j/1' }, ok: true, w };
  w.fetch = async (url, opts = {}) => {
    t.requests.push([opts.method || 'GET', url, opts.body ? String(opts.body) : '']);
    if ((opts.method || 'GET') === 'GET') return { ok: true, json: async () => ({ services }) };
    return { ok: t.ok, json: async () => t.answer };
  };
  w.open = () => { const win = { location: '', closed: false, close() { this.closed = true; } }; t.opened.push(win); return win; };
  w.alert = m => t.alerts.push(m);
  w.eval(js);
  t.$ = s => w.document.querySelector(s);
  return t;
};

const none = page([]);
await tick();
assert.equal(none.$('#task [data-call-ext]').hidden, true);

const t = page([{ service: 'telemost', name: 'Яндекс Телемост', api: true }, { service: 'telegram', name: 'Telegram', api: false }]);
await tick();
assert.equal(t.requests.length, 1, 'the services are asked once');
assert.equal(t.$('#task [data-call-ext]').hidden, false);
assert.deepEqual([...t.$('#task [data-call-ext-list]').children].map(b => b.textContent), ['Яндекс Телемост', 'Telegram']);

// in a task: go24 makes the meeting and tells the task; it opens in the new window
t.$('#task [data-call-ext-service="telemost"]').click();
await tick();
assert.deepEqual(t.requests.at(-1), ['POST', '/call/external?task=CE-1', 'service=telemost']);
assert.equal(t.opened.length, 1);
assert.equal(t.opened[0].location, 'https://telemost.yandex.ru/j/1');

// a CRM activity: the link after its text, the kind «meeting», the subject as the meeting's title; no window
t.answer = { url: 'https://t.me/anna' };
t.$('#act [data-call-ext-service="telegram"]').click();
await tick();
assert.deepEqual(t.requests.at(-1), ['POST', '/call/external', new URLSearchParams({ service: 'telegram', title: 'Встреча с клиентом' }).toString()]);
assert.equal(t.$('#act textarea').value, 'Обсудить договор\nhttps://t.me/anna');
assert.equal(t.$('#act input[value="meeting"]').checked, true);
assert.equal(t.opened.length, 1);

// the place of a calendar meeting: an empty field gets the link
t.$('#ev [data-call-ext-service="telemost"]').click();
await tick();
assert.equal(t.$('#ev input[name="where"]').value, 'https://t.me/anna');

// refused: told, the window closed
t.ok = false;
t.answer = { error: 'Сервис недоступен' };
t.$('#task [data-call-ext-service="telegram"]').click();
await tick();
assert.deepEqual(t.alerts, ['Сервис недоступен']);
assert.equal(t.opened.at(-1).closed, true);
console.log('callext: ok');
