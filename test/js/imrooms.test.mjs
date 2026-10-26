// The chat list's own marks in the messenger (static/js/im.js) in jsdom: «Закрепить чат» moves the chat to the top
// and keeps it there when others get messages, «Без звука» leaves the counter alone on a new message, «Отметить
// непрочитанным» shows the dot, opening the chat takes it off; the search box hides whole rows; «Скрыть чат» takes
// it out of the list until a new message, the search still finds it.
// node test/js/imrooms.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const im = readFileSync(new URL('../../internal/web/static/js/im.js', import.meta.url), 'utf8');
const tick = () => new Promise(r => setTimeout(r, 20));
const row = (id, name, marks = '') => `<div class="im-row" data-row ${marks}><a class="im-room" data-room-id="${id}" data-name="${name}" data-kind="dm">
  <span data-preview></span><span class="cnt" data-unread hidden>0</span></a>
  <details class="im-rmenu"><summary>…</summary><div class="pop"><button data-room-mark="favorite">p</button><button data-room-mark="mute">m</button>
  <button data-room-mark="unread">u</button><button data-room-hide>h</button></div></details></div>`;
const dom = new JSDOM(`<!doctype html><body><div class="im" data-im data-room="!open" data-failed="Не удалось">
  <input type="search" data-filter>
  <nav data-list>${row('!a', 'Анна')}${row('!b', 'Борис')}${row('!c', 'Вера')}${row('!open', 'Открытый', 'data-marked="1"')}</nav>
  <section class="im-chat"><div class="im-msgs" data-msgs></div><form class="chatform" data-send><textarea name="body"></textarea></form></section>
  <aside data-thread-pane hidden><div data-thread-msgs></div><form data-send-thread><textarea name="body"></textarea></form></aside>
  </div></body>`, { runScripts: 'outside-only', url: 'http://go24.test/im?room=!open', pretendToBeVisual: true });
const w = dom.window, d = w.document;
w.CSS = { escape: s => s };
w.HTMLElement.prototype.scrollIntoView = () => {};
const posts = [];
w.fetch = async (url, opts = {}) => {
  if (url.startsWith('/im/messages')) return { ok: true, json: async () => ({ html: '' }) };
  posts.push([url, Object.fromEntries(new URLSearchParams(String(opts.body)))]);
  return { ok: true, status: 204, json: async () => ({}) };
};
const streams = [];
w.EventSource = class { constructor() { this.h = {}; streams.push(this); } addEventListener(t, f) { this.h[t] = f; } close() {} };
w.eval(im);
await tick();
const $ = s => d.querySelector(s);
const order = () => [...d.querySelectorAll('[data-list] .im-room')].map(a => a.dataset.roomId).join(' ');
const rowOf = id => $(`.im-room[data-room-id="${id}"]`).closest('[data-row]');
const mark = (id, m) => rowOf(id).querySelector(`[data-room-mark="${m}"]`).click();
const live = (room, name) => streams[0].h.msg({ data: JSON.stringify({ room, id: '$' + Math.random(), name, preview: 'привет', html: '' }) });

// the open chat is read: its mark is gone at once
assert.equal(rowOf('!open').dataset.marked, '');

// pin Вера: she goes to the top; a message to Борис puts him right below her, not above
mark('!c', 'favorite');
await tick();
assert.deepEqual(posts.at(-1), ['/im/mark', { room: '!c', mark: 'favorite', on: '1' }]);
assert.equal(rowOf('!c').dataset.favorite, '1');
assert.equal(order(), '!c !a !b !open');
live('!b', 'Борис');
assert.equal(order(), '!c !b !a !open');
live('!c', 'Вера');
assert.equal(order(), '!c !b !a !open');
// unpinned: she joins the others, below the pinned ones (none now — at the top)
mark('!c', 'favorite');
await tick();
assert.deepEqual(posts.at(-1), ['/im/mark', { room: '!c', mark: 'favorite', on: '0' }]);
assert.equal(rowOf('!c').dataset.favorite, '');

// muted: a new message does not count at once (Matrix's own count stays 0 for it)
mark('!a', 'mute');
await tick();
assert.equal(rowOf('!a').dataset.muted, '1');
live('!a', 'Анна');
assert.equal($('.im-room[data-room-id="!a"] [data-unread]').hidden, true);
live('!b', 'Борис');
assert.equal($('.im-room[data-room-id="!b"] [data-unread]').textContent, '2');

// marked unread, and back
mark('!b', 'unread');
await tick();
assert.equal(rowOf('!b').dataset.marked, '1');
mark('!b', 'unread');
await tick();
assert.deepEqual(posts.at(-1), ['/im/mark', { room: '!b', mark: 'unread', on: '0' }]);
assert.equal(rowOf('!b').dataset.marked, '');

// the search box hides whole rows, the menus with them
const filter = $('[data-filter]');
filter.value = 'вер';
filter.dispatchEvent(new w.Event('input', { bubbles: true }));
assert.deepEqual([...d.querySelectorAll('[data-row]')].filter(r => !r.hidden).map(r => r.querySelector('.im-room').dataset.name), ['Вера']);

// hidden: out of the list; the search finds it; a new message brings it back
filter.value = '';
filter.dispatchEvent(new w.Event('input', { bubbles: true }));
rowOf('!a').querySelector('[data-room-hide]').click();
await tick();
assert.deepEqual(posts.at(-1), ['/im/mark', { room: '!a', mark: 'hidden', on: '1' }]);
assert.equal(rowOf('!a').hidden, true);
filter.value = 'анн';
filter.dispatchEvent(new w.Event('input', { bubbles: true }));
assert.equal(rowOf('!a').hidden, false);
filter.value = '';
filter.dispatchEvent(new w.Event('input', { bubbles: true }));
assert.equal(rowOf('!a').hidden, true);
live('!a', 'Анна');
assert.equal(rowOf('!a').hidden, false);
assert.equal(rowOf('!a').dataset.hidden, '');
console.log('im.js: the chat list marks passed');
