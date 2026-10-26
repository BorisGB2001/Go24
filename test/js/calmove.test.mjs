// Dragging on the calendar in jsdom: a meeting moved to another day in the month keeps its time and length; in the
// week a meeting of one's own is moved down by the grid, someone else's is not; a click on an empty place opens a new
// meeting at that time. node test/js/calmove.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/calendar.js', import.meta.url), 'utf8');
const page = body => {
  const dom = new JSDOM(`<!doctype html><body>${body}</body>`, { runScripts: 'outside-only', url: 'http://go24.test/calendar' });
  const w = dom.window, posts = [];
  w.fetch = async (u, o) => { posts.push({ url: u, body: Object.fromEntries(o.body) }); return { ok: true, json: async () => ({}) }; };
  let went = '';
  w.nav = { reload() { went = 'reload'; }, set href(v) { went = v; }, get href() { return went; }, pathname: '/calendar', search: '?view=week' };
  return { w, posts, went: () => went };
};
const tick = () => new Promise(r => setTimeout(r, 10));

// the month: drag to Friday
{
  const { w, posts, went } = page(`<div class="cal" data-calmove data-me="me">
    <div class="calday" data-date="2026-10-21"><a class="calitem" data-id="E1" data-owner="me" data-start="2026-10-21T10:00" data-end="2026-10-21T11:30" href="/calendar/E1">x</a>
      <a class="calitem" data-id="E2" data-owner="other" data-start="2026-10-21T12:00" data-end="2026-10-21T13:00" href="/calendar/E2">y</a></div>
    <div class="calday" data-date="2026-10-23"></div></div>`);
  w.eval(`((location) => {${script}})(nav)`);
  const [mine, theirs] = w.document.querySelectorAll('.calitem');
  assert.equal(mine.draggable, true);
  assert.equal(theirs.draggable, false, 'only the organizer drags');
  const ev = (type, target) => { const e = new w.Event(type, { bubbles: true, cancelable: true }); e.dataTransfer = { setData() {} }; target.dispatchEvent(e); return e; };
  ev('dragstart', mine);
  const fri = w.document.querySelectorAll('.calday')[1];
  assert.equal(ev('dragover', fri).defaultPrevented, true);
  ev('drop', fri);
  await tick();
  assert.deepEqual(posts, [{ url: '/calendar/E1/move', body: { start: '2026-10-23T10:00', end: '2026-10-23T11:30' } }]);
  assert.equal(went(), 'reload');
}

// the week: 7:00 at the top, 48 px an hour
{
  const { w, posts, went } = page(`<div class="calgrid" data-calmove data-me="me" data-hour0="7" data-hour-px="48">
    <div class="cg-col" data-date="2026-10-21">
      <a class="cg-ev" data-id="S1" data-owner="me" data-start="2026-10-21T10:00" data-end="2026-10-21T11:00" data-at="2026-10-21T10:00" href="/calendar/S1">x</a>
      <a class="cg-ev" data-id="E2" data-owner="other" data-start="2026-10-21T12:00" data-end="2026-10-21T13:00" href="/calendar/E2">y</a></div></div>`);
  w.eval(`((location) => {${script}})(nav)`);
  const [mine, theirs] = w.document.querySelectorAll('.cg-ev');
  assert.ok(mine.classList.contains('movable') && !theirs.classList.contains('movable'));
  let top = 144; // 10:00
  Object.defineProperty(mine, 'offsetTop', { get: () => top });
  Object.defineProperty(mine, 'offsetHeight', { get: () => 48 });
  mine.getBoundingClientRect = () => ({ top: 144, bottom: 192 });
  const ptr = (type, target, y) => target.dispatchEvent(new w.MouseEvent(type, { bubbles: true, cancelable: true, button: 0, clientX: 5, clientY: y }));
  ptr('pointerdown', mine, 150);
  ptr('pointermove', w.document, 150 + 26); // half an hour down: 26 px snap to 24
  assert.equal(mine.style.top, '168px');
  top = 168;
  ptr('pointerup', w.document, 180);
  await tick();
  assert.deepEqual(posts.at(-1), { url: '/calendar/S1/move', body: { start: '2026-10-21T10:30', end: '2026-10-21T11:30', at: '2026-10-21T10:00' } });
  assert.equal(went(), 'reload');

  const n = posts.length;
  ptr('pointerdown', theirs, 300);
  ptr('pointermove', w.document, 350);
  ptr('pointerup', w.document, 350);
  assert.equal(posts.length, n, "someone else's meeting does not move");

  const col = w.document.querySelector('.cg-col');
  col.getBoundingClientRect = () => ({ top: 0, bottom: 720 });
  ptr('pointerdown', col, 100); // 9:00 (100 − 6 px → 94 → 96 px)
  ptr('pointerup', w.document, 100);
  assert.equal(went(), '/calendar/new?start=2026-10-21T09%3A00&end=2026-10-21T10%3A00');
}

// the panel of calendars: a project's calendar is added from the meetings shown; a hidden calendar hides its items and
// a day of the schedule left empty; the choice stays in the browser
{
  const { w } = page(`<div class="calcals" data-calcals><label><input type="checkbox" value="personal"></label><label><input type="checkbox" value="company"></label></div>
    <div class="cl-day" id="d1"><div class="cl-item"><a class="calitem" data-cal="company">a</a></div></div>
    <div class="cl-day" id="d2"><div class="cl-item"><a class="calitem" data-cal="p:GO" data-cal-name="Go24-Dev">b</a></div>
      <div class="cl-item"><a class="calitem" data-cal="personal">c</a></div></div>`);
  w.eval(`((location) => {${script}})(nav)`);
  const boxes = () => [...w.document.querySelectorAll('[data-calcals] input')];
  assert.deepEqual(boxes().map(b => b.value), ['personal', 'company', 'p:GO']);
  assert.ok(boxes().every(b => b.checked));
  const company = boxes()[1];
  company.checked = false;
  company.dispatchEvent(new w.Event('change', { bubbles: true }));
  assert.equal(w.document.querySelector('[data-cal="company"]').hidden, true);
  assert.equal(w.document.getElementById('d1').hidden, true, 'a day with nothing shown is hidden');
  assert.equal(w.document.getElementById('d2').hidden, false);
  assert.deepEqual(JSON.parse(w.localStorage.getItem('go24.calendar.hidden')), ['company']);
}
// one's own calendars (GO24DEV-423): «Добавить календарь» opens the dialog empty with a colour of the palette; a
// calendar's pencil — with its name and colour, and «Удалить» to its address; the main one — its colour only
{
  const { w } = page(`<div class="calcals" data-calcals>
      <span class="calsec"><label><input type="checkbox" value="personal"></label><button type="button" data-cal-edit="main" data-color=""></button></span>
      <span class="calsec"><label><input type="checkbox" value="c:7"></label><button type="button" data-cal-edit="7" data-name="Рабочий" data-color="#e89b06"></button></span>
      <button type="button" class="calsec-add" data-cal-edit=""></button></div>
    <dialog data-cal-dlg data-t-new="Добавить" data-t-edit="Изменить" data-t-main="Мой"><form method="post" action="/calendar/sections">
      <input type="hidden" name="id"><input type="hidden" name="back"><h3 data-cal-dlg-title></h3><input type="text" name="name">
      <label data-main-only><input type="radio" name="color" value=""></label><label><input type="radio" name="color" value="#86b100"></label>
      <label><input type="radio" name="color" value="#e89b06"></label>
      <button type="submit" form="cal-sec-del" data-cal-del></button><button type="button" data-cal-close></button></form>
      <form method="post" id="cal-sec-del"></form></dialog>`);
  w.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
  w.HTMLDialogElement.prototype.close = function () { this.open = false; };
  w.eval(`((location) => {${script}})(nav)`);
  const d = w.document, f = d.querySelector('[data-cal-dlg] form'), color = () => f.querySelector('input[name=color]:checked').value;
  d.querySelector('.calsec-add').click();
  assert.equal(d.querySelector('[data-cal-dlg]').open, true);
  assert.equal(f.elements.id.value, '');
  assert.equal(f.elements.back.value, '/calendar?view=week');
  assert.equal(d.querySelector('[data-cal-dlg-title]').textContent, 'Добавить');
  assert.equal(color(), '#86b100', 'a new calendar takes a colour of the palette');
  assert.equal(d.querySelector('[data-cal-del]').hidden, true);
  assert.equal(d.querySelector('[data-main-only]').hidden, true);
  d.querySelector('[data-cal-edit="7"]').click();
  assert.equal(f.elements.name.value, 'Рабочий');
  assert.equal(color(), '#e89b06');
  assert.equal(d.querySelector('[data-cal-del]').hidden, false);
  assert.ok(d.getElementById('cal-sec-del').action.endsWith('/calendar/sections/7/delete'));
  d.querySelector('[data-cal-edit="main"]').click();
  assert.equal(f.elements.name.hidden, true);
  assert.equal(f.elements.name.required, false);
  assert.equal(color(), '', 'the main calendar without a colour: that of the theme');
  assert.equal(d.querySelector('[data-cal-dlg-title]').textContent, 'Мой');
  d.querySelector('[data-cal-close]').click();
  assert.equal(d.querySelector('[data-cal-dlg]').open, false);
  // the panel's checkboxes still show and hide: the dialog's radios are not among them
  assert.deepEqual([...d.querySelectorAll('[data-calcals] input[type=checkbox]')].map(b => b.checked), [true, true]);
}
// calendars of colleagues (GO24DEV-516): a colleague's meetings hide with their checkbox; «Календари сотрудников»
// opens its dialog, the forms come back to this page; a click on the grid of a colleague's calendar invites them
{
  const { w, went } = page(`<div class="calcals" data-calcals><label><input type="checkbox" value="personal"></label>
      <span class="calsec calperson"><label><input type="checkbox" value="u:42"></label><a href="/calendar?user=42">Анна</a>
        <form method="post" action="/calendar/people/42/delete"><input type="hidden" name="back" data-cal-back></form></span>
      <button type="button" data-cal-people></button></div>
    <a class="calitem" data-cal="u:42" id="theirs">x</a><a class="calitem" data-cal="personal" id="mine">y</a>
    <dialog data-cal-people-dlg><form method="post" action="/calendar/people"><input type="hidden" name="back" data-cal-back>
      <button type="button" data-cal-close></button></form></dialog>
    <div class="calgrid" data-calmove data-me="me" data-hour0="7" data-hour-px="48" data-user="42">
      <div class="cg-col" data-date="2026-10-14"></div></div>`);
  w.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
  w.HTMLDialogElement.prototype.close = function () { this.open = false; };
  w.eval(`((location) => {${script}})(nav)`);
  const d = w.document;
  assert.deepEqual([...d.querySelectorAll('[data-cal-back]')].map(i => i.value), ['/calendar?view=week', '/calendar?view=week']);
  const box = d.querySelector('input[value="u:42"]');
  box.checked = false;
  box.dispatchEvent(new w.Event('change', { bubbles: true }));
  assert.equal(d.getElementById('theirs').hidden, true);
  assert.equal(d.getElementById('mine').hidden, false);
  d.querySelector('[data-cal-people]').click();
  assert.equal(d.querySelector('[data-cal-people-dlg]').open, true);
  d.querySelector('[data-cal-people-dlg] [data-cal-close]').click();
  assert.equal(d.querySelector('[data-cal-people-dlg]').open, false);
  const col = d.querySelector('.cg-col');
  col.getBoundingClientRect = () => ({ top: 0, bottom: 720 });
  const ptr = (type, target, y) => target.dispatchEvent(new w.MouseEvent(type, { bubbles: true, cancelable: true, button: 0, clientX: 5, clientY: y }));
  ptr('pointerdown', col, 100);
  ptr('pointerup', d, 100);
  assert.equal(went(), '/calendar/new?start=2026-10-14T09%3A00&end=2026-10-14T10%3A00&user=42', 'a new meeting in a colleague\'s calendar invites them');
}
// reminders of the meeting form (GO24DEV-424): «Добавить напоминание» adds one of 15 minutes, the cross removes one
{
  const { w } = page(`<form><div class="reminds" data-reminds>
      <span class="remind"><select name="remind"><option value="15" selected>15</option><option value="60">60</option></select><button type="button" data-remind-del>x</button></span>
      <template data-remind-row><span class="remind"><select name="remind"><option value="15" selected>15</option><option value="60">60</option></select><button type="button" data-remind-del>x</button></span></template>
      <button type="button" data-remind-add>+</button></div></form>`);
  w.eval(`((location) => {${script}})(nav)`);
  const d = w.document, values = () => new w.FormData(d.querySelector('form')).getAll('remind');
  d.querySelector('[data-remind-add]').click();
  d.querySelector('[data-remind-add]').click();
  assert.deepEqual(values(), ['15', '15', '15']);
  d.querySelectorAll('[data-reminds] select')[1].value = '60';
  d.querySelector('[data-remind-del]').click();
  assert.deepEqual(values(), ['60', '15']);
  d.querySelectorAll('[data-remind-del]').forEach(b => b.click());
  assert.deepEqual(values(), [], 'no reminders');
}
console.log('calendar.js: dragging, the panel of calendars, one\'s own calendars, colleagues\' calendars and reminders passed');
