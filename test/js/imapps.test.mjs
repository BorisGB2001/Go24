// The widgets of applications in the messenger (static/js/im.js, apphost.js) in jsdom: IM_SIDEBAR
// opens the application's frame in the side panel with the chat, IM_CONTEXT_MENU goes into a message's «…» and opens
// it with the message, IM_TEXTAREA opens over the input field in its size and closes by its button again, the
// application's closeApplication closes its panel; each frame is named for go24.js and asks for its chat's tokens.
// node test/js/imapps.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const read = f => readFileSync(new URL('../../internal/web/static/js/' + f, import.meta.url), 'utf8');
const tick = () => new Promise(r => setTimeout(r, 20));
const msg = `<div class="msg" data-id="$m"><div class="bubble"><details class="im-menu"><summary>…</summary><div class="pop">
  <button type="button" data-act="reply">Ответить</button><a href="/tasks/new" data-act-link>Задача</a><a href="/feed" data-act-link>Пост</a>
  <button type="button" data-act="pin">Закрепить</button></div></details><div class="text">Привет</div></div></div>`;
const dom = new JSDOM(`<!doctype html><body><div class="im" data-im data-room="!r" data-failed="Не удалось">
  <section class="im-chat"><div class="im-head"><button type="button" class="ghost" data-im-app="s1" data-place="IM_SIDEBAR" data-title="База знаний">i</button></div>
    <div class="im-msgs" data-msgs></div>
    <div class="im-apppop" data-app-pop hidden><b data-app-title></b><button type="button" data-app-close>x</button><div data-app-box></div></div>
    <template data-ctx-apps><button type="button" class="im-ctxapp" data-im-app="c1" data-place="IM_CONTEXT_MENU" data-title="В CRM">В CRM</button></template>
    <form class="chatform" data-send><button type="button" class="ghost attach" data-im-app="t1" data-place="IM_TEXTAREA" data-title="Шаблоны" data-w="250" data-h="120">t</button>
      <textarea name="body"></textarea><button>s</button></form></section>
  <aside data-thread-pane hidden><div data-thread-msgs></div></aside>
  <aside class="im-thread im-apppane" data-app-pane hidden><b data-app-title></b><button type="button" data-app-close>x</button><div data-app-box></div></aside>
  </div></body>`, { runScripts: 'outside-only', url: 'http://go24.test/im?room=!r', pretendToBeVisual: true });
const w = dom.window, d = w.document;
w.CSS = { escape: s => s };
w.HTMLElement.prototype.scrollIntoView = () => {};
w.fetch = async url => {
  if (url.startsWith('/im/messages')) return { ok: true, json: async () => ({ html: msg }) };
  return { ok: true, status: 204, json: async () => ({}) };
};
w.EventSource = class { constructor() { this.h = {}; } addEventListener(t, f) { this.h[t] = f; } close() {} };
w.eval(read('apphost.js'));
w.eval(read('im.js'));
await tick();
const $ = s => d.querySelector(s);
const pane = $('[data-app-pane]'), pop = $('[data-app-pop]');

// IM_SIDEBAR: the side panel with the frame of the chat
$('[data-im-app="s1"]').click();
let f = pane.querySelector('iframe');
assert.equal(pane.hidden, false);
assert.equal(pane.querySelector('[data-app-title]').textContent, 'База знаний');
assert.equal(f.dataset.tab, 's1');
assert.equal(f.dataset.ctx, 'room=%21r');
assert.equal(f.getAttribute('src'), '/apps/tab/s1?room=%21r');
assert.match(f.name, /^go24\|http:\/\/go24\.test\|\d+$/);

// IM_CONTEXT_MENU: after the last «Создать …» of the message's «…», opened with the message
const menu = $('.msg .im-menu');
menu.open = true;
menu.dispatchEvent(new w.Event('toggle'));
const links = [...menu.querySelectorAll('.pop > *')].map(e => e.dataset.imApp || e.dataset.act || e.textContent);
assert.deepEqual(links, ['reply', 'Задача', 'Пост', 'c1', 'pin']);
menu.dispatchEvent(new w.Event('toggle'));
assert.equal(menu.querySelectorAll('[data-im-app]').length, 1, 'the item is put once');
menu.querySelector('[data-im-app="c1"]').click();
f = pane.querySelector('iframe');
assert.equal(menu.open, false);
assert.equal(pane.querySelectorAll('iframe').length, 1, 'a new frame instead of the old one');
assert.equal(f.getAttribute('src'), '/apps/tab/c1?room=%21r&msg=%24m');
assert.equal(pane.querySelector('[data-app-title]').textContent, 'В CRM');

// closeApplication of the application: its panel closes, the frame goes
f.dispatchEvent(new w.CustomEvent('app-close', { bubbles: true }));
assert.equal(pane.hidden, true);
assert.equal(pane.querySelector('iframe'), null);

// IM_TEXTAREA: over the input field in its size; its button again closes it
$('[data-im-app="t1"]').click();
f = pop.querySelector('iframe');
assert.equal(pop.hidden, false);
assert.equal(f.style.width, '250px');
assert.equal(f.style.height, '120px');
assert.equal(pane.hidden, true, 'the side panel stays as it was');
$('[data-im-app="t1"]').click();
assert.equal(pop.hidden, true);
$('[data-im-app="t1"]').click();
pop.querySelector('[data-app-close]').click();
assert.equal(pop.hidden, true);
console.log('imapps.test.mjs: ok');
