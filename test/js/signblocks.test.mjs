// «Подпись» → «Блоки на страницах»: a block is put where the page is clicked, moved by dragging, resized by its corner,
// removed by ×; the form keeps them as JSON, in jsdom.
// node test/js/signblocks.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/signblocks.js', import.meta.url), 'utf8');
const dom = new JSDOM(`<!doctype html><body><form data-sgblocks data-del="Удалить">
  <input type="hidden" name="blocks" value='[{"signer":2,"kind":"date","page":2,"x":0.1,"y":0.1,"w":0.15,"h":0.03},{"signer":2,"kind":"name","page":9,"x":0,"y":0,"w":0.1,"h":0.1}]'>
  <div class="sgb-page" data-page="1"></div><div class="sgb-page" data-page="2"></div>
  <select data-signer><option value="1">1. Анна</option><option value="2">2. Иван</option></select>
  <button type="button" data-kind="signature">Подпись</button><button type="button" data-kind="date">Дата подписания</button><button type="button" data-kind="text">Текст</button><button type="button" data-kind="field">Поле</button>
  <input data-text><input type="checkbox" data-required><p data-count data-tpl="Блоков: %d"></p></form></body>`, { runScripts: 'outside-only', url: 'http://go24.test/sign/s1/blocks/edit' });
const w = dom.window, doc = w.document;
// a page is 600×800 at (0, 0)
for (const p of doc.querySelectorAll('.sgb-page')) p.getBoundingClientRect = () => ({ left: 0, top: 0, width: 600, height: 800 });
w.eval(script);
const kept = () => JSON.parse(doc.querySelector('[name="blocks"]').value);
const count = doc.querySelector('[data-count]');
const page1 = doc.querySelector('[data-page="1"]');
const click = (el, x, y) => el.dispatchEvent(new w.MouseEvent('click', { bubbles: true, clientX: x, clientY: y }));
const ptr = (el, type, x, y) => { const e = new w.MouseEvent(type, { bubbles: true, clientX: x, clientY: y }); e.pointerId = 1; el.dispatchEvent(e); };

// as it opens: the block of a page the document has is drawn, one of no page is dropped
assert.equal(doc.querySelectorAll('.sgb-block').length, 1);
assert.equal(doc.querySelector('[data-page="2"] .sgb-block').textContent.startsWith('Дата подписания · 2. Иван'), true);
assert.equal(kept().length, 1);
assert.equal(count.textContent, 'Блоков: 1');

// a click with no block chosen puts nothing
click(page1, 300, 400);
assert.equal(kept().length, 1);
// the signature of Анна where the page is clicked: centred at the click
doc.querySelector('[data-kind="signature"]').click();
assert.equal(doc.querySelector('[data-kind="signature"]').getAttribute('aria-pressed'), 'true');
click(page1, 300, 400);
let b = kept()[1];
assert.deepEqual([b.signer, b.kind, b.page, b.x, b.y, b.w, b.h], [1, 'signature', 1, 0.375, 0.47, 0.25, 0.06]);
assert.equal(doc.querySelector('[data-kind="signature"]').getAttribute('aria-pressed'), 'false');
// near the edge: kept within the page
doc.querySelector('[data-kind="date"]').click();
click(page1, 599, 799);
b = kept()[2];
assert.equal(Math.abs(b.x - 0.85) < 1e-9 && Math.abs(b.y - 0.97) < 1e-9, true);
// a text block needs its text
doc.querySelector('[data-kind="text"]').click();
click(page1, 100, 100);
assert.equal(kept().length, 3);
doc.querySelector('[data-text]').value = ' Согласовано ';
click(page1, 100, 100);
assert.equal(kept()[3].text, 'Согласовано');

// dragged by 60×80 px: moved by 0.1 of the page; by its corner — resized
const sig = page1.querySelectorAll('.sgb-block')[0];
ptr(sig, 'pointerdown', 300, 400); ptr(sig, 'pointermove', 360, 480); ptr(sig, 'pointerup', 360, 480);
b = kept()[1];
assert.deepEqual([b.x, b.y], [0.475, 0.57]);
const corner = sig.querySelector('.sgb-size');
ptr(corner, 'pointerdown', 0, 0); ptr(sig, 'pointermove', 60, 8); ptr(sig, 'pointerup', 60, 8);
b = kept()[1];
assert.deepEqual([b.x, b.w, b.h], [0.475, 0.35, 0.07]);
// × removes it
sig.querySelector('.sgb-del').click();
assert.equal(kept().length, 3);
assert.equal(kept().some((x) => x.kind === 'signature'), false);
assert.equal(count.textContent, 'Блоков: 3');

// a field: its caption from the text, required when ticked; an id and a value given out are not sent back
doc.querySelector('[data-text]').value = 'ИНН';
doc.querySelector('[data-required]').checked = true;
doc.querySelector('[data-kind="field"]').click();
click(page1, 300, 200);
b = kept()[3];
assert.deepEqual([b.kind, b.text, b.required, b.w, b.h], ['field', 'ИНН', true, 0.3, 0.03]);
assert.equal(page1.querySelector('.sgb-field').textContent.startsWith('Поле: ИНН * · 1. Анна'), true);

const dom2 = new JSDOM(`<!doctype html><body><form data-sgblocks><input type="hidden" name="blocks"
  value='[{"id":"b1","signer":1,"kind":"field","page":1,"x":0.1,"y":0.1,"w":0.3,"h":0.03,"text":"Адрес","value":"Москва"}]'>
  <div class="sgb-page" data-page="1"></div><select data-signer><option value="1">1. Анна</option></select>
  <input data-text><p data-count></p></form></body>`, { runScripts: 'outside-only' });
dom2.window.eval(script);
assert.deepEqual(JSON.parse(dom2.window.document.querySelector('[name="blocks"]').value),
  [{ signer: 1, kind: 'field', page: 1, x: 0.1, y: 0.1, w: 0.3, h: 0.03, text: 'Адрес' }]);

console.log('signblocks: ok');
