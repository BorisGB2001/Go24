// The menu of a message in the messenger (static/js/im.js) in jsdom: reply sends the quote, edit puts the text in
// the field and replaces the message, delete asks and removes it, forward sends it to the chat picked; a live
// change edits or removes a message on the screen; a formatted message is edited as typed (its BB codes), Ctrl+B
// wraps the selection in [b]. node test/js/immenu.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const im = readFileSync(new URL('../../internal/web/static/js/im.js', import.meta.url), 'utf8');
const tick = () => new Promise(r => setTimeout(r, 20));
const msg = (id, who, text, raw) => `<div class="msg" data-id="${id}"><div class="bubble"><b class="who">${who}</b>
  <details class="im-menu"><summary>…</summary><div class="pop"><button data-act="reply">r</button><button data-act="edit">e</button>
  <button data-act="delete">d</button><button data-act="link">l</button><button data-act="forward">f</button></div></details><div class="text"${raw ? ` data-body="${raw}"` : ''}>${text}</div></div></div>`;
const dom = new JSDOM(`<!doctype html><body><div class="im" data-im data-room="!r" data-failed="Не удалось" data-delete-confirm="Удалить?"
  data-edit-hint="Изменение" data-reply-hint="Ответ на">
  <nav data-list><a class="im-room" data-room-id="!r" data-name="Мы"><span data-preview></span><span data-unread hidden></span></a>
    <a class="im-room" data-room-id="!other" data-name="Другой чат"><span data-preview></span><span data-unread hidden></span></a></nav>
  <section class="im-chat"><div class="im-msgs" data-msgs></div>
    <div data-replying hidden><span data-replying-text></span><button data-reply-cancel>x</button></div>
    <dialog data-forward-pick><form method="dialog"><input data-forward-filter><div data-forward-list></div></form></dialog>
    <form class="chatform" data-send><textarea name="body"></textarea><button>s</button></form></section>
  <aside data-thread-pane hidden><div data-thread-msgs></div><form data-send-thread><textarea name="body"></textarea></form></aside>
  </div></body>`, { runScripts: 'outside-only', url: 'http://go24.test/im?room=!r', pretendToBeVisual: true });
const w = dom.window, d = w.document;
w.CSS = { escape: s => s };
w.HTMLElement.prototype.scrollIntoView = () => {};
w.HTMLDialogElement.prototype.showModal = function () { this.setAttribute('open', ''); };
w.HTMLDialogElement.prototype.close = function () { this.removeAttribute('open'); };
const posts = [];
w.fetch = async (url, opts = {}) => {
  if (url.startsWith('/im/messages')) return { ok: true, json: async () => ({ html: msg('$a', 'Анна', 'привет') + msg('$b', '', 'моё')
    + msg('$c', '', '<b>жирное</b> и <a href="https://x.ru">https://x.ru</a>', '*жирное* и https://x.ru') }) };
  const p = Object.fromEntries(new URLSearchParams(String(opts.body)));
  posts.push([url, p]);
  if (url === '/im/send') return { ok: true, json: async () => ({ id: '$n', html: msg('$n', '', p.body) }) };
  if (url === '/im/edit') return { ok: true, json: async () => ({ id: p.event, html: msg(p.event, '', p.body + ' (изм.)') }) };
  if (url === '/im/forward') return { ok: true, status: 201, json: async () => ({ id: '$f', room: p.to }) };
  return { ok: true, json: async () => ({}) };
};
let confirmed = true;
w.confirm = () => confirmed;
const streams = [];
w.EventSource = class { constructor() { this.h = {}; streams.push(this); } addEventListener(t, f) { this.h[t] = f; } close() {} };
w.eval(im);
await tick();
const $ = s => d.querySelector(s);
const text = $('[data-send] textarea');
const act = (id, a) => $(`[data-id="${id}"] [data-act="${a}"]`).click();

// reply: the line above the field names the message; the send carries the quote, then the mode is over
act('$a', 'reply');
assert.equal($('[data-replying]').hidden, false);
assert.match($('[data-replying-text]').textContent, /Ответ на Анна: привет/);
text.value = 'и тебе';
$('[data-send]').requestSubmit();
await tick();
assert.deepEqual(posts.at(-1), ['/im/send', { room: '!r', body: 'и тебе', reply: '$a' }]);
assert.equal($('[data-replying]').hidden, true);

// edit: the text goes into the field, saving replaces the message in place
act('$b', 'edit');
assert.equal(text.value, 'моё');
text.value = 'моё, исправлено';
$('[data-send]').requestSubmit();
await tick();
assert.deepEqual(posts.at(-1), ['/im/edit', { room: '!r', event: '$b', body: 'моё, исправлено' }]);
assert.match($('[data-id="$b"] .text').textContent, /исправлено \(изм\.\)/);
assert.equal(d.querySelectorAll('[data-id="$b"]').length, 1);

// «Копировать ссылку»: the chat opened at the message
const copied = [];
w.go24Copy = t => copied.push(t);
act('$a', 'link');
assert.equal(copied.at(-1), 'http://go24.test/im?room=%21r&at=%24a');

// a formatted message: the field gets the text as it was typed, not as it is shown
act('$c', 'edit');
assert.equal(text.value, '*жирное* и https://x.ru');
$('[data-send]').reset();
$('[data-reply-cancel]').click();

// Ctrl+B / Ctrl+Shift+X: the selection in [b] / [s]; with nothing selected the caret goes between the tags
text.value = 'очень важно';
text.setSelectionRange(6, 11);
text.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'b', ctrlKey: true, bubbles: true }));
assert.equal(text.value, 'очень [b]важно[/b]');
text.value = 'x';
text.setSelectionRange(1, 1);
text.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'X', ctrlKey: true, shiftKey: true, bubbles: true }));
assert.equal(text.value, 'x[s][/s]');
assert.equal(text.selectionStart, 4);
text.value = '';

// the «…» opens over everything, whole in the window: by its button's right edge, moved in from the window's edge,
// above the button near the window's bottom; scrolling the chat closes it
const open = (id, at, size) => {
  const dm = $(`[data-id="${id}"] .im-menu`), pop = dm.querySelector('.pop');
  dm.querySelector('summary').getBoundingClientRect = () => ({ top: at.top, bottom: at.top + 20, left: at.right - 20, right: at.right });
  pop.getBoundingClientRect = () => { const h = Math.min(size.h, parseFloat(pop.style.maxHeight) || Infinity); return { width: size.w, height: h }; };
  dm.open = true;
  dm.dispatchEvent(new w.Event('toggle'));
  return pop;
};
Object.defineProperty(d.documentElement, 'clientWidth', { value: 1000, configurable: true });
w.innerHeight = 800;
let pop = open('$a', { top: 100, right: 150 }, { w: 300, h: 400 });
assert.ok(pop.classList.contains('fixed'));
assert.deepEqual([pop.style.left, pop.style.top], ['8px', '120px']);
pop = open('$a', { top: 600, right: 900 }, { w: 300, h: 400 });
assert.deepEqual([pop.style.left, pop.style.top], ['600px', '200px']);
pop = open('$a', { top: 300, right: 900 }, { w: 300, h: 600 });
assert.deepEqual([pop.style.maxHeight, pop.style.top], ['472px', '320px']);
$('[data-msgs]').dispatchEvent(new w.Event('scroll'));
assert.equal($('[data-id="$a"] .im-menu').open, false);

// delete: refused at the question — nothing; agreed — removed
confirmed = false;
act('$b', 'delete');
await tick();
assert.ok($('[data-id="$b"]'));
confirmed = true;
act('$b', 'delete');
await tick();
assert.deepEqual(posts.at(-1), ['/im/delete', { room: '!r', event: '$b' }]);
assert.equal($('[data-id="$b"]'), null);

// forward: the chats to pick from, filtered by name; the pick sends it there
act('$a', 'forward');
assert.ok($('[data-forward-pick]').hasAttribute('open'));
assert.equal(d.querySelectorAll('[data-to]').length, 2);
const f = $('[data-forward-filter]');
f.value = 'друг'; f.dispatchEvent(new w.Event('input'));
assert.deepEqual([...d.querySelectorAll('[data-to]')].map(o => o.hidden), [true, false]);
$('[data-to="!other"]').click();
await tick();
assert.deepEqual(posts.at(-1), ['/im/forward', { room: '!r', event: '$a', to: '!other' }]);
assert.ok(!$('[data-forward-pick]').hasAttribute('open'));

// live: someone's edit and deletion reach the open chat
const es = streams.find(s => s.h.change);
es.h.change({ data: JSON.stringify({ id: '$a', room: '!r', change: 'edit', body: 'строка 1\nстрока 2' }) });
assert.equal($('[data-id="$a"] .text').innerHTML, 'строка 1<br>строка 2');
es.h.change({ data: JSON.stringify({ id: '$a', room: '!r', change: 'edit', body: '[b]да[/b]', text: '<b>да</b>' }) });
assert.equal($('[data-id="$a"] .text').innerHTML, '<b>да</b>');
assert.equal($('[data-id="$a"] .text').dataset.body, '[b]да[/b]');
es.h.change({ data: JSON.stringify({ id: '$a', room: '!r', change: 'delete' }) });
assert.equal($('[data-id="$a"]'), null);
console.log('im.js: the message menu passed');
process.exit(0);
