// Presence in jsdom: [data-presence] gets the state class (the dot) and, with data-presence-text, the words;
// «Не беспокоить» (body[data-dnd]) keeps the counter of the bell but shows no toast. node test/js/presence.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/menu.js', import.meta.url), 'utf8');
const page = dnd => {
  const dom = new JSDOM(`<!doctype html><body${dnd ? ' data-dnd' : ''}><div class="bell"><button data-bell></button>
    <span data-counter="notify" hidden>0</span><div data-bellpop hidden></div></div>
    <span class="pr-ava" data-presence="u1" id="dot"></span><span data-presence="u2" data-presence-text id="line"></span>
    <span data-presence="u3" id="nobody"></span></body>`,
    { runScripts: 'outside-only', url: 'http://go24.test/im', pretendToBeVisual: true });
  const w = dom.window;
  w.asked = [];
  w.fetch = async u => { w.asked.push(u); return { ok: true, json: async () => ({
    u1: { state: 'online', text: 'В сети' }, u2: { state: 'offline', seen: 1, text: 'Был(а) в сети 5 мин назад' } }) }; };
  w.EventSource = class {
    constructor() { this.h = {}; w.stream = this; }
    addEventListener(t, f) { this.h[t] = f; }
    close() {}
  };
  w.eval(script);
  return w;
};

const w = page(false);
await new Promise(r => setTimeout(r, 20));
assert.equal(w.asked.length, 1, 'one request for everyone');
assert.match(w.asked[0], /^\/presence\?user=u1&user=u2&user=u3$/);
const d = w.document;
assert.ok(d.getElementById('dot').classList.contains('pr-online'), 'the green dot');
assert.equal(d.getElementById('dot').textContent, '', 'a dot has no words');
assert.equal(d.getElementById('dot').title, 'В сети');
assert.ok(d.getElementById('line').classList.contains('pr-offline'));
assert.equal(d.getElementById('line').textContent, 'Был(а) в сети 5 мин назад');
assert.equal(d.getElementById('nobody').className, '', 'unknown person: nothing');
w.stream?.h.notify?.({ data: JSON.stringify({ id: 1, unread: 2, html: '<a class="nitem">Анна написала</a>' }) });
assert.match(d.querySelector('.toasts')?.textContent || '', /Анна написала/, 'a toast normally');

const q = page(true);
await new Promise(r => setTimeout(r, 20));
q.stream?.h.notify?.({ data: JSON.stringify({ id: 1, unread: 2, html: '<a class="nitem">Анна написала</a>' }) });
assert.equal(q.document.querySelector('[data-counter="notify"]').textContent, '2', 'the counter stays');
assert.equal(q.document.querySelector('.toasts'), null, 'no toast in «Не беспокоить»');
console.log('menu.js: presence and do-not-disturb passed');
process.exit(0);
