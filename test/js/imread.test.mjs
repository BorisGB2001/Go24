// Read receipts and «печатает…» in the messenger (static/js/im.js) in jsdom: a live receipt turns one's own messages
// up to the one read (or sent before it was read, when it is not on the screen) into ✓✓ with the reader's name;
// someone typing shows a line above the field, gone when they stop or their message comes; typing tells the chat
// once in a few seconds and stops on sending. node test/js/imread.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const im = readFileSync(new URL('../../internal/web/static/js/im.js', import.meta.url), 'utf8');
const tick = () => new Promise(r => setTimeout(r, 20));
const msg = (id, mine, ts) => `<div class="msg${mine ? ' mine' : ''}" data-id="${id}" data-ts="${ts}"><div class="bubble"><div class="text">${id}</div>
  <span class="time">12:00${mine ? ' <span class="tick" data-tick data-readers="" title="Отправлено">✓</span>' : ''}</span></div></div>`;
const dom = new JSDOM(`<!doctype html><body><div class="im" data-im data-room="!r" data-failed="Не удалось" data-read-by="Прочитано: %s"
  data-typing-one="%s печатает…" data-typing-many="%s печатают…">
  <nav data-list><a class="im-room" data-room-id="!r" data-name="Мы"><span data-preview></span><span data-unread hidden></span></a></nav>
  <section class="im-chat"><div class="im-msgs" data-msgs></div><div data-typing hidden></div>
    <form class="chatform" data-send><textarea name="body"></textarea></form></section>
  <aside data-thread-pane hidden><div data-thread-msgs></div><form data-send-thread><textarea name="body"></textarea></form></aside>
  </div></body>`, { runScripts: 'outside-only', url: 'http://go24.test/im?room=!r', pretendToBeVisual: true });
const w = dom.window, d = w.document;
w.CSS = { escape: s => s };
w.HTMLElement.prototype.scrollIntoView = () => {};
const posts = [];
let now = 100000;
w.Date.now = () => now;
w.fetch = async (url, opts = {}) => {
  if (url.startsWith('/im/messages')) return { ok: true, json: async () => ({ html: msg('$1', true, 10) + msg('$2', false, 20) + msg('$3', true, 30) + msg('$4', true, 40) }) };
  posts.push([url, Object.fromEntries(new URLSearchParams(String(opts.body)))]);
  if (url === '/im/send') return { ok: true, json: async () => ({ id: '$n', html: msg('$n', true, 50) }) };
  return { ok: true, json: async () => ({}) };
};
const streams = [];
w.EventSource = class { constructor() { this.h = {}; streams.push(this); } addEventListener(t, f) { this.h[t] = f; } close() {} };
w.eval(im);
await tick();
const $ = s => d.querySelector(s);
const live = (type, data) => streams[0].h[type]({ data: JSON.stringify(data) });
const ticks = () => [...d.querySelectorAll('.msg.mine')].map(m => m.dataset.id + m.querySelector('[data-tick]').textContent);

// a receipt at $3: $1 and $3 are read by Анна, $4 not yet
live('change', { room: '!r', id: '$3', change: 'read', body: 'Анна', at: 35 });
assert.deepEqual(ticks(), ['$1✓✓', '$3✓✓', '$4✓']);
assert.equal($('[data-id="$3"] [data-tick]').title, 'Прочитано: Анна');
// a receipt on an event not on the screen: by time; a second reader joins the name
live('change', { room: '!r', id: '$elsewhere', change: 'read', body: 'Борис', at: 45 });
assert.deepEqual(ticks(), ['$1✓✓', '$3✓✓', '$4✓✓']);
assert.equal($('[data-id="$1"] [data-tick]').title, 'Прочитано: Анна, Борис');
assert.equal($('[data-id="$4"] [data-tick]').title, 'Прочитано: Борис');
// another chat's receipt changes nothing here
live('change', { room: '!other', id: '$n', change: 'read', body: 'Вера', at: 99 });
assert.equal($('[data-id="$4"] [data-tick]').title, 'Прочитано: Борис');

// typing: one, several, stopped; a message from them takes the line away
live('change', { room: '!r', change: 'typing', body: 'Анна' });
assert.equal($('[data-typing]').hidden, false);
assert.equal($('[data-typing]').textContent, 'Анна печатает…');
live('change', { room: '!r', change: 'typing', body: 'Анна, Борис' });
assert.equal($('[data-typing]').textContent, 'Анна, Борис печатают…');
live('change', { room: '!r', change: 'typing', body: '' });
assert.equal($('[data-typing]').hidden, true);
live('change', { room: '!r', change: 'typing', body: 'Анна' });
live('msg', { room: '!r', id: '$5', name: 'Анна', preview: 'привет', html: msg('$5', false, 60) });
assert.equal($('[data-typing]').hidden, true, 'her message came');

// one's own typing: told once in a few seconds, stopped on sending
const text = $('[data-send] textarea');
const type = v => { text.value = v; text.dispatchEvent(new w.Event('input', { bubbles: true })); };
type('п'); type('пр');
now += 5000; type('при');
const typed = posts.filter(p => p[0] === '/im/typing');
assert.deepEqual(typed, [['/im/typing', { room: '!r', on: '1' }], ['/im/typing', { room: '!r', on: '1' }]]);
$('[data-send]').requestSubmit();
await tick();
assert.deepEqual(posts.filter(p => p[0] === '/im/typing').at(-1), ['/im/typing', { room: '!r', on: '0' }]);
assert.equal(ticks().at(-1), '$n✓', 'a message just sent is not read yet');
// a call's line drawn anew when the call is over
live('change', { room: '!r', id: '$2', change: 'edit', body: 'x', html: '<div class="msg system call" data-id="$2">Звонок завершён</div>' });
assert.equal($('[data-id="$2"]').textContent, 'Звонок завершён');
assert.ok($('[data-id="$2"]').classList.contains('call'));
console.log('im.js: read receipts and typing passed');
