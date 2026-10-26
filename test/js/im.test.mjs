// The messenger's unread counters in jsdom: the open chat is read at once and as messages come; the «Мессенджер»
// counter of the menu is one stream for all tabs. node test/js/im.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const im = readFileSync(new URL('../../internal/web/static/js/im.js', import.meta.url), 'utf8');
const menu = readFileSync(new URL('../../internal/web/static/js/menu.js', import.meta.url), 'utf8');
const tick = () => new Promise(r => setTimeout(r, 10));
const storage = new Map();
const channels = new Set();
const streams = [];
const tones = [];

const tab = (url, body) => {
  const dom = new JSDOM(`<!doctype html><body><nav><div class="navitem" data-im-unread><a href="/im">M</a>
    <a class="cnt green" data-counter="im" hidden></a></div></nav>${body}</body>`,
    { runScripts: 'outside-only', url, pretendToBeVisual: true });
  const w = dom.window;
  Object.defineProperty(w, 'localStorage', { value: {
    getItem: k => storage.has(k) ? storage.get(k) : null, setItem: (k, v) => storage.set(k, String(v)), removeItem: k => storage.delete(k) } });
  w.BroadcastChannel = class {
    constructor() { channels.add(this); }
    postMessage(d) { for (const c of channels) if (c !== this) c.onmessage?.({ data: structuredClone(d) }); }
  };
  w.EventSource = class {
    constructor(u) { this.u = u; this.h = {}; this.open = true; streams.push(this); }
    addEventListener(t, f) { this.h[t] = f; }
    close() { this.open = false; }
  };
  w.posts = [];
  w.fetch = async (u, opts = {}) => {
    if (opts.method === 'POST') w.posts.push({ u, body: String(opts.body) });
    return { ok: true, json: async () => ({ html: '' }) };
  };
  w.CSS = { escape: s => s };
  w.AudioContext = class { // the sound of a new message: each tone counted
    constructor() { this.currentTime = 0; this.destination = {}; }
    createOscillator() { const p = { setValueAtTime() {}, exponentialRampToValueAtTime() {} }; return { frequency: p, connect: g => g, start() { tones.push(1); }, stop() {} }; }
    createGain() { const g = { setValueAtTime() {}, exponentialRampToValueAtTime() {} }; return { gain: g, connect: x => x }; }
  };
  w.eval(menu);
  return w;
};
const room = (id, n) => `<a class="im-room" data-room-id="${id}" data-name="${id}"><span data-preview></span><span data-unread ${n ? '' : 'hidden'}>${n}</span></a>`;
const badge = (w, id) => w.document.querySelector(`[data-room-id="${id}"] [data-unread]`);

// a chat page with chat A open (it showed 3 unread when the page was made) and chat B
const w = tab('http://go24.test/im?room=A', `<div data-im data-room="A" data-failed="x"><div data-list>${room('A', 3)}${room('B', 0)}</div>
  <div data-msgs></div><div data-thread-pane hidden><div data-thread-msgs></div></div></div>`);
w.eval(im);
await tick();
assert.equal(badge(w, 'A').hidden, true, 'the open chat is read at once');
const live = streams.find(s => s.u === '/im/live');
const send = (t, d) => live.h[t]({ data: JSON.stringify(d) });

// a message in the open chat while looking at it: read on the server, no badge
send('msg', { id: 'e1', room: 'A', mine: false, preview: 'привет', name: 'Анна', html: '<div class="msg" data-id="e1"></div>' });
await tick();
assert.ok(w.posts.some(p => p.u === '/im/read' && p.body === 'room=A'), 'the open chat is marked read');
assert.equal(badge(w, 'A').hidden, true);
// in another chat: its badge counts
send('msg', { id: 'e2', room: 'B', mine: false, preview: 'ок', name: 'Пётр', html: '<div class="msg" data-id="e2"></div>' });
assert.equal(badge(w, 'B').textContent, '1');
// Matrix's counts: B has 2, A (open, in view) shows none
send('unread', { rooms: { A: 1, B: 2 }, total: 3 });
assert.equal(badge(w, 'B').textContent, '2');
assert.equal(badge(w, 'A').hidden, true);

// the menu counter: one stream for both tabs; the open chat in view is not counted
const counts = streams.filter(s => s.u === '/im/unread');
assert.equal(counts.length, 1, 'one stream');
counts[0].h.unread({ data: JSON.stringify({ rooms: { A: 1, B: 2 }, total: 3 }) });
assert.equal(w.document.querySelector('[data-counter="im"]').textContent, '2');
const other = tab('http://go24.test/my', '');
await tick();
assert.equal(streams.filter(s => s.u === '/im/unread' && s.open).length, 1, 'the second tab shares the stream');
assert.equal(other.document.querySelector('[data-counter="im"]').textContent, '3', 'a new tab gets the last counts');
// «Настройки»: the sound of a new message — once, from the tab that holds the stream; not for the chat open and in view,
// not when the person turned it off, not in «Не беспокоить»; the first counts after opening the stream do not sound
assert.equal(tones.length, 0, 'the first counts are silent');
const ring = rooms => counts[0].h.unread({ data: JSON.stringify({ rooms, total: Object.values(rooms).reduce((a, b) => a + b, 0), sound: true }) });
ring({ A: 1, B: 3 });
assert.equal(tones.length, 1, 'a new message in another chat sounds');
ring({ A: 2, B: 3 });
assert.equal(tones.length, 1, 'the open chat in view is silent');
counts[0].h.unread({ data: JSON.stringify({ rooms: { A: 2, B: 4 }, total: 6, sound: false }) });
assert.equal(tones.length, 1, 'the sound turned off');
w.document.body.setAttribute('data-dnd', '');
ring({ A: 2, B: 5 });
assert.equal(tones.length, 1, '«Не беспокоить» is silent');
w.document.body.removeAttribute('data-dnd');
ring({ A: 2, B: 5 });
assert.equal(tones.length, 1, 'nothing new — silent');
counts[0].h.unread({ data: JSON.stringify({ rooms: { A: 0, B: 0 }, total: 0 }) });
assert.equal(other.document.querySelector('[data-counter="im"]').hidden, true, 'all read: no counter');
// «Коллабы»: the chat of a collab's card and the menu counter — the collabs' tasks needing attention (data-base) and
// the unread messages of their chats, threads too
const cl = tab('http://go24.test/collabs', `<a data-counter="collabs" data-base="2" data-rooms="B C" hidden>2</a>
  <span data-room-unread="B" hidden></span><span data-room-unread="C" hidden></span>`);
await tick();
counts[0].h.unread({ data: JSON.stringify({ rooms: { A: 0, B: 4, C: 1 }, threads: { C: 2 }, total: 7 }) });
const cc = cl.document.querySelector('[data-counter="collabs"]');
assert.equal(cc.textContent, '9', 'the collabs counter adds their chats');
assert.equal(cc.hidden, false);
assert.equal(cl.document.querySelector('[data-room-unread="B"]').textContent, '4', 'the chat of a card');
assert.equal(cl.document.querySelector('[data-room-unread="C"]').textContent, '3', 'its threads count');
counts[0].h.unread({ data: JSON.stringify({ rooms: { A: 0, B: 0, C: 0 }, total: 0 }) });
assert.equal(cc.textContent, '2', 'chats read: the tasks stay');
assert.equal(cl.document.querySelector('[data-room-unread="B"]').hidden, true, 'a read chat: no counter');
console.log('im.js: unread counters passed');
process.exit(0);
