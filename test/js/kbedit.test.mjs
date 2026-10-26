// «База знаний»: the panel of the page's editor puts Markdown around the selection or at the start of its lines,
// Ctrl+B / Ctrl+K do the same, «Предпросмотр» shows the server's rendering, unsaved changes ask before leaving, in
// jsdom. node test/js/kbedit.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/kbedit.js', import.meta.url), 'utf8');
const dom = new JSDOM(`<!doctype html><body><form method="post"><input name="title" value="Отпуск">
  <div class="kbtools" data-kb-preview="/kb/s1/preview">
    <button type="button" data-kb-tab="write" class="on"></button><button type="button" data-kb-tab="preview"></button>
    <button type="button" data-kb-fmt="bold"></button><button type="button" data-kb-fmt="link" data-kb-link-text="текст ссылки"></button>
    <button type="button" data-kb-fmt="ol"></button><button type="button" data-kb-fmt="check"></button>
    <button type="button" data-kb-fmt="table" data-kb-table="Кто|Что"></button><button type="button" data-kb-fmt="hr"></button>
  </div><textarea name="body" data-kb-upload="/kb/s1/files" data-kb-leave="Уйти?">раз два
три</textarea></form></body>`, { runScripts: 'outside-only', url: 'http://go24.test/kb/s1/p1?edit=1' });
const w = dom.window, d = w.document;
const sent = [];
w.fetch = async (url, opts) => {
  sent.push({ url, body: opts.body.get('body') });
  return { ok: true, status: 200, json: async () => ({ html: '<p><b>раз</b></p>' }) };
};
w.eval(script);
const tick = async () => { for (let i = 0; i < 5; i++) await new Promise(r => setTimeout(r, 0)); };
const ta = d.querySelector('textarea'), click = sel => d.querySelector(sel).dispatchEvent(new w.MouseEvent('click', { bubbles: true }));
const sel = (a, b) => { ta.selectionStart = a; ta.selectionEnd = b; };

// unchanged: leaving asks nothing
let leave = new w.Event('beforeunload', { cancelable: true });
w.dispatchEvent(leave);
assert.ok(!leave.defaultPrevented);

// bold around the selection, the text stays selected
sel(0, 3);
click('[data-kb-fmt="bold"]');
assert.equal(ta.value, '**раз** два\nтри');
assert.equal(ta.value.slice(ta.selectionStart, ta.selectionEnd), 'раз');

// Ctrl+K: a link of the selection, the address selected to type over
sel(8, 11);
ta.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'k', ctrlKey: true, bubbles: true, cancelable: true }));
assert.equal(ta.value, '**раз** [два](https://)\nтри');
assert.equal(ta.value.slice(ta.selectionStart, ta.selectionEnd), 'https://');

// a numbered list and a checklist over the selected lines
ta.value = 'а\nб';
sel(0, 3);
click('[data-kb-fmt="ol"]');
assert.equal(ta.value, '1. а\n2. б');
ta.value = 'а\nб\n';
sel(0, 4);
click('[data-kb-fmt="check"]');
assert.equal(ta.value, '- [ ] а\n- [ ] б\n');

// a table and a line go on lines of their own
ta.value = 'x';
sel(1, 1);
click('[data-kb-fmt="table"]');
assert.equal(ta.value, 'x\n| Кто | Что |\n|---|---|\n|  |  |\n');
assert.equal(ta.selectionStart, 4);
ta.value = 'x\ny';
sel(1, 1);
click('[data-kb-fmt="hr"]');
assert.equal(ta.value, 'x\n---\ny');

// «Предпросмотр»: the server renders the text, the field and the panel's buttons step aside
click('[data-kb-tab="preview"]');
assert.ok(ta.hidden);
assert.ok(d.querySelector('[data-kb-fmt="bold"]').disabled);
assert.ok(d.querySelector('[data-kb-tab="preview"]').classList.contains('on'));
await tick();
assert.deepEqual(sent, [{ url: '/kb/s1/preview', body: 'x\n---\ny' }]);
assert.equal(d.querySelector('.kbpreview').innerHTML, '<p><b>раз</b></p>');
click('[data-kb-tab="write"]');
assert.ok(!ta.hidden);
assert.equal(d.querySelector('.kbpreview'), null);
assert.ok(!d.querySelector('[data-kb-fmt="bold"]').disabled);

// changed: leaving asks; saving does not
leave = new w.Event('beforeunload', { cancelable: true });
w.dispatchEvent(leave);
assert.ok(leave.defaultPrevented);
let submitted = 0;
ta.form.requestSubmit = () => { submitted++; ta.form.dispatchEvent(new w.Event('submit', { cancelable: true })); };
ta.dispatchEvent(new w.KeyboardEvent('keydown', { key: 's', ctrlKey: true, bubbles: true, cancelable: true }));
assert.equal(submitted, 1);
leave = new w.Event('beforeunload', { cancelable: true });
w.dispatchEvent(leave);
assert.ok(!leave.defaultPrevented);

console.log('kbedit ok');
