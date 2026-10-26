// «Файл с Диска» in jsdom: the picker lists the drive's folders and files; a file chosen for a form becomes a hidden
// input with a chip (once, and gone when the form is cleared); a file chosen for a task is sent at once (the page
// then comes back at the task's files).
// node test/js/drivepick.test.mjs
import { JSDOM, VirtualConsole } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const js = readFileSync(new URL('../../internal/web/static/js/drivepick.js', import.meta.url), 'utf8');
const dom = new JSDOM(`<!doctype html><body>
  <form id="post"><button type="button" data-disk-pick data-input="drive_file" data-l-title="Файл с Диска" data-l-top="Корень" data-l-empty="Пусто">p</button><span data-disk-picked></span></form>
  <form id="task"><button type="button" data-disk-pick data-to="/t/GO-1/files/drive" data-l-title="Файл с Диска">t</button></form>
</body>`, { runScripts: 'outside-only', url: 'http://go24.test/t/GO-1' });
const w = dom.window;
const doc = w.document;
const calls = [];
w.fetch = async (u, o = {}) => {
  calls.push([String(u), o.body ? String(o.body) : '']);
  const json = d => ({ ok: true, json: async () => d });
  if (String(u).startsWith('/disk/pick')) {
    return json({ drives: [{ which: 'my', name: 'Мой диск' }, { which: 'sh:r1', name: 'Общая (Анна)' }], which: 'my',
      path: [], folders: [{ id: 'f1', name: 'Сканы' }], files: [{ id: 'x1', name: 'отчёт.pdf', size_text: '3 КБ' }] });
  }
  return json({ redirect: '/t/GO-1#files' });
};
w.eval(js);
const tick = () => new Promise(r => setTimeout(r, 0));

const post = doc.querySelector('#post [data-disk-pick]');
post.click();
await tick();
let dlg = doc.querySelector('[data-disk-pick-dlg]');
assert.ok(dlg, 'the picker opens');
assert.equal(calls[0][0], '/disk/pick?which=my&folder=');
assert.equal(dlg.querySelectorAll('option').length, 2);
assert.match(dlg.textContent, /Сканы/);
dlg.querySelector('[data-file="x1"]').click();
await tick();
assert.equal(doc.querySelector('[data-disk-pick-dlg]'), null, 'the picker closes');
let inputs = doc.querySelectorAll('#post input[name="drive_file"]');
assert.equal(inputs.length, 1);
assert.equal(inputs[0].value, 'x1');
assert.match(doc.querySelector('#post [data-disk-picked]').textContent, /отчёт\.pdf/);
post.click(); await tick();
doc.querySelector('[data-disk-pick-dlg] [data-file="x1"]').click(); await tick();
assert.equal(doc.querySelectorAll('#post input[name="drive_file"]').length, 1, 'the same file once');
doc.querySelector('#post').reset();
await tick();
assert.equal(doc.querySelectorAll('#post input[name="drive_file"]').length, 0, 'cleared with the form');

doc.querySelector('#task [data-disk-pick]').click();
await tick();
doc.querySelector('[data-disk-pick-dlg] [data-file="x1"]').click();
await tick(); await tick();
assert.deepEqual(calls.at(-1), ['/t/GO-1/files/drive', 'file=x1']);
assert.equal(w.location.hash, '#files');
console.log('drivepick: ok');
