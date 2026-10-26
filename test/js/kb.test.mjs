// «База знаний»: a picture or a file chosen, pasted or dropped in a page's editor is uploaded and its Markdown goes
// where the cursor is, in jsdom. node test/js/kb.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/kb.js', import.meta.url), 'utf8');
const dom = new JSDOM(`<!doctype html><body><form method="post">
  <textarea name="body" data-kb-upload="/kb/s1/files" data-kb-uploading="Загрузка: %s…" data-kb-failed="Не удалось загрузить %s">Начало
Конец</textarea>
  <label>Картинка или файл<input type="file" multiple hidden data-kb-file></label></form></body>`, { runScripts: 'outside-only', url: 'http://go24.test/kb/s1/p1?edit=1' });
const w = dom.window;
const sent = [];
let answers = [];
w.fetch = async (url, opts) => {
  sent.push({ url, name: opts.body.get('file').name });
  const a = answers.shift();
  return { ok: a.status < 300, status: a.status, statusText: 'x', json: async () => a.body };
};
w.eval(script);
const tick = async () => { for (let i = 0; i < 5; i++) await new Promise(r => setTimeout(r, 0)); };
const ta = w.document.querySelector('textarea'), input = w.document.querySelector('input[data-kb-file]');
const file = (name, type) => new w.File(['x'], name, { type });

// chosen with the button: uploaded one by one, put at the cursor (after «Начало»), each on a line of its own
ta.selectionStart = ta.selectionEnd = 'Начало'.length;
answers = [{ status: 200, body: { md: '![план.png](/files/f1?inline=1)' } }, { status: 200, body: { md: '[отчёт.pdf](/files/f2)' } }];
Object.defineProperty(input, 'files', { value: [file('план.png', 'image/png'), file('отчёт.pdf', 'application/pdf')], configurable: true });
input.dispatchEvent(new w.Event('change', { bubbles: true }));
await tick();
assert.deepEqual(sent, [{ url: '/kb/s1/files', name: 'план.png' }, { url: '/kb/s1/files', name: 'отчёт.pdf' }]);
assert.equal(ta.value, 'Начало\n![план.png](/files/f1?inline=1)\n[отчёт.pdf](/files/f2)\nКонец');

// pasted from the clipboard: the picture goes in at the cursor, the paste itself is cancelled
sent.length = 0;
ta.selectionStart = ta.selectionEnd = ta.value.length;
answers = [{ status: 200, body: { md: '![image.png](/files/f3?inline=1)' } }];
const paste = new w.Event('paste', { bubbles: true, cancelable: true });
paste.clipboardData = { files: [file('image.png', 'image/png')] };
ta.dispatchEvent(paste);
await tick();
assert.ok(paste.defaultPrevented);
assert.ok(ta.value.endsWith('Конец\n![image.png](/files/f3?inline=1)\n'), ta.value);

// a text paste is left to the browser
const text = new w.Event('paste', { bubbles: true, cancelable: true });
text.clipboardData = { files: [] };
ta.dispatchEvent(text);
assert.ok(!text.defaultPrevented);

// refused: the text stays, the reason is shown
const before = ta.value;
answers = [{ status: 403, body: { error: 'Нет прав' } }];
const drop = new w.Event('drop', { bubbles: true, cancelable: true });
drop.dataTransfer = { files: [file('x.png', 'image/png')] };
ta.dispatchEvent(drop);
await tick();
assert.equal(ta.value, before);
assert.equal(w.document.querySelector('[data-kb-status]').textContent, 'Не удалось загрузить x.png: Нет прав');
console.log('kb: ok');
