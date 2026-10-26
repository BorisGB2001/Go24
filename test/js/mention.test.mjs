// The @-mention picker in jsdom: "@ан" offers matching people; Enter puts "@Full Name " into the text and does
// not send the message. node test/js/mention.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/mention.js', import.meta.url), 'utf8');
const dom = new JSDOM(`<!doctype html><body><form><textarea name="body" data-mention></textarea></form></body>`,
  { runScripts: 'outside-only', url: 'http://go24.test/t/GO-1' });
const w = dom.window;
w.fetch = async () => ({ ok: true, json: async () => [{ id: '1', name: 'Анна Ли' }, { id: '2', name: 'Пётр Иванов' }, { id: '3', name: 'Иван Анин' }] });
w.eval(script);
const f = w.document.querySelector('textarea');
let sent = 0;
f.addEventListener('keydown', e => { if (e.key === 'Enter') sent++; }); // the chat's Enter-to-send

const type = async text => {
  f.value = text;
  f.setSelectionRange(text.length, text.length);
  f.dispatchEvent(new w.Event('input', { bubbles: true }));
  await new Promise(r => setTimeout(r, 10));
};
await type('привет @ан');
const names = [...w.document.querySelectorAll('.mention-item')].map(e => e.textContent);
assert.deepEqual(names, ['Анна Ли', 'Иван Анин'], 'matches by any word');
f.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }));
f.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }));
assert.equal(f.value, 'привет @Иван Анин ');
assert.equal(sent, 0, 'Enter picked the name, not sent the message');
assert.equal(w.document.querySelector('.mention-pop'), null);

await type('mail@example');
assert.equal(w.document.querySelector('.mention-pop'), null, 'no picker inside an e-mail address');
console.log('mention.js: picker passed');
