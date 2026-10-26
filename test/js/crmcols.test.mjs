// «Настроить колонки» of CRM lists in jsdom: node test/js/crmcols.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const list = readFileSync(new URL('../../internal/web/static/js/list.js', import.meta.url), 'utf8');
const script = readFileSync(new URL('../../internal/web/static/js/crmcols.js', import.meta.url), 'utf8');
const dom = new JSDOM(`<!doctype html><body>
  <form method="post" action="/crm/columns" id="crmcols" data-colpick data-crmcols hidden><input type="hidden" name="scope" value="deal"></form>
  <form method="post" action="/crm/bulk/deals" data-crmbulk>
  <table><thead><tr><th class="gearcol"><details class="colpick crmcols"><summary></summary><div class="pop"><ul class="crmcols-list">
    <li draggable="true"><input type="checkbox" name="cols" value="stage" form="crmcols" checked></li>
    <li draggable="true"><input type="checkbox" name="cols" value="amount" form="crmcols" checked></li>
    <li draggable="true"><input type="checkbox" name="cols" value="close_date" form="crmcols"></li></ul></div></details></th>
    <th>T</th><th data-col="stage">S</th><th data-col="amount">A</th><th data-col="close_date" hidden>C</th></tr></thead>
  <tbody><tr><td></td><td>x</td><td data-col="stage">s</td><td data-col="amount">a</td><td data-col="close_date" hidden>c</td></tr></tbody></table></form></body>`,
  { runScripts: 'outside-only', url: 'http://go24.test/crm/deals?view=list' });
const w = dom.window;
const sent = [];
w.fetch = async (url, opts) => { sent.push({ url, body: String(opts.body) }); return { ok: true, status: 204 }; };
w.eval(list);
w.eval(script);
const doc = w.document;
const order = sel => [...doc.querySelector(sel).querySelectorAll('[data-col]')].map(c => c.dataset.col).join(',');

// the gear sits in the list of group actions (a form): its boxes belong to its own form by form="crmcols";
// ticking shows the column at once and saves the choice in the gear's order (list.js)
const close = doc.querySelector('input[value=close_date]');
close.checked = true;
close.dispatchEvent(new w.Event('change', { bubbles: true }));
await new Promise(r => setTimeout(r, 10));
doc.querySelectorAll('[data-col=close_date]').forEach(c => assert.equal(c.hidden, false));
assert.equal(sent.at(-1).body, 'scope=deal&cols=stage&cols=amount&cols=close_date');

// «Дата завершения» dragged above «Стадия»: the columns of every row follow, the order is saved
const li = [...doc.querySelectorAll('.crmcols-list li')];
li[0].getBoundingClientRect = () => ({ top: 0, height: 20 });
const fire = (el, type, y) => {
  const e = new w.Event(type, { bubbles: true, cancelable: true });
  e.clientY = y;
  el.dispatchEvent(e);
};
fire(li[2], 'dragstart');
fire(li[0], 'dragover', 2);
fire(li[2], 'dragend');
await new Promise(r => setTimeout(r, 10));
assert.equal(order('thead tr'), 'close_date,stage,amount');
assert.equal(order('tbody tr'), 'close_date,stage,amount');
assert.equal(sent.at(-1).url, 'http://go24.test/crm/columns');
assert.equal(sent.at(-1).body, 'scope=deal&cols=close_date&cols=stage&cols=amount');
console.log('crmcols.js: column picker passed');
