// «Настроить карточку» of CRM cards in jsdom: node test/js/crmlayout.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/crmlayout.js', import.meta.url), 'utf8');
const li = c => `<li draggable="true" data-code="${c}"><span>${c}</span><button type="button" data-up></button><button type="button" data-down></button><button type="button" data-hide></button><button type="button" data-show></button></li>`;
const sec = (code, name, fields) => `<div class="crmlsec" data-sec data-code="${code}"><div class="row nowrap"><input type="text" class="crmlsec-name" value="${name}" placeholder="О сделке"><button type="button" data-sec-del></button></div>
  <ul class="crmlfields">${fields.map(li).join('')}</ul></div>`;
const dom = new JSDOM(`<!doctype html><body><form method="post" action="/crm/deals/d1/layout" data-crmlayout data-new-name="Новый раздел">
  <input type="hidden" name="layout" value="">
  <div class="crmlsecs">${sec('main', '', ['amount', 'responsible'])}${sec('more', '', ['f:7'])}</div>
  <button type="button" data-sec-add></button>
  <div class="crmlsec crmlhidden" data-hidden><ul class="crmlfields">${li('comment')}</ul></div>
  <button class="primary">save</button></form></body>`, { runScripts: 'outside-only', url: 'http://go24.test/crm/deals/d1?layout=1' });
const w = dom.window;
w.eval(script);
const doc = w.document;
const form = doc.querySelector('form');
const click = sel => doc.querySelector(sel).dispatchEvent(new w.MouseEvent('click', { bubbles: true }));
const result = () => {
  form.dispatchEvent(new w.Event('submit', { cancelable: true }));
  return JSON.parse(form.querySelector('input[name=layout]').value);
};

// the standard view as it is
assert.deepEqual(result(), { sections: [{ code: 'main', name: '', fields: ['amount', 'responsible'] }, { code: 'more', name: '', fields: ['f:7'] }], hidden: ['comment'] });

// ↓ at the bottom of «О сделке» goes to the top of «Дополнительно»; ↑ at the top of a section — to the end of the one above
click('[data-code=responsible] [data-down]');
click('[data-code="f:7"] [data-up]');
assert.deepEqual(result().sections.map(s => s.fields), [['amount'], ['f:7', 'responsible']]);
click('[data-code="f:7"] [data-up]');
assert.deepEqual(result().sections.map(s => s.fields), [['amount', 'f:7'], ['responsible']]);

// hidden and shown again (to the first section); the comment out of «Скрытые поля»
click('[data-code=amount] [data-hide]');
click('[data-code=comment] [data-show]');
assert.deepEqual(result(), { sections: [{ code: 'main', name: '', fields: ['f:7', 'comment'] }, { code: 'more', name: '', fields: ['responsible'] }], hidden: ['amount'] });

// a new section, named; a field dragged into it (empty — dropped below)
click('[data-sec-add]');
const added = doc.querySelectorAll('[data-sec]')[2];
assert.equal(added.querySelector('.crmlsec-name').value, 'Новый раздел');
added.querySelector('.crmlsec-name').value = 'Клиент';
const fire = (el, type) => el.dispatchEvent(new w.Event(type, { bubbles: true, cancelable: true }));
fire(doc.querySelector('[data-code=comment]'), 'dragstart');
fire(added.querySelector('.crmlfields'), 'dragover');
fire(doc.querySelector('[data-code=comment]'), 'dragend');
let r = result();
assert.deepEqual(r.sections[2], { code: '', name: 'Клиент', fields: ['comment'] });

// a custom section without a name keeps the standard one; deleting a section gives its fields to the one above
added.querySelector('.crmlsec-name').value = '';
assert.equal(result().sections[2].name, 'Новый раздел');
added.querySelector('[data-sec-del]').dispatchEvent(new w.MouseEvent('click', { bubbles: true }));
r = result();
assert.equal(r.sections.length, 2);
assert.deepEqual(r.sections[1].fields, ['responsible', 'comment']);
// the last section stays
doc.querySelectorAll('[data-sec-del]')[1].dispatchEvent(new w.MouseEvent('click', { bubbles: true }));
doc.querySelectorAll('[data-sec-del]')[0].dispatchEvent(new w.MouseEvent('click', { bubbles: true }));
r = result();
assert.equal(r.sections.length, 1);
assert.deepEqual(r.sections[0].fields, ['f:7', 'responsible', 'comment']);
console.log('crmlayout.js: card view editor passed');
