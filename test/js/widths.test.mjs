// Dragged widths of list columns and page panes (widths.js) in jsdom. node test/js/widths.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/widths.js', import.meta.url), 'utf8');

function page(html, { local = null, server = {}, path = '/crm/deal/42' } = {}) {
  const dom = new JSDOM(`<!doctype html><body>${html}</body>`, { runScripts: 'outside-only', url: 'http://go24.test' + path, pretendToBeVisual: true });
  const w = dom.window;
  if (local) w.localStorage.setItem('go24.widths', JSON.stringify(local));
  // every header cell is 100px wide as laid out; a fixed one — its own width
  w.HTMLElement.prototype.getBoundingClientRect = function () {
    const width = this.hidden ? 0 : parseFloat(this.style.width) || 100;
    return { width, height: 20, top: 0, left: 0, right: width, bottom: 20 };
  };
  w.CSS = { escape: s => s };
  w.posts = [];
  w.fetch = async (url, opts = {}) => {
    if (opts.method === 'POST') { w.posts.push(Object.fromEntries(new URLSearchParams(String(opts.body)))); return { ok: true, status: 204 }; }
    return { ok: true, status: 200, json: async () => server };
  };
  w.eval(script);
  return w;
}
const tick = () => new Promise(r => setTimeout(r, 10));
const table = `<div class="card"><table class="tlist"><thead><tr><th class="gearcol"></th><th data-col="title">Название</th><th>Сумма</th><th data-col="stage" hidden>Стадия</th></tr></thead>
  <tbody><tr><td></td><td>Сделка</td><td>100</td><td hidden>Новая</td></tr></tbody></table></div>`;
const pointer = (w, el, type, x) => {
  const e = new w.Event(type, { bubbles: true, cancelable: true });
  Object.assign(e, { clientX: x, pointerId: 1 });
  el.dispatchEvent(e);
};

// 1. a list table gets grips (not on the gear column); dragging a border widens the column, the table is fixed
// to the sum and wrapped to scroll sideways; the widths are kept under the page's address with * for ids
{
  const w = page(table);
  const t = w.document.querySelector('table');
  assert.equal(t.querySelectorAll('.colgrip').length, 3);
  assert.equal(t.querySelector('.gearcol .colgrip'), null);
  const grip = t.querySelector('th[data-col="title"] .colgrip');
  pointer(w, grip, 'pointerdown', 500);
  pointer(w, grip, 'pointermove', 580);
  pointer(w, grip, 'pointerup', 580);
  assert.equal(t.querySelector('th[data-col="title"]').style.width, '180px');
  assert.equal(t.style.tableLayout, 'fixed');
  assert.equal(t.style.width, '380px', 'gear 100 + title 180 + sum 100; the hidden column counts nothing');
  assert.ok(t.parentElement.classList.contains('cols-scroll'));
  const kept = JSON.parse(w.localStorage.getItem('go24.widths'));
  assert.deepEqual(kept['list:/crm/deal/*:0'], { 0: 100, title: 180, 2: 100 });
  assert.equal(w.posts.at(-1).name, 'list:/crm/deal/*:0');
  assert.deepEqual(JSON.parse(w.posts.at(-1).widths), { 0: 100, title: 180, 2: 100 });
  // not narrower than 40px
  pointer(w, grip, 'pointerdown', 500);
  pointer(w, grip, 'pointermove', 100);
  pointer(w, grip, 'pointerup', 100);
  assert.equal(t.querySelector('th[data-col="title"]').style.width, '40px');
}

// 2. saved widths apply on load; the server's (another device) win over this browser's; a double click forgets them
{
  const w = page(table.replace('class="tlist"', 'class="tlist" data-cols="deals"'),
    { local: { deals: { title: 300 } }, server: { deals: { title: 250, 2: 60 } } });
  const t = w.document.querySelector('table'), title = t.querySelector('th[data-col="title"]');
  assert.equal(title.style.width, '300px');
  await tick();
  assert.equal(title.style.width, '250px');
  assert.equal(t.rows[0].cells[2].style.width, '60px');
  assert.deepEqual(JSON.parse(w.localStorage.getItem('go24.widths')), { deals: { title: 250, 2: 60 } });
  title.querySelector('.colgrip').dispatchEvent(new w.MouseEvent('dblclick', { bubbles: true }));
  assert.equal(title.style.width, '');
  assert.equal(t.style.tableLayout, '');
  assert.deepEqual(JSON.parse(w.localStorage.getItem('go24.widths')), {});
  assert.deepEqual(w.posts.at(-1), { name: 'deals', widths: '' });
}

// 3. data-cols="off" and tables that are not lists stay as they are
{
  const w = page(table.replace('class="tlist"', 'class="tlist" data-cols="off"') + '<table class="props"><thead><tr><th>x</th></tr></thead></table>');
  assert.equal(w.document.querySelectorAll('.colgrip').length, 0);
}

// 4. a pane of a grid page: its left border is dragged, the grid gets the width on wide screens only, kept
{
  const w = page(`<div class="tcard" style="display:grid"><div class="main">m</div><aside data-pane="task-chat" data-pane-side="left">c</aside></div>`);
  const p = w.document.querySelector('aside'), grip = p.querySelector('.panegrip.left');
  assert.ok(grip);
  w.HTMLElement.prototype.getBoundingClientRect = function () {
    const width = this.classList.contains('tcard') ? 1200 : 400;
    return { width, height: 20, top: 0, left: 0, right: width, bottom: 20 };
  };
  pointer(w, grip, 'pointerdown', 800);
  pointer(w, grip, 'pointermove', 700);
  pointer(w, grip, 'pointerup', 700);
  const css = [...w.document.head.querySelectorAll('style')].map(s => s.textContent).join('');
  assert.match(css, /@media \(min-width: 1001px\) \{ \[data-pane-box="task-chat"\] \{ grid-template-columns: minmax\(0, 1fr\) 500px !important; \} \}/);
  assert.deepEqual(JSON.parse(w.localStorage.getItem('go24.widths'))['pane:task-chat'], { w: 500 });
}

// 5. a board of columns (Сроки): a column's right border is dragged, the board gets the widths of all its columns,
// kept by the columns' data-stage; not narrower than 160px; a double click gives the usual layout back
{
  const w = page(`<div class="board deadlines" style="display:grid" data-cols-board="deadlines">
    <div class="col" data-stage="overdue">a</div><div class="col" data-stage="today">b</div><div class="col" data-stage="week">c</div></div>`,
    { path: '/deadlines' });
  const b = w.document.querySelector('.board'), grip = b.querySelector('[data-stage="today"] .boardgrip');
  assert.equal(b.querySelectorAll('.boardgrip').length, 3);
  pointer(w, grip, 'pointerdown', 300);
  pointer(w, grip, 'pointermove', 450);
  pointer(w, grip, 'pointerup', 450);
  assert.equal(b.style.gridTemplateColumns, '100px 250px 100px');
  assert.deepEqual(JSON.parse(w.localStorage.getItem('go24.widths'))['board:deadlines'], { overdue: 100, today: 250, week: 100 });
  pointer(w, grip, 'pointerdown', 300);
  pointer(w, grip, 'pointermove', 0);
  pointer(w, grip, 'pointerup', 0);
  assert.equal(b.style.gridTemplateColumns, '100px 160px 100px');
  grip.dispatchEvent(new w.MouseEvent('dblclick', { bubbles: true }));
  assert.equal(b.style.gridTemplateColumns, '');
  assert.equal(JSON.parse(w.localStorage.getItem('go24.widths'))['board:deadlines'], undefined);
}

// 6. a table without a header (Дни рождения): its first row carries the grips and the widths
{
  const w = page(`<table class="tlist"><tbody><tr><td>1 мая</td><td>Анна</td></tr><tr><td>2 мая</td><td>Пётр</td></tr></tbody></table>`,
    { path: '/company/birthdays' });
  const t = w.document.querySelector('table'), grip = t.rows[0].cells[0].querySelector('.colgrip');
  assert.equal(t.querySelectorAll('.colgrip').length, 2);
  assert.equal(t.rows[1].querySelector('.colgrip'), null);
  pointer(w, grip, 'pointerdown', 100);
  pointer(w, grip, 'pointermove', 160);
  pointer(w, grip, 'pointerup', 160);
  assert.equal(t.rows[0].cells[0].style.width, '160px');
  assert.deepEqual(JSON.parse(w.localStorage.getItem('go24.widths'))['list:/company/birthdays:0'], { 0: 160, 1: 100 });
}

// 7. the messenger: its grid lays the panes out (the side pane comes and goes), so the widths go into CSS variables of
// the page — the chats' list (right border) and the side pane, one for the thread and the search (left border); the
// chat keeps 300px; another device's widths apply; a double click gives the usual width back
{
  const w = page(`<div class="im" style="width: 1400px"><aside class="im-rooms" style="width: 320px" data-pane="im-rooms" data-pane-var="--im-rooms"></aside>
    <section class="im-chat" style="width: 700px"></section>
    <aside data-search-pane data-pane="im-side" data-pane-var="--im-side" data-pane-side="left" hidden></aside>
    <aside style="width: 360px" data-thread-pane data-pane="im-side" data-pane-var="--im-side" data-pane-side="left"></aside></div>`,
  { path: '/im', server: { 'pane:im-side': { w: 420 } } });
  await tick();
  const im = w.document.querySelector('.im');
  assert.equal(im.style.getPropertyValue('--im-side'), '420px', "another device's width of the side pane");
  assert.equal(w.document.querySelectorAll('.panegrip').length, 3);
  const rooms = w.document.querySelector('.im-rooms .panegrip');
  assert.ok(rooms.classList.contains('right'));
  pointer(w, rooms, 'pointerdown', 320);
  pointer(w, rooms, 'pointermove', 400);
  pointer(w, rooms, 'pointerup', 400);
  assert.equal(im.style.getPropertyValue('--im-rooms'), '400px');
  assert.equal(im.style.gridTemplateColumns, '', 'the page keeps its own grid');
  assert.deepEqual(w.posts.at(-1), { name: 'pane:im-rooms', widths: '{"w":400}' });
  pointer(w, rooms, 'pointerdown', 320);
  pointer(w, rooms, 'pointermove', 3000);
  pointer(w, rooms, 'pointerup', 3000);
  assert.equal(im.style.getPropertyValue('--im-rooms'), '720px', 'the chat keeps 300px');
  const side = w.document.querySelector('[data-thread-pane] .panegrip');
  assert.ok(side.classList.contains('left'));
  pointer(w, side, 'pointerdown', 1000);
  pointer(w, side, 'pointermove', 900);
  pointer(w, side, 'pointerup', 900);
  assert.equal(im.style.getPropertyValue('--im-side'), '460px', 'its left border: wider to the left');
  side.dispatchEvent(new w.MouseEvent('dblclick', { bubbles: true }));
  assert.equal(im.style.getPropertyValue('--im-side'), '');
  assert.equal(JSON.parse(w.localStorage.getItem('go24.widths'))['pane:im-side'], undefined);
}

console.log('widths.js: 7 scenarios passed');
