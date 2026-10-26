// CRM «Документы» → «Отправить»: the card's letter opens with the document attached, in jsdom. node test/js/crmdocsend.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/crmmail.js', import.meta.url), 'utf8');
const dom = new JSDOM(`<!doctype html><body>
  <button type="button" data-crmdoc-send="d1" data-name="Договор № 7 — Поставка.docx" data-pdf>send</button>
  <button type="button" data-crmdoc-send="d2" data-name="Акт.docx">send</button>
  <details data-crmmail="/crm/deals/x"><summary>Письмо</summary><form>
    <input name="subject" data-crmmail-subject><div class="row" data-crmmail-docs hidden></div></form></details>
  </body>`, { runScripts: 'outside-only', url: 'http://go24.test/crm/deals/x' });
const w = dom.window, doc = w.document;
w.HTMLElement.prototype.scrollIntoView = () => {};
w.eval(script);
const details = doc.querySelector('details'), box = doc.querySelector('[data-crmmail-docs]');
const docs = () => [...doc.querySelectorAll('input[name=doc]')].map(i => i.value);

doc.querySelector('[data-crmdoc-send="d1"]').click();
assert.ok(details.open, 'the letter is open');
assert.ok(!box.hidden);
assert.deepEqual(docs(), ['d1:pdf'], 'PDF by default');
assert.equal(doc.querySelector('[name=subject]').value, 'Договор № 7 — Поставка', 'the subject is the document');
const sel = box.querySelector('select');
sel.value = 'docx';
sel.dispatchEvent(new w.Event('change'));
assert.deepEqual(docs(), ['d1:docx'], 'DOCX chosen');
doc.querySelector('[data-crmdoc-send="d1"]').click();
assert.equal(docs().length, 1, 'the same document is attached once');

doc.querySelector('[data-crmdoc-send="d2"]').click();
assert.deepEqual(docs(), ['d1:docx', 'd2:docx'], 'no PDFs — DOCX');
assert.equal(doc.querySelector('[name=subject]').value, 'Договор № 7 — Поставка', 'a subject typed is kept');
for (const x of [...box.querySelectorAll('[data-crmdoc-remove]')]) x.click();
assert.deepEqual(docs(), []);
assert.ok(box.hidden, 'nothing attached — the row is hidden');
console.log('crmdocsend: ok');
