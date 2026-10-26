// «Почта» → CRM: the search of a card in jsdom. node test/js/mailcrm.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/mailcrm.js', import.meta.url), 'utf8');
const dom = new JSDOM(`<!doctype html><body><form method="post" action="/mail/letters/m1/crm" data-crm-search>
  <input type="search" data-crm-q><input type="hidden" name="target" data-crm-target><div data-crm-res></div></form></body>`,
  { runScripts: 'outside-only', url: 'http://go24.test/mail' });
const w = dom.window;
const asked = [];
w.fetch = async (url) => {
  asked.push(String(url));
  return { ok: true, json: async () => [{ path: '/crm/deals/d1', kind: 'deal', kind_name: 'Сделки', title: 'Поставка', sub: 'ООО Ромашка' },
    { path: '/crm/contacts/c1', kind: 'contact', kind_name: 'Контакты', title: 'Иван', sub: '' }] };
};
let submitted = null;
w.HTMLFormElement.prototype.requestSubmit = function () { submitted = this.querySelector('[data-crm-target]').value; };
w.eval(script);
const q = w.document.querySelector('[data-crm-q]');
const box = w.document.querySelector('[data-crm-res]');
const type = async (v) => {
  q.value = v;
  q.dispatchEvent(new w.Event('input', { bubbles: true }));
  await new Promise(r => setTimeout(r, 320));
};

// one letter is not searched; two are, once the typing stops
await type('п');
assert.equal(asked.length, 0);
await type('по');
assert.equal(asked.length, 1);
assert.match(asked[0], /\/mail\/crm\/search\?q=%D0%BF%D0%BE$/);
const found = [...box.querySelectorAll('button')];
assert.deepEqual(found.map(b => b.textContent), ['Сделки: Поставка · ООО Ромашка', 'Контакты: Иван']);
assert.equal(found[0].type, 'button', 'a found card does not send the form by itself');

// a click links the letter to the card
found[1].click();
assert.equal(submitted, '/crm/contacts/c1');

// Enter takes the first card; an empty search clears the list
submitted = null;
q.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }));
assert.equal(submitted, '/crm/deals/d1');
await type('');
assert.equal(box.children.length, 0);
console.log('mailcrm.test.mjs: ok');
