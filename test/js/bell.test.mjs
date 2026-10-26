// The bell in jsdom: two tabs of one browser keep a single live stream; a notification reaches both tabs
// (counter, toast), so does a call ringing; the stream moves to the other tab when the holder closes. node test/js/bell.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/menu.js', import.meta.url), 'utf8');
const storage = new Map(); // localStorage shared by the tabs
const channels = new Set();
const streams = [];
const tab = name => {
  const dom = new JSDOM(`<!doctype html><body><div class="bell"><button data-bell></button>
    <span data-counter="notify" hidden>0</span><div data-bellpop hidden></div></div></body>`,
    { runScripts: 'outside-only', url: 'http://go24.test/my', pretendToBeVisual: true });
  const w = dom.window;
  Object.defineProperty(w, 'localStorage', { value: {
    getItem: k => storage.has(k) ? storage.get(k) : null, setItem: (k, v) => storage.set(k, String(v)), removeItem: k => storage.delete(k) } });
  w.BroadcastChannel = class {
    constructor() { channels.add(this); }
    postMessage(d) { for (const c of channels) if (c !== this) c.onmessage?.({ data: structuredClone(d) }); }
  };
  w.EventSource = class {
    constructor(u) { this.u = u; this.h = {}; this.open = true; this.tab = name; streams.push(this); }
    addEventListener(t, f) { this.h[t] = f; }
    close() { this.open = false; }
  };
  w.eval(script);
  return w;
};
const open = () => streams.filter(s => s.open);

const a = tab('a');
const b = tab('b');
assert.equal(open().length, 1, 'one stream for two tabs');
assert.equal(open()[0].tab, 'a', 'the first tab holds it');

open()[0].h.notify({ data: JSON.stringify({ id: 7, unread: 3, html: '<a class="nitem" data-nid="7">Анна написала</a>' }) });
for (const w of [a, b]) {
  const c = w.document.querySelector('[data-counter="notify"]');
  assert.equal(c.textContent, '3');
  assert.equal(c.hidden, false);
  assert.match(w.document.querySelector('.toasts')?.textContent || '', /Анна написала/, 'toast in every tab');
}

// a call rings: every tab hears it (ring.js listens for go24:call)
const heard = [];
for (const w of [a, b]) w.addEventListener('go24:call', e => heard.push(e.detail.ring));
open()[0].h.call({ data: JSON.stringify({ ring: 'r1', kind: 'ring', from: 'Анна' }) });
assert.deepEqual(heard, ['r1', 'r1'], 'the ring in both tabs');

a.dispatchEvent(new a.Event('pagehide')); a.close(); // the holder closes
assert.equal(open().length, 0);
await new Promise(r => setTimeout(r, 2100)); // the next heartbeat of the other tab
assert.equal(open().length, 1, 'the stream moved');
assert.equal(open()[0].tab, 'b');
b.close();
console.log('menu.js: bell shares one stream passed');
process.exit(0);
