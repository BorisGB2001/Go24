// «Выбрать» in the messenger (static/js/im.js) in jsdom: the menu item starts the choice, a click marks or unmarks a
// message, the bar counts them; «Копировать» copies the chosen with author and time, «Переслать» sends them in their
// order to the chat picked, «Удалить» — only one's own; «Отмена» and Esc leave the choice. node test/js/imselect.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const im = readFileSync(new URL('../../internal/web/static/js/im.js', import.meta.url), 'utf8');
const tick = () => new Promise(r => setTimeout(r, 20));
const msg = (id, who, text, mine) => `<div class="msg${mine ? ' mine' : ''}" data-id="${id}" data-sender="${who}" data-when="28.10 12:0${id.length}">
  <div class="bubble"><details class="im-menu"><summary>…</summary><div class="pop"><button data-act="select">s</button>
  ${mine ? '<button data-act="delete">d</button>' : ''}</div></details><div class="text">${text}</div></div></div>`;
const bar = `<div class="im-selbar" data-selbar hidden><b data-sel-count data-fmt="Выбрано: %s"></b><button data-sel="forward">f</button>
  <button data-sel="copy">c</button><button data-sel="delete">d</button><button data-sel="cancel">x</button></div>`;
const dom = new JSDOM(`<!doctype html><body><div class="im" data-im data-room="!r" data-failed="Не удалось" data-delete-confirm="Удалить?">
  <nav data-list><a class="im-room" data-room-id="!r" data-name="Мы"><span data-preview></span><span data-unread hidden></span></a>
    <a class="im-room" data-room-id="!other" data-name="Другой чат"><span data-preview></span><span data-unread hidden></span></a></nav>
  <section class="im-chat"><div class="im-msgs" data-msgs></div>
    <dialog data-forward-pick><form method="dialog"><input data-forward-filter><div data-forward-list></div></form></dialog>
    ${bar}<form class="chatform" data-send><textarea name="body"></textarea><button>s</button></form></section>
  <aside data-thread-pane hidden><div data-thread-msgs></div>${bar}<form data-send-thread><textarea name="body"></textarea></form></aside>
  </div></body>`, { runScripts: 'outside-only', url: 'http://go24.test/im?room=!r', pretendToBeVisual: true });
const w = dom.window, d = w.document;
w.CSS = { escape: s => s };
w.HTMLElement.prototype.scrollIntoView = () => {};
w.HTMLDialogElement.prototype.showModal = function () { this.setAttribute('open', ''); };
w.HTMLDialogElement.prototype.close = function () { this.removeAttribute('open'); };
const posts = [];
w.fetch = async (url, opts = {}) => {
  if (url.startsWith('/im/messages')) return { ok: true, json: async () => ({ html: msg('$a', 'Анна', 'привет') + msg('$bb', 'Я', 'моё', true)
    + msg('$ccc', 'Я', 'ещё<br>строка', true) }) };
  const q = new URLSearchParams(String(opts.body));
  posts.push([url, [...q]]);
  if (url === '/im/forward') return { ok: true, status: 201, json: async () => ({ id: '$f', room: q.get('to') }) };
  return { ok: true, json: async () => ({}) };
};
w.confirm = () => true;
w.EventSource = class { constructor() { this.h = {}; } addEventListener(t, f) { this.h[t] = f; } close() {} };
const copied = [];
w.go24Copy = t => copied.push(t);
w.eval(im);
await tick();
const $ = s => d.querySelector(s);
const box = $('[data-msgs]'), selbar = $('.im-chat [data-selbar]');
const click = el => el.dispatchEvent(new w.MouseEvent('click', { bubbles: true, cancelable: true }));
const sel = a => click(selbar.querySelector(`[data-sel="${a}"]`));

// «Выбрать» marks the message and shows the bar
click($('[data-id="$a"] [data-act="select"]'));
assert.ok(box.classList.contains('selecting'));
assert.equal(selbar.hidden, false);
assert.equal($('[data-sel-count]').textContent, 'Выбрано: 1');
assert.ok(selbar.querySelector('[data-sel="delete"]').disabled, 'a stranger\'s message cannot be deleted');

// clicks mark more and unmark; the menu does not open while choosing
click($('[data-id="$ccc"] .text'));
click($('[data-id="$bb"] .text'));
assert.equal($('[data-sel-count]').textContent, 'Выбрано: 3');
click($('[data-id="$a"] .text'));
assert.equal($('[data-sel-count]').textContent, 'Выбрано: 2');
assert.equal(selbar.querySelector('[data-sel="delete"]').disabled, false);

// «Копировать»: in the chat's order, with author and time; the choice is over
sel('copy');
assert.equal(copied.at(-1), 'Я [28.10 12:03]\nмоё\n\nЯ [28.10 12:04]\nещё\nстрока');
assert.ok(!box.classList.contains('selecting') && selbar.hidden && !d.querySelector('.msg.chosen'));

// «Переслать»: all chosen, in the chat's order, to the chat picked
click($('[data-id="$ccc"] [data-act="select"]'));
click($('[data-id="$a"] .text'));
sel('forward');
assert.ok($('[data-forward-pick]').hasAttribute('open'));
click($('[data-forward-list] [data-to="!other"]'));
await tick();
assert.deepEqual(posts.at(-1), ['/im/forward', [['room', '!r'], ['event', '$a'], ['event', '$ccc'], ['to', '!other']]]);

// «Отмена» leaves the choice
click($('[data-id="$bb"] [data-act="select"]'));
sel('cancel');
assert.ok(!box.classList.contains('selecting'));

// «Удалить»: the chosen own messages go in one request and leave the screen
click($('[data-id="$bb"] [data-act="select"]'));
click($('[data-id="$ccc"] .text'));
sel('delete');
await tick();
assert.deepEqual(posts.at(-1), ['/im/delete', [['room', '!r'], ['event', '$bb'], ['event', '$ccc']]]);
assert.ok(!$('[data-id="$bb"]') && !$('[data-id="$ccc"]'));
assert.ok(selbar.hidden);

// Esc leaves the choice
click($('[data-id="$a"] [data-act="select"]'));
d.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
assert.ok(!box.classList.contains('selecting') && !d.querySelector('.msg.chosen'));
console.log('imselect ok');
