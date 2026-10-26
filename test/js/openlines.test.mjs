// «Открытые линии»: «Быстрые ответы» after «/» in the reply and by the button; the list and the open dialogue refresh
// when the server says a dialogue changed, in jsdom. node test/js/openlines.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/openlines.js', import.meta.url), 'utf8');
const quick = JSON.stringify([{ ID: 'q1', Title: 'Доставка', Body: 'Доставка — 1–2 дня.' }, { ID: 'q2', Title: 'Акция', Body: 'Скидка 10%' }])
  .replace(/"/g, '&quot;');
const page = (list, msgs) => `<!doctype html><body><div data-ol data-current="s1"><div data-ol-tabs><a>Все 1</a></div>
  <nav data-ol-list>${list}</nav><div data-ol-msgs>${msgs}</div>
  <form><div data-ol-quick-list="${quick}"><textarea data-ol-reply></textarea><div data-ol-quick hidden></div></div>
  <button type="button" data-ol-quick-btn></button></form></div>
  <template data-ol-quick-empty>Нет</template></body>`;
const dom = new JSDOM(page('<a>Иван</a>', '<div>Привет</div>'), { runScripts: 'outside-only', url: 'http://go24.test/openlines?s=s1' });
const w = dom.window, d = w.document;
const sources = [];
w.EventSource = class { constructor(u) { this.url = u; this.l = {}; sources.push(this); } addEventListener(k, f) { this.l[k] = f; } close() {} };
let fetched = 0;
w.fetch = async () => { fetched++; return { ok: true, text: async () => page('<a>Иван</a><a>Мария</a>', '<div>Привет</div><div>Новое</div>') }; };
w.eval(script);

const reply = d.querySelector('[data-ol-reply]'), box = d.querySelector('[data-ol-quick]');
const type = v => { reply.value = v; reply.selectionStart = reply.selectionEnd = v.length; reply.dispatchEvent(new w.Event('input')); };
const key = k => reply.dispatchEvent(new w.KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true }));
const titles = () => [...box.querySelectorAll('button b')].map(b => b.textContent);

// «/» opens all, what follows filters by the title or the text; Enter puts the answer in place of «/…»
type('/');
assert.equal(box.hidden, false);
assert.deepEqual(titles(), ['Доставка', 'Акция']);
type('Добрый день! /скид');
assert.deepEqual(titles(), ['Акция']);
key('Enter');
assert.equal(reply.value, 'Добрый день! Скидка 10%');
assert.equal(box.hidden, true);

// arrows choose, Escape closes; «/» inside a word is not a command
type('/');
key('ArrowDown');
assert.ok(box.querySelectorAll('button')[1].classList.contains('on'));
key('Escape');
assert.equal(box.hidden, true);
type('a/b');
assert.equal(box.hidden, true);
type('/нет такого');
assert.equal(box.hidden, true);

// the button opens the list at the caret
type('Здравствуйте. ');
d.querySelector('[data-ol-quick-btn]').click();
assert.deepEqual(titles(), ['Доставка', 'Акция']);
box.querySelector('button').dispatchEvent(new w.MouseEvent('mousedown', { bubbles: true, cancelable: true }));
assert.equal(reply.value, 'Здравствуйте. Доставка — 1–2 дня.');

// live: a change of the open dialogue refreshes the list and its messages; the reply being typed stays
assert.equal(sources[0].url, '/openlines/live');
sources[0].l.session({ data: 's1' });
sources[0].l.session({ data: 's2' });
await new Promise(r => setTimeout(r, 500));
assert.equal(fetched, 1);
assert.equal(d.querySelectorAll('[data-ol-list] a').length, 2);
assert.equal(d.querySelectorAll('[data-ol-msgs] div').length, 2);
assert.equal(reply.value, 'Здравствуйте. Доставка — 1–2 дня.');

// another dialogue changed: only the list
w.fetch = async () => { fetched++; return { ok: true, text: async () => page('<a>Пётр</a>', '<div>Чужое</div>') }; };
sources[0].l.session({ data: 's9' });
await new Promise(r => setTimeout(r, 500));
assert.equal(d.querySelector('[data-ol-list]').textContent, 'Пётр');
assert.equal(d.querySelectorAll('[data-ol-msgs] div').length, 2);

console.log('openlines: ok');
