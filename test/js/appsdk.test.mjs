// go24.js in an application's frame and go24's page around it (static/js/apphost.js) in jsdom, as BX24.js in
// the portal: init gives the tokens, the place and the settings; REST goes with the application's token; the frame
// is resized, the people picker answers; messages from another host get nothing. node test/js/appsdk.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const read = p => readFileSync(new URL('../../internal/web/' + p, import.meta.url), 'utf8');
const plain = x => JSON.parse(JSON.stringify(x)); // objects of the frame's realm
const GO24 = 'https://go24.test', APP = 'https://app.test';

// go24's page: the application's frame, the form of its tokens and the picker
const host = new JSDOM(`<!doctype html><body><div class="app-frame"><iframe data-app="a1" data-placement="DEFAULT" data-options="{}"></iframe>
  <form method="post" action="${APP}/page" data-app-post hidden></form></div>
  <dialog data-app-select><form method="dialog"><h3 data-title-one="Сотрудник" data-title-many="Сотрудники"></h3>
  <div data-one hidden><div data-upick><input class="upick-q"></div></div><div data-many hidden><div data-upick data-multiple><input class="upick-q"></div></div></form></dialog>
  </body>`, { runScripts: 'outside-only', url: GO24 + '/apps/a1', pretendToBeVisual: true });
const hw = host.window;
let posted = null;
hw.HTMLFormElement.prototype.submit = function () { posted = this.target; };
hw.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
hw.HTMLDialogElement.prototype.close = function (v) { this.open = false; this.returnValue = v ?? this.returnValue; this.dispatchEvent(new hw.Event('close')); };
const calls = [];
let issued = 0;
hw.fetch = async (url, opts = {}) => {
  const u = new URL(String(url), GO24);
  calls.push({ path: u.pathname + u.search, method: opts.method || 'GET', auth: opts.headers?.Authorization, body: opts.body, credentials: opts.credentials });
  if (u.pathname === '/apps/frame') {
    issued++;
    return new Response(JSON.stringify({ origins: [APP], title: 'Доставка', app: 'a1', placement: 'DEFAULT', options: {}, lang: 'ru',
      user_id: 'u1', admin: true, app_options: { mode: 'fast' }, user_options: {},
      auth: { access_token: 'go24at_' + issued, refresh_token: 'go24rt_' + issued, expires_in: 3600, domain: 'go24.test', member_id: 'm1' } }), { status: 200 });
  }
  if (u.pathname === '/api/v1/crm/deals') return new Response('[{"id":"d1"}]', { status: 200 });
  if (u.pathname === '/api/v1/app/user-options') return new Response(opts.body, { status: 200 });
  return new Response('{"error":"нет"}', { status: 404 });
};
hw.eval(read('static/js/apphost.js'));
const frame = hw.document.querySelector('iframe');
assert.equal(frame.name, 'go24|' + GO24 + '|1', 'the frame is named with go24\'s address');
assert.equal(posted, frame.name, 'the tokens are posted into the frame');

// the application's page inside: its parent is go24's page, messages travel between the two
const app = new JSDOM('<!doctype html><body><div style="height:900px">Расчёт</div></body>', { runScripts: 'outside-only', url: APP + '/page' });
const aw = app.window;
aw.name = frame.name;
const parent = { postMessage: (data, target) => {
  assert.equal(target, GO24, 'go24.js speaks only to go24');
  hw.dispatchEvent(new hw.MessageEvent('message', { data, origin: from, source: frame.contentWindow }));
} };
let from = APP;
Object.defineProperty(aw, 'parent', { value: parent });
frame.contentWindow.postMessage = (data, target) => {
  assert.equal(target, from, 'go24 answers on the frame\'s host');
  aw.dispatchEvent(new aw.MessageEvent('message', { data, origin: GO24, source: parent }));
};
aw.eval(read('widget/go24.js'));
const go24 = aw.go24;

await go24.init();
assert.deepEqual(plain(go24.getAuth()), { access_token: 'go24at_1', refresh_token: 'go24rt_1', expires_in: 3600, domain: 'go24.test', member_id: 'm1' });
assert.deepEqual(plain(go24.placement.info()), { placement: 'DEFAULT', options: {} });
assert.equal(go24.getLang(), 'ru');
assert.equal(go24.isAdmin(), true);
assert.equal(go24.appOption.get('mode'), 'fast');

// REST with the application's token, not the employee's cookie
const deals = await go24.callMethod('GET', 'crm/deals', { stage: 'new' });
assert.deepEqual(plain(deals), { status: 200, data: [{ id: 'd1' }], error: null });
const last = calls.at(-1);
assert.equal(last.path, '/api/v1/crm/deals?stage=new');
assert.equal(last.auth, 'Bearer go24at_1');
assert.equal(last.credentials, 'omit');
assert.equal((await go24.callMethod('GET', '/api/v1/nothing')).error, 'нет');
await assert.rejects(go24.callMethod('GET', 'https://evil.test/api/v1/x'), /path/, 'only go24\'s REST');
await assert.rejects(go24.callMethod('GET', '/settings/apps'), /path/);

// the employee's settings
assert.deepEqual(plain(await go24.userOption.set('tab', 'map')), { tab: 'map' });
assert.equal(go24.userOption.get('tab'), 'map');

// the frame's size, and the callback besides the Promise
let got = null;
await go24.resizeWindow(0, 640, r => { got = r; });
assert.equal(frame.style.height, '640px');
assert.ok(got);
await assert.rejects(go24.openPath('https://evil.test/'), /path/);

// refreshAuth gives new tokens
assert.equal((await go24.refreshAuth()).access_token, 'go24at_2');
assert.equal(go24.getAuth().access_token, 'go24at_2');

// selectUsers: the chosen people, cancelled — null
const dlg = hw.document.querySelector('dialog');
const chosen = go24.selectUsers();
await new Promise(r => setTimeout(r, 10));
assert.equal(dlg.open, true);
assert.equal(dlg.querySelector('h3').textContent, 'Сотрудники');
assert.equal(dlg.querySelector('div[data-many]').hidden, false);
dlg.querySelector('div[data-many] [data-upick]').insertAdjacentHTML('afterbegin',
  '<span class="upick-chip" data-id="u2"><span class="upick-name">Анна</span></span><span class="upick-chip" data-id="u3"><span class="upick-name">Иван</span></span>');
dlg.close('ok');
assert.deepEqual(plain(await chosen), [{ id: 'u2', name: 'Анна' }, { id: 'u3', name: 'Иван' }]);
const one = go24.selectUser();
await new Promise(r => setTimeout(r, 10));
assert.equal(dlg.querySelectorAll('.upick-chip').length, 2, 'the other picker keeps its chips until it opens');
dlg.close('');
assert.equal(await one, null);

// the frame went to another site: nothing answers it
from = 'https://evil.test';
const before = calls.length;
let answered = false;
go24.callMethod('GET', 'crm/deals').then(() => { answered = true; }, () => { answered = true; });
await new Promise(r => setTimeout(r, 30));
assert.equal(answered, false);
assert.equal(calls.length, before, 'no REST call for a stranger');
console.log('appsdk: go24.js init, REST, settings, size, people picker and the host check passed');
