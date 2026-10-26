// The «@» in the messenger (static/js/im.js) in jsdom: a chat where the person is mentioned among the
// unread messages shows «@» in the list (live too); the open chat takes it off and its button «@ N» goes through
// those mentions, oldest first; a mention arriving while the chat is scrolled up adds to the button.
// node test/js/immention.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const im = readFileSync(new URL('../../internal/web/static/js/im.js', import.meta.url), 'utf8');
const tick = () => new Promise(r => setTimeout(r, 20));
const row = (id, at = 0) => `<div class="im-row" data-row><a class="im-room" data-room-id="${id}" data-name="${id}" data-kind="group">
  <span data-preview></span><span class="cnt at" data-mention="${at}" ${at ? '' : 'hidden'}>@</span><span class="cnt" data-unread hidden>0</span></a></div>`;
const dom = new JSDOM(`<!doctype html><body><div class="im" data-im data-room="!open" data-failed="Не удалось">
  <nav data-list>${row('!a')}${row('!open', 2)}</nav>
  <section class="im-chat"><div class="im-msgs" data-msgs></div><button data-latest hidden></button>
  <button data-at-next hidden>@ <span data-at-count></span></button><form class="chatform" data-send><textarea name="body"></textarea></form></section>
  <aside data-thread-pane hidden><div data-thread-msgs></div><form data-send-thread><textarea name="body"></textarea></form></aside>
  </div></body>`, { runScripts: 'outside-only', url: 'http://go24.test/im?room=!open', pretendToBeVisual: true });
const w = dom.window, d = w.document;
w.CSS = { escape: s => s };
const flashed = [];
w.HTMLElement.prototype.scrollIntoView = function () { flashed.push(this.dataset.id); };
const msg = (id, me) => `<div class="msg${me ? ' to-me' : ''}" data-id="${id}"><div class="bubble">${id}</div></div>`;
w.fetch = async (url) => {
  if (url.startsWith('/im/messages')) return { ok: true, json: async () => ({ html: msg('$1', true) + msg('$2') + msg('$3', true) + msg('$4', true) }) };
  return { ok: true, status: 204, json: async () => ({}) };
};
const streams = [];
w.EventSource = class { constructor() { this.h = {}; streams.push(this); } addEventListener(t, f) { this.h[t] = f; } close() {} };
w.eval(im);
await tick();
const $ = s => d.querySelector(s);
const at = id => $(`.im-room[data-room-id="${id}"] [data-mention]`);

// the open chat: no «@» in the list; the button counts its two unread mentions — the newest two
assert.equal(at('!open').hidden, true);
const btn = $('[data-at-next]');
assert.equal(btn.hidden, false);
assert.equal(btn.textContent.trim(), '@ 2');
btn.click();
assert.equal(flashed.at(-1), '$3');
assert.equal(btn.textContent.trim(), '@ 1');
btn.click();
assert.equal(flashed.at(-1), '$4');
assert.equal(btn.hidden, true);

// a mention in another chat: «@» there at once; Matrix's counts then keep or take it off
streams[0].h.msg({ data: JSON.stringify({ room: '!a', id: '$5', name: 'Анна', preview: '@Борис', tome: true, html: '' }) });
assert.equal(at('!a').hidden, false);
streams[0].h.unread({ data: JSON.stringify({ rooms: { '!a': 1, '!open': 0 }, mentions: { '!a': 1 } }) });
assert.equal(at('!a').hidden, false);
streams[0].h.unread({ data: JSON.stringify({ rooms: { '!a': 0, '!open': 0 }, mentions: {} }) });
assert.equal(at('!a').hidden, true);

// the open chat scrolled up: a new mention goes to the button
const box = $('[data-msgs]');
Object.defineProperty(box, 'scrollHeight', { value: 2000 });
Object.defineProperty(box, 'clientHeight', { value: 300 });
box.scrollTop = 0;
streams[0].h.msg({ data: JSON.stringify({ room: '!open', id: '$6', name: 'Анна', preview: '@Борис', tome: true, html: msg('$6', true) }) });
assert.equal(btn.hidden, false);
btn.click();
assert.equal(flashed.at(-1), '$6');
console.log('immention: ok');
