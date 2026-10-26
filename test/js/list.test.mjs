// Column picker of task lists in jsdom: node test/js/list.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/list.js', import.meta.url), 'utf8');
const dom = new JSDOM(`<!doctype html><body>
  <table><thead><tr><th><form method="post" action="/prefs/columns" data-colpick>
    <input type="checkbox" name="cols" value="status" checked><input type="checkbox" name="cols" value="closed"></form></th>
    <th data-col="status">S</th><th data-col="closed" hidden>C</th></tr></thead>
  <tbody><tr><td></td><td data-col="status">x</td><td data-col="closed" hidden>y</td></tr></tbody></table></body>`,
  { runScripts: 'outside-only', url: 'http://go24.test/my' });
const w = dom.window;
w.fetch = async (url, opts) => { w.lastRequest = { url, body: String(opts.body) }; return { ok: true, status: 204 }; };
w.eval(script);

const closed = w.document.querySelector('input[value=closed]');
closed.checked = true;
closed.dispatchEvent(new w.Event('change', { bubbles: true }));
const status = w.document.querySelector('input[value=status]');
status.checked = false;
status.dispatchEvent(new w.Event('change', { bubbles: true }));
await new Promise(r => setTimeout(r, 10));
w.document.querySelectorAll('[data-col=closed]').forEach(c => assert.equal(c.hidden, false));
w.document.querySelectorAll('[data-col=status]').forEach(c => assert.equal(c.hidden, true));
assert.equal(w.lastRequest.url, 'http://go24.test/prefs/columns');
assert.equal(w.lastRequest.body, 'cols=closed');
console.log('list.js: column picker passed');

// group actions: «all» ticks the list, the panel shows the count and only the field its action needs
{
  const dom = new JSDOM(`<!doctype html><body>
    <table><thead><tr><th><input type="checkbox" data-bulk-all></th></tr></thead><tbody>
      <tr><td><input type="checkbox" name="task" value="GO-1" form="bulk"></td></tr>
      <tr><td><input type="checkbox" name="task" value="GO-2" form="bulk"></td></tr></tbody></table>
    <form id="bulk" data-bulk><span data-bulk-count></span>
      <select name="op" data-bulk-op><option value="complete">c</option><option value="responsible">r</option></select>
      <select name="user_id" data-bulk-for="responsible accomplice"></select><input name="tag" data-bulk-for="tag"></form></body>`,
    { runScripts: 'outside-only', url: 'http://go24.test/my' });
  const w = dom.window;
  w.eval(script);
  const bar = w.document.querySelector('#bulk');
  const user = bar.querySelector('[name=user_id]');
  assert.equal(bar.hidden, true, 'nothing ticked: no panel');
  const all = w.document.querySelector('[data-bulk-all]');
  all.checked = true;
  all.dispatchEvent(new w.Event('change', { bubbles: true }));
  assert.equal(bar.hidden, false);
  assert.equal(bar.querySelector('[data-bulk-count]').textContent, '2');
  assert.equal(user.disabled, true, 'complete needs no person');
  const op = bar.querySelector('[data-bulk-op]');
  op.value = 'responsible';
  op.dispatchEvent(new w.Event('change', { bubbles: true }));
  assert.equal(user.disabled, false);
  assert.equal(bar.querySelector('[name=tag]').hidden, true);
  const one = w.document.querySelector('[value="GO-2"]');
  one.checked = false;
  one.dispatchEvent(new w.Event('change', { bubbles: true }));
  assert.equal(bar.querySelector('[data-bulk-count]').textContent, '1');
  console.log('list.js: group actions passed');
}
