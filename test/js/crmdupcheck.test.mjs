// CRM «Контроль дубликатов» while a form is filled: the bar «Найдены совпадения» in jsdom. node test/js/crmdupcheck.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/crmdupcheck.js', import.meta.url), 'utf8');
const dom = new JSDOM(`<!doctype html><body>
  <form method="post" action="/crm/leads/new" data-dupcheck="lead"><input name="title"><input name="name"><input name="company_title">
    <input type="tel" name="phone"><input type="email" name="email"><p><button class="primary">Создать</button></p></form>
  <form method="post" action="/crm/contacts/c1" data-dupcheck="contact" data-dup-id="c1"><input name="name" value="Иван">
    <input type="hidden" name="comm_kind" value="phone"><input type="tel" name="comm_value" value="+7 900"><input type="tel" name="comm_value" value="">
    <p class="row"><button class="primary">Сохранить</button><a href="/crm/contacts/c1">Отмена</a></p></form>
  <form method="post" action="/crm/companies/k1/requisites" data-dupcheck="company" data-dup-only="inn" data-dup-id="k1">
    <input name="name" value="ООО"><input name="inn"><button>Сохранить</button></form>
  </body>`, { runScripts: 'outside-only', url: 'http://go24.test/crm/leads' });
const w = dom.window;
const asked = [];
let answer = { count: 0, title: '', items: [] };
w.fetch = async url => {
  asked.push(new URL(url, 'http://go24.test').searchParams);
  return { ok: true, json: async () => answer };
};
w.eval(script);
w.eval(script); // loaded twice: still one check
const wait = ms => new Promise(r => setTimeout(r, ms));
const type = (el, v) => { el.value = v; el.dispatchEvent(new w.Event('input', { bubbles: true })); };
const [lead, contact, req] = w.document.querySelectorAll('form');

// a new lead: typing waits a moment, then asks once with all that is typed; the bar lists the cards
answer = { count: 2, title: 'Найдены совпадения: 2', items: [
  { URL: '/crm/contacts/c9', Title: 'Иван Петров', Kind: 'Контакт', Responsible: 'Анна', By: 'тот же телефон' },
  { URL: '/crm/companies/k9', Title: '<b>Ромашка</b>', Kind: 'Компания', Responsible: '', By: 'то же название' }] };
type(lead.elements.title, 'Заявка');
type(lead.elements.name, 'Иван Петров');
type(lead.elements.company_title, 'Ромашка');
type(lead.elements.phone, '8 900 123-45-67');
assert.equal(asked.length, 0, 'not at once');
await wait(600);
assert.equal(asked.length, 1);
assert.deepEqual(Object.fromEntries(asked[0]), { entity: 'lead', name: 'Иван Петров', company: 'Ромашка', phone: '8 900 123-45-67' });
const bar = lead.querySelector('.dupcheck');
assert.ok(bar && !bar.hidden, 'the bar is shown');
assert.equal(bar.nextElementSibling, lead.querySelector('p'), 'above the button');
assert.equal(bar.querySelector('.dupcheck-head').textContent, '⚠ Найдены совпадения: 2');
const links = bar.querySelectorAll('a');
assert.deepEqual([...links].map(a => [a.getAttribute('href'), a.target, a.textContent]),
  [['/crm/contacts/c9', '_blank', 'Иван Петров'], ['/crm/companies/k9', '_blank', '<b>Ромашка</b>']]);
assert.equal(bar.querySelector('b'), null, 'titles are text');
assert.equal(bar.querySelector('li span').textContent, ' Контакт · тот же телефон · Анна');

// the same again is not asked; the title (not a client field) changes nothing to ask
type(lead.elements.title, 'Заявка 2');
await wait(600);
assert.equal(asked.length, 1);

// nothing found: the bar hides
answer = { count: 0, title: '', items: [] };
type(lead.elements.phone, '');
type(lead.elements.name, '');
type(lead.elements.company_title, '');
await wait(600);
assert.equal(asked.length, 1, 'nothing typed: no request');
assert.ok(bar.hidden);

// a card being changed: its id and all its phones; the bar goes above the buttons' row
type(contact.querySelectorAll('[name=comm_value]')[1], '+7 901 000-00-00');
await wait(600);
assert.deepEqual([...asked[1].entries()], [['entity', 'contact'], ['id', 'c1'], ['name', 'Иван'], ['phone', '+7 900'], ['phone', '+7 901 000-00-00']]);
assert.equal(contact.querySelector('.dupcheck').nextElementSibling, contact.querySelector('p.row'));

// a requisite: the ИНН only, a change asks at once
req.elements.inn.value = '7707083893';
req.elements.inn.dispatchEvent(new w.Event('change', { bubbles: true }));
await wait(10);
assert.deepEqual(Object.fromEntries(asked[2]), { entity: 'company', id: 'k1', inn: '7707083893' });
console.log('crmdupcheck: ok');
