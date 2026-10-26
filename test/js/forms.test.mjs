// Background POST forms in jsdom. node test/js/forms.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/forms.js', import.meta.url), 'utf8');
// any POST form: a mistake keeps everything typed and shows the message; success follows the redirect
{
  const dom = new JSDOM(`<!doctype html><html data-failed="Не удалось"><body><form method="post" action="/projects">
    <input name="name" value="Проект"><input name="key" value="1x"><textarea name="description">длинное описание</textarea>
    <button>Создать</button></form></body></html>`, { runScripts: 'outside-only', url: 'http://go24.test/projects' });
  const w = dom.window;
  let answer = { status: 422, body: { error: 'Ключ проекта: 2–10 латинских букв' } }, sent = null;
  w.fetch = async (url, opts) => { sent = { url: String(url), body: String(opts.body), head: opts.headers['X-Go24-Form'] };
    return { ok: answer.status < 300, status: answer.status, headers: { get: () => 'application/json' }, json: async () => answer.body }; };
  w.eval(script);
  const f = w.document.querySelector('form');
  f.dispatchEvent(new w.Event('submit', { bubbles: true, cancelable: true }));
  await new Promise(r => setTimeout(r, 20));
  assert.equal(sent.head, '1');
  assert.match(sent.body, /key=1x/);
  assert.equal(f.querySelector('.form-error').textContent, 'Ключ проекта: 2–10 латинских букв');
  assert.equal(f.querySelector('textarea').value, 'длинное описание', 'typed text kept');
  assert.equal(f.querySelector('button').disabled, false);
}
// the button pressed is sent with its name and value (mail's toolbar: do=delete), and the fields of the form that
// sit outside it (form="…": the ticked letters)
{
  const dom = new JSDOM(`<!doctype html><body><form method="post" action="/mail/act" id="act"><button name="do" value="read">r</button>
    <button name="do" value="delete">d</button></form><input type="checkbox" name="letter" value="m1" form="act" checked></body>`,
    { runScripts: 'outside-only', url: 'http://go24.test/mail' });
  const w = dom.window;
  let sent = null;
  let release;
  w.fetch = async (url, opts) => { sent = String(opts.body); await new Promise(r => { release = r; });
    return { ok: false, status: 500, headers: { get: () => 'application/json' }, json: async () => ({ error: 'x' }) }; };
  w.eval(script);
  const f = w.document.querySelector('form');
  const del = f.querySelectorAll('button')[1];
  f.dispatchEvent(new w.SubmitEvent('submit', { bubbles: true, cancelable: true, submitter: del }));
  await new Promise(r => setTimeout(r, 20));
  assert.equal(sent, 'do=delete&letter=m1');
  // while the server works: the running line, the pressed button spins, all are disabled
  assert.ok(w.document.documentElement.classList.contains('go24-busy'));
  assert.ok(del.classList.contains('busy') && del.disabled && f.querySelector('button').disabled);
  release();
  await new Promise(r => setTimeout(r, 20));
  assert.ok(!w.document.documentElement.classList.contains('go24-busy') && !del.classList.contains('busy') && !del.disabled);
}
// [data-copy] on plain http: no navigator.clipboard — the text goes through a hidden field and execCommand('copy'),
// the button turns «copied» with the hint; on https navigator.clipboard is used
{
  const dom = new JSDOM(`<!doctype html><html data-copied="Скопировано"><body><div class="row"><input readonly value="http://go24.test/join/abc">
    <button type="button" data-copy="http://go24.test/join/abc" title="Копировать"><svg></svg></button></div></body></html>`,
    { runScripts: 'outside-only', url: 'http://go24.test/p/COL' });
  const w = dom.window;
  let copied = null;
  w.document.execCommand = cmd => { copied = cmd === 'copy' ? w.document.activeElement.value : null; return true; };
  w.eval(script);
  const b = w.document.querySelector('[data-copy]');
  b.querySelector('svg').dispatchEvent(new w.MouseEvent('click', { bubbles: true }));
  await new Promise(r => setTimeout(r, 20));
  assert.equal(copied, 'http://go24.test/join/abc');
  assert.ok(b.classList.contains('copied'));
  assert.equal(b.dataset.tip, 'Скопировано');
  assert.equal(w.document.querySelector('textarea'), null, 'the hidden field is removed');

  let viaApi = null;
  Object.defineProperty(w.navigator, 'clipboard', { value: { writeText: async t => { viaApi = t; } } });
  copied = null;
  b.click();
  await new Promise(r => setTimeout(r, 20));
  assert.equal(viaApi, 'http://go24.test/join/abc');
  assert.equal(copied, null);
  await new Promise(r => setTimeout(r, 1600));
  assert.equal(b.title, 'Копировать', 'the title comes back');
}
// a button with data-confirm asks before its form goes (mail's «Игнорировать беседу»); the others of the form do not
{
  const dom = new JSDOM(`<!doctype html><body><form method="post" action="/mail/act"><button name="do" value="read">r</button>
    <button name="do" value="ignore" data-confirm="Игнорировать?">i</button></form></body>`, { runScripts: 'outside-only', url: 'http://go24.test/mail' });
  const w = dom.window;
  const asked = [];
  let yes = false, sent = 0;
  w.confirm = msg => { asked.push(msg); return yes; };
  w.fetch = async () => { sent++; return { ok: true, status: 200, redirected: false, url: 'http://go24.test/mail', headers: { get: () => 'text/html' }, text: async () => '' }; };
  w.eval(script);
  const f = w.document.querySelector('form');
  const [read, ignore] = f.querySelectorAll('button');
  const press = b => { const e = new w.SubmitEvent('submit', { bubbles: true, cancelable: true, submitter: b }); f.dispatchEvent(e); return e.defaultPrevented; };
  assert.equal(press(ignore), true);
  assert.deepEqual(asked, ['Игнорировать?']);
  await new Promise(r => setTimeout(r, 20));
  assert.equal(sent, 0, 'not sent when not confirmed');
  press(read);
  assert.equal(asked.length, 1, 'another button does not ask');
}
console.log('forms.js: background forms and copy passed');
// go24 restarting behind the proxy (502): sent once more a moment later, and the redirect is followed; a second
// answer go24 did not mean shows its code and is reported to /client-error (GO24DEV-349)
{
  const dom = new JSDOM(`<!doctype html><html data-failed="Не удалось сохранить"><body><form method="post" action="/t/GO-1/action">
    <input type="hidden" name="action" value="start"><button>Начать выполнение</button></form></body></html>`,
    { runScripts: 'outside-only', url: 'http://go24.test/t/GO-1' });
  const w = dom.window;
  w.go24RetryMs = 1;
  const sent = [];
  let answers = [];
  w.fetch = async (url, opts) => {
    sent.push({ url: String(url), body: String(opts.body) });
    const a = answers.shift() || { status: 204, type: '', body: '' };
    return { ok: a.status < 300, status: a.status, headers: { get: () => a.type },
      json: async () => JSON.parse(a.body), text: async () => a.body };
  };
  w.eval(script);
  const f = w.document.querySelector('form');
  const bad = { status: 502, type: 'text/html', body: '<h1>502 Bad Gateway</h1>' };
  answers = [bad, bad];
  f.dispatchEvent(new w.Event('submit', { bubbles: true, cancelable: true }));
  await new Promise(r => setTimeout(r, 30));
  assert.deepEqual(sent.map(s => s.url), ['http://go24.test/t/GO-1/action', 'http://go24.test/t/GO-1/action', '/client-error']);
  assert.equal(f.querySelector('.form-error').textContent, 'Не удалось сохранить (502)');
  const report = new URLSearchParams(sent[2].body);
  assert.equal(report.get('status'), '502');
  assert.equal(report.get('url'), '/t/GO-1/action');
  assert.equal(report.get('page'), '/t/GO-1');
  assert.equal(report.get('body'), '<h1>502 Bad Gateway</h1>');

  // a JSON error of go24 is shown as it is and not reported
  sent.length = 0;
  answers = [{ status: 422, type: 'application/json', body: '{"error":"Действие недоступно"}' }];
  f.dispatchEvent(new w.Event('submit', { bubbles: true, cancelable: true }));
  await new Promise(r => setTimeout(r, 30));
  assert.equal(sent.length, 1);
  assert.equal(f.querySelector('.form-error').textContent, 'Действие недоступно');
}
console.log('forms 349: ok');
// a field or a button named «action» (the task's «Начать выполнение», the work day's buttons) or «target» does not
// stand for the form's address or target: the form is sent to its own action, in the background (GO24DEV-349)
{
  const dom = new JSDOM(`<!doctype html><body>
    <form method="post" action="/t/CL1-1/action" id="a"><input type="hidden" name="action" value="start"><button>Начать выполнение</button></form>
    <form method="post" action="/worktime/day" id="b"><button name="action" value="start">Начать рабочий день</button></form>
    <form method="POST" action="/kb/s/p/move" id="c"><input name="target" value="x"><button>Переместить</button></form>
    <form method="post" action="/booking/item/b1" id="d"><button class="primary">Сохранить</button><button formaction="/booking/item/b1/deal">Создать сделку</button></form></body>`,
    { runScripts: 'outside-only', url: 'http://go24.test/t/CL1-1' });
  const w = dom.window;
  // browsers let a named field or button stand for form.action / form.target / form.method; jsdom does not — as they do
  for (const prop of ['action', 'target', 'method']) {
    const own = Object.getOwnPropertyDescriptor(w.HTMLFormElement.prototype, prop);
    Object.defineProperty(w.HTMLFormElement.prototype, prop, { configurable: true,
      get() { return this.elements.namedItem(prop) || own.get.call(this); } });
  }
  const sent = [];
  w.fetch = async (url, opts) => { sent.push({ url: String(url), body: String(opts.body) });
    return { ok: true, status: 200, headers: { get: () => 'application/json' }, json: async () => ({ redirect: '/elsewhere' }) }; };
  w.eval(script);
  for (const id of ['a', 'b', 'c', 'd']) { // d: the pressed button's formaction is where it goes
    const f = w.document.getElementById(id);
    const ev = new w.Event('submit', { bubbles: true, cancelable: true });
    Object.defineProperty(ev, 'submitter', { value: f.querySelector(id === 'd' ? 'button[formaction]' : 'button') });
    f.dispatchEvent(ev);
    assert.ok(ev.defaultPrevented, id + ' is sent in the background');
  }
  await new Promise(r => setTimeout(r, 20));
  assert.deepEqual(sent.map(s => s.url), ['http://go24.test/t/CL1-1/action', 'http://go24.test/worktime/day', 'http://go24.test/kb/s/p/move',
    'http://go24.test/booking/item/b1/deal']);
  assert.equal(sent[0].body, 'action=start');
  assert.equal(sent[1].body, 'action=start');
}
console.log('forms action field: ok');
// an application's tab in a card: its frame gets the address only when the tab opens, once
{
  const dom = new JSDOM(`<!doctype html><body><details data-app-tab><summary>Доставка</summary>
    <iframe data-src="/apps/tab/p1?id=d1"></iframe></details></body>`, { runScripts: 'outside-only', url: 'http://go24.test/crm/deals/d1' });
  const w = dom.window;
  w.eval(script);
  const d = w.document.querySelector('details'), f = d.querySelector('iframe');
  assert.equal(f.getAttribute('src'), null, 'closed: nothing loads');
  d.open = true;
  d.dispatchEvent(new w.Event('toggle'));
  assert.equal(f.getAttribute('src'), '/apps/tab/p1?id=d1');
  f.setAttribute('src', '/apps/tab/p1?id=d1&x');
  d.dispatchEvent(new w.Event('toggle'));
  assert.equal(f.getAttribute('src'), '/apps/tab/p1?id=d1&x', 'opened again: the frame stays');
}
console.log('forms app tab: ok');
