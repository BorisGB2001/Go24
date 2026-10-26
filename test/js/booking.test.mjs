// «Онлайн-запись»: a click on a free time of a resource opens a new booking at the slot under the pointer, a click
// on a booking — its card with its fields and «Удалить»; the client's CRM cards; the other resources of a booking;
// the waiting list (dragged onto a free time, «Записать», changed), in jsdom. node test/js/booking.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/booking.js', import.meta.url), 'utf8');
const dom = new JSDOM(`<!doctype html><body>
<div data-booking data-date="2026-10-26" data-hour0="9" data-hour-px="48">
  <div class="cg-col" data-resource="r1" data-slot="30" data-from="540" data-to="1080" data-break-from="780" data-break-to="840">
    <a href="#" data-bk="b1" data-time="10:00" data-minutes="60" data-name="Иван" data-phone="+7 900" data-email="" data-note="первый"
      data-status="confirmed" data-by="Анна">10:00</a></div>
  <div class="cg-col" data-resource="r2" data-slot="60" data-from="600" data-to="720"></div>
  <div class="cg-col off" data-resource="r3" data-slot="30" data-from="540" data-to="1080"></div>
</div>
<dialog data-booking-dlg><form method="post" action="/booking" data-new-action="/booking">
  <h3 data-title-new="Записать клиента" data-title-edit="Запись клиента"></h3>
  <select name="resource"><option value="r1" data-slot="30">1</option><option value="r2" data-slot="60">2</option><option value="r3" data-slot="30">3</option></select>
  <input name="time"><input name="minutes"><input name="client_name"><input name="client_phone"><input name="client_email">
  <textarea name="note"></textarea><select name="status"><option value="unconfirmed"></option><option value="confirmed"></option></select>
  <p data-booking-by data-label="Записал(а)"></p>
  <button type="button" data-booking-close></button><button data-booking-delete hidden></button>
</form></dialog></body>`, { runScripts: 'outside-only', url: 'http://go24.test/booking' });
const w = dom.window, d = w.document;
w.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
w.HTMLDialogElement.prototype.close = function () { this.open = false; };
w.eval(script);
const dlg = d.querySelector('dialog'), form = dlg.querySelector('form'), f = n => form.elements[n];
const [c1, c2, c3] = d.querySelectorAll('.cg-col');
const click = (el, y) => {
  el.getBoundingClientRect = () => ({ top: 0, left: 0 });
  el.dispatchEvent(new w.MouseEvent('click', { bubbles: true, clientY: y }));
};

// a free time: 11:40 under the pointer (2h40m below 9:00) → the 11:30 slot of 30 minutes
click(c1, 48 * 2 + 32);
assert.equal(dlg.open, true);
assert.equal(f('resource').value, 'r1');
assert.equal(f('time').value, '11:30');
assert.equal(f('minutes').value, '30');
assert.equal(form.getAttribute('action'), '/booking');
assert.equal(dlg.querySelector('h3').textContent, 'Записать клиента');
assert.equal(dlg.querySelector('[data-booking-delete]').hidden, true);

// the other resource: slots of an hour from 10:00; before its work — nothing
dlg.open = false;
click(c2, 48 * 2 + 40);
assert.equal(f('resource').value, 'r2');
assert.equal(f('time').value, '11:00');
assert.equal(f('minutes').value, '60');
dlg.open = false;
click(c2, 10);
assert.equal(dlg.open, false);
click(c3, 100); // a day off
assert.equal(dlg.open, false);
click(c1, 48 * 4 + 16); // 13:20 — the break of 13:00–14:00
assert.equal(dlg.open, false);
click(c1, 48 * 5 + 2); // 14:00 — after it
assert.equal(f('time').value, '14:00');
dlg.open = false;

// a booking: its card
click(c1.querySelector('[data-bk]'), 50);
assert.equal(dlg.open, true);
assert.equal(form.getAttribute('action'), '/booking/item/b1');
assert.equal(f('client_name').value, 'Иван');
assert.equal(f('minutes').value, '60');
assert.equal(f('note').value, 'первый');
assert.equal(f('status').value, 'confirmed');
assert.equal(dlg.querySelector('[data-booking-by]').textContent, 'Записал(а): Анна');
const del = dlg.querySelector('[data-booking-delete]');
assert.equal(del.hidden, false);
assert.equal(del.getAttribute('formaction'), '/booking/item/b1/delete');
dlg.querySelector('[data-booking-close]').click();
assert.equal(dlg.open, false);

// CRM: a booking's cards as chips, «Создать сделку» while it has no deal; the preset of «Записать» from a card goes to
// a new booking; ?b= opens the booking; typing finds cards (leads left out), a picked one binds, × unbinds
{
  const dom = new JSDOM(`<!doctype html><body>
<div data-booking data-date="2026-10-26" data-hour0="9" data-hour-px="48" data-open="b2">
  <div class="cg-col" data-resource="r1" data-slot="30" data-from="540" data-to="1080">
    <a href="#" data-bk="b2" data-time="10:00" data-minutes="30" data-name="Ольга" data-phone="" data-email="" data-note=""
      data-status="confirmed" data-contact-id="c1" data-contact="Ольга Петрова" data-company-id="" data-company="" data-deal-id="" data-deal="">10:00</a>
    <a href="#" data-bk="b3" data-time="11:00" data-minutes="30" data-name="Пётр" data-phone="" data-email="" data-note=""
      data-status="confirmed" data-contact-id="" data-contact="" data-company-id="" data-company="" data-deal-id="d1" data-deal="Визит">11:00</a></div>
</div>
<dialog data-booking-dlg data-preset-name="ООО Ромашка" data-preset-phone="+7 1" data-preset-email="r@x.ru" data-preset-contact-id=""
  data-preset-contact="" data-preset-company-id="k1" data-preset-company="Ромашка" data-preset-deal-id="" data-preset-deal="" data-preset-back="/crm/companies/k1">
<form method="post" action="/booking" data-new-action="/booking">
  <h3 data-title-new="Записать клиента" data-title-edit="Запись клиента"></h3>
  <select name="resource"><option value="r1" data-slot="30">1</option></select>
  <input name="time"><input name="minutes"><input name="client_name"><input name="client_phone"><input name="client_email">
  <textarea name="note"></textarea><select name="status"><option value="unconfirmed"></option><option value="confirmed"></option></select>
  <div data-bk-crm data-kind-contact="Контакт" data-kind-company="Компания" data-kind-deal="Сделка" data-unbind="Отвязать">
    <input type="hidden" name="contact_id"><input type="hidden" name="company_id"><input type="hidden" name="deal_id"><input type="hidden" name="crm_back" disabled>
    <div data-bk-chips></div><input type="search" data-bk-crm-q><div data-bk-crm-res></div></div>
  <p data-booking-by data-label="Записал(а)"></p>
  <button type="button" data-booking-close></button><button data-booking-deal hidden></button><button data-booking-delete hidden></button>
</form></dialog><button data-booking-new></button></body>`, { runScripts: 'outside-only', url: 'http://go24.test/booking?b=b2' });
  const w = dom.window, d = w.document;
  w.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
  w.HTMLDialogElement.prototype.close = function () { this.open = false; };
  w.CSS = { escape: s => s };
  w.fetch = async () => ({ ok: true, json: async () => [
    { kind: 'lead', path: '/crm/leads/l1', kind_name: 'Лид', title: 'Лид Ольги' },
    { kind: 'deal', path: '/crm/deals/d9', kind_name: 'Сделка', title: 'Абонемент', sub: '' }] });
  w.eval(script);
  const dlg = d.querySelector('dialog'), form = dlg.querySelector('form'), f = n => form.elements[n];
  const chips = () => [...dlg.querySelectorAll('[data-bk-chips] a')].map(a => a.textContent + ' ' + a.getAttribute('href'));
  const deal = dlg.querySelector('[data-booking-deal]');
  assert.equal(dlg.open, true, '?b= opens the booking');
  assert.equal(form.getAttribute('action'), '/booking/item/b2');
  assert.equal(f('contact_id').value, 'c1');
  assert.deepEqual(chips(), ['Контакт: Ольга Петрова /crm/contacts/c1']);
  assert.equal(deal.hidden, false);
  assert.equal(deal.getAttribute('formaction'), '/booking/item/b2/deal');
  assert.equal(f('crm_back').disabled, true);

  // the search: leads are not offered; a deal picked binds, × takes the contact off
  const q = dlg.querySelector('[data-bk-crm-q]');
  q.value = 'Абон';
  q.dispatchEvent(new w.Event('input'));
  await new Promise(r => setTimeout(r, 400));
  const found = [...dlg.querySelectorAll('[data-bk-crm-res] button')];
  assert.deepEqual(found.map(b => b.textContent), ['Сделка: Абонемент']);
  found[0].click();
  assert.equal(f('deal_id').value, 'd9');
  assert.equal(q.value, '');
  assert.deepEqual(chips(), ['Контакт: Ольга Петрова /crm/contacts/c1', 'Сделка: Абонемент /crm/deals/d9']);
  dlg.querySelector('[data-bk-chips] button[data-unbind="contact"]').click();
  assert.equal(f('contact_id').value, '');
  assert.deepEqual(chips(), ['Сделка: Абонемент /crm/deals/d9']);

  // a booking with a deal: no «Создать сделку»
  d.querySelectorAll('[data-bk]')[1].dispatchEvent(new w.MouseEvent('click', { bubbles: true }));
  assert.equal(deal.hidden, true);
  assert.deepEqual(chips(), ['Сделка: Визит /crm/deals/d1']);

  // a new one: the preset's client and company, back to the card
  d.querySelector('[data-booking-new]').click();
  assert.equal(form.getAttribute('action'), '/booking');
  assert.equal(f('client_name').value, 'ООО Ромашка');
  assert.equal(f('client_email').value, 'r@x.ru');
  assert.equal(f('company_id').value, 'k1');
  assert.equal(f('deal_id').value, '');
  assert.deepEqual(chips(), ['Компания: Ромашка /crm/companies/k1']);
  assert.equal(f('crm_back').disabled, false);
  assert.equal(f('crm_back').value, '/crm/companies/k1');
  assert.equal(deal.hidden, true);
}
// the other resources and the waiting list
{
  const dom = new JSDOM(`<!doctype html><body>
<section data-bk-wait><button type="button" data-wait-new></button>
  <div data-wait="w1" draggable="true" data-res="r2" data-name="Мария" data-phone="+7 901" data-email="" data-note="утром"
    data-contact-id="" data-contact="" data-company-id="" data-company="" data-deal-id="" data-deal=""><a href="#" data-wait-edit>Мария</a><button type="button" data-wait-book></button></div>
  <form method="post" action="#" data-wait-drop data-native hidden><input name="resource"><input name="date" value="2026-10-26"><input name="time"></form>
</section>
<div data-booking data-date="2026-10-26" data-hour0="9" data-hour-px="48">
  <div class="cg-col" data-resource="r1" data-slot="30" data-from="540" data-to="1080">
    <a href="#" data-bk="b1" data-res="r1" data-extras="r2" data-time="10:00" data-minutes="60" data-name="Иван" data-phone="" data-email="" data-note=""
      data-status="confirmed" data-by="">10:00</a></div>
  <div class="cg-col" data-resource="r2" data-slot="60" data-from="600" data-to="720">
    <a href="#" class="bk-extra" data-bk="b1" data-res="r1" data-extras="r2" data-time="10:00" data-minutes="60" data-name="Иван" data-phone="" data-email="" data-note=""
      data-status="confirmed" data-by="">10:00</a></div>
  <div class="cg-col off" data-resource="r3" data-slot="30" data-from="540" data-to="1080"></div>
</div>
<dialog data-booking-dlg><form method="post" action="/booking" data-new-action="/booking">
  <h3 data-title-new="Записать клиента" data-title-edit="Запись клиента"></h3><input type="hidden" name="wait" disabled>
  <select name="resource"><option value="r1" data-slot="30">1</option><option value="r2" data-slot="60">2</option><option value="r3" data-slot="30">3</option></select>
  <label><input type="checkbox" name="extra" value="r1"></label><label><input type="checkbox" name="extra" value="r2"></label><label><input type="checkbox" name="extra" value="r3"></label>
  <input name="time"><input name="minutes"><input name="client_name"><input name="client_phone"><input name="client_email">
  <textarea name="note"></textarea><select name="status"><option value="unconfirmed"></option><option value="confirmed"></option></select>
  <p data-booking-by data-label="Записал(а)"></p>
  <button type="button" data-booking-close></button><button data-booking-towait hidden></button><button data-booking-delete hidden></button>
</form></dialog>
<dialog data-wait-dlg><form method="post" action="/booking/wait" data-new-action="/booking/wait"><h3 data-title-new="В лист" data-title-edit="Клиент"></h3>
  <input name="client_name"><input name="client_phone"><input name="client_email"><textarea name="note"></textarea>
  <select name="resource"><option value=""></option><option value="r1"></option><option value="r2"></option></select>
  <button type="button" data-wait-close></button></form></dialog></body>`, { runScripts: 'outside-only', url: 'http://go24.test/booking' });
  const w = dom.window, d = w.document;
  w.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
  w.HTMLDialogElement.prototype.close = function () { this.open = false; };
  w.CSS = { escape: s => s };
  let sent = null;
  w.HTMLFormElement.prototype.submit = function () { sent = { action: this.getAttribute('action'), ...Object.fromEntries(new w.FormData(this)) }; };
  w.eval(script);
  const dlg = d.querySelector('[data-booking-dlg]'), form = dlg.querySelector('form'), f = n => form.elements[n];
  const checked = () => [...form.querySelectorAll('[name=extra]:checked')].map(x => x.value);
  const own = () => [...form.querySelectorAll('[name=extra]')].filter(x => x.disabled).map(x => x.value);
  const [c1, c2, c3] = d.querySelectorAll('.cg-col');

  // a booking of r1 with the room r2, opened in the room's column: still r1's, r2 ticked, r1 not offered
  c2.querySelector('[data-bk]').dispatchEvent(new w.MouseEvent('click', { bubbles: true }));
  assert.equal(f('resource').value, 'r1');
  assert.deepEqual(checked(), ['r2']);
  assert.deepEqual(own(), ['r1']);
  const tw = dlg.querySelector('[data-booking-towait]');
  assert.equal(tw.hidden, false);
  assert.equal(tw.getAttribute('formaction'), '/booking/item/b1/wait');
  assert.equal(f('wait').disabled, true);
  // the resource changed to r2: r2 is not one of the others any more
  f('resource').value = 'r2';
  f('resource').dispatchEvent(new w.Event('change'));
  assert.deepEqual(checked(), []);
  assert.deepEqual(own(), ['r2']);
  // a new booking: nothing ticked, no «В лист ожидания»
  dlg.open = false;
  c1.getBoundingClientRect = () => ({ top: 0, left: 0 });
  c1.dispatchEvent(new w.MouseEvent('click', { bubbles: true, clientY: 48 * 3 }));
  assert.equal(f('resource').value, 'r1');
  assert.deepEqual(checked(), []);
  assert.equal(tw.hidden, true);

  // «Записать» from the waiting list: the client, the resource wanted from the start of its work, the entry sent along
  dlg.open = false;
  const item = d.querySelector('[data-wait]');
  item.querySelector('[data-wait-book]').click();
  assert.equal(dlg.open, true);
  assert.equal(form.getAttribute('action'), '/booking');
  assert.equal(f('resource').value, 'r2');
  assert.equal(f('time').value, '10:00');
  assert.equal(f('minutes').value, '60');
  assert.equal(f('client_name').value, 'Мария');
  assert.equal(f('note').value, 'утром');
  assert.equal(f('wait').disabled, false);
  assert.equal(f('wait').value, 'w1');

  // dragged onto 11:20 of r1 — booked at 11:00; onto a day off or before the work — nothing
  const drag = (type, el, y) => {
    const e = new w.Event(type, { bubbles: true, cancelable: true });
    e.clientY = y;
    el.dispatchEvent(e);
    return e;
  };
  c2.getBoundingClientRect = c3.getBoundingClientRect = () => ({ top: 0, left: 0 });
  drag('dragstart', item);
  assert.equal(drag('dragover', c3, 100).defaultPrevented, false);
  assert.equal(drag('dragover', c2, 10).defaultPrevented, false);
  assert.equal(drag('dragover', c1, 48 * 2 + 16).defaultPrevented, true);
  assert.equal(c1.classList.contains('bk-drop'), true);
  drag('drop', c1, 48 * 2 + 16);
  assert.deepEqual(sent, { action: '/booking/wait/w1/book', resource: 'r1', date: '2026-10-26', time: '11:00' });
  drag('dragend', item);
  assert.equal(c1.classList.contains('bk-drop'), false);
  sent = null;
  drag('drop', c1, 48 * 2 + 16); // not dragging any more
  assert.equal(sent, null);

  // the entry changed in its dialog; a new one — empty
  const wdlg = d.querySelector('[data-wait-dlg]'), wform = wdlg.querySelector('form');
  item.querySelector('[data-wait-edit]').click();
  assert.equal(wdlg.open, true);
  assert.equal(wform.getAttribute('action'), '/booking/wait/w1');
  assert.equal(wform.elements.client_phone.value, '+7 901');
  assert.equal(wform.elements.resource.value, 'r2');
  assert.equal(wdlg.querySelector('h3').textContent, 'Клиент');
  d.querySelector('[data-wait-new]').click();
  assert.equal(wform.getAttribute('action'), '/booking/wait');
  assert.equal(wform.elements.client_name.value, '');
  assert.equal(wform.elements.resource.value, '');
  wdlg.querySelector('[data-wait-close]').click();
  assert.equal(wdlg.open, false);
}

// the services of a resource: only its own are offered; checking them sets the minutes to theirs together and shows
// the total; a booking's card has its services checked
{
  const dom = new JSDOM(`<!doctype html><html lang="ru-RU"><body>
<div data-booking data-date="2026-10-26" data-hour0="9" data-hour-px="48">
  <div class="cg-col" data-resource="r1" data-slot="30" data-from="540" data-to="1080">
    <a href="#" data-bk="b1" data-res="r1" data-services="s2" data-time="10:00" data-minutes="45" data-name="Иван" data-status="confirmed">10:00</a></div>
  <div class="cg-col" data-resource="r2" data-slot="60" data-from="540" data-to="1080"></div>
</div>
<dialog data-booking-dlg><form method="post" action="/booking" data-new-action="/booking">
  <h3 data-title-new="н" data-title-edit="к"></h3>
  <select name="resource"><option value="r1" data-slot="30">1</option><option value="r2" data-slot="60">2</option></select>
  <div data-bk-svcs hidden><div>
    <label data-svc-res="r1"><input type="checkbox" name="svc" value="s1" data-minutes="90" data-price="3000" data-currency="RUB"></label>
    <label data-svc-res="r1"><input type="checkbox" name="svc" value="s2" data-minutes="45" data-price="1500.5" data-currency="RUB"></label>
    <label data-svc-res="r2"><input type="checkbox" name="svc" value="s3" data-minutes="60" data-price="100" data-currency="RUB"></label></div>
    <p data-bk-total data-label="Итого"></p></div>
  <input name="time"><input name="minutes"><input name="client_name"><input name="client_phone"><input name="client_email">
  <textarea name="note"></textarea><select name="status"><option value="unconfirmed"></option><option value="confirmed"></option></select>
  <p data-booking-by data-label="Записал(а)"></p>
  <button type="button" data-booking-close></button><button data-booking-delete hidden></button>
</form></dialog></body></html>`, { runScripts: 'outside-only', url: 'http://go24.test/booking' });
  const w = dom.window, d = w.document;
  w.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
  w.HTMLDialogElement.prototype.close = function () { this.open = false; };
  w.eval(script);
  const form = d.querySelector('form'), f = n => form.elements[n], box = d.querySelector('[data-bk-svcs]');
  const [s1, s2, s3] = d.querySelectorAll('[name=svc]'), total = d.querySelector('[data-bk-total]');
  const [c1, c2] = d.querySelectorAll('.cg-col');
  const click = (el, y) => {
    el.getBoundingClientRect = () => ({ top: 0, left: 0 });
    el.dispatchEvent(new w.MouseEvent('click', { bubbles: true, clientY: y }));
  };
  click(c1, 48 * 2);
  assert.equal(box.hidden, false);
  assert.deepEqual([s1, s2, s3].map(x => x.disabled), [false, false, true]);
  assert.equal(s3.closest('label').hidden, true);
  assert.equal(f('minutes').value, '30');
  s1.checked = true;
  s1.dispatchEvent(new w.Event('change'));
  s2.checked = true;
  s2.dispatchEvent(new w.Event('change'));
  assert.equal(f('minutes').value, '135');
  assert.match(total.textContent, /^Итого: 4\s?500,50 RUB$/);
  // the other resource: its own service only, the checked ones of the first are off
  f('resource').value = 'r2';
  f('resource').dispatchEvent(new w.Event('change'));
  assert.deepEqual([s1, s2, s3].map(x => x.checked), [false, false, false]);
  assert.equal(s3.disabled, false);
  assert.equal(total.textContent, '');
  s3.checked = true;
  s3.dispatchEvent(new w.Event('change'));
  assert.equal(total.textContent, 'Итого: 100 RUB');
  assert.equal(f('minutes').value, '60');
  // a booking: its services checked, its minutes kept
  d.querySelector('dialog').open = false;
  click(c1.querySelector('[data-bk]'), 50);
  assert.deepEqual([s1, s2].map(x => x.checked), [false, true]);
  assert.equal(f('minutes').value, '45');
  assert.match(total.textContent, /1\s?500,50 RUB/);
  void c2;
}
console.log('booking.test.mjs: ok');
