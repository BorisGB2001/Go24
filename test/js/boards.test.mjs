// Drag & drop and quick add on go24 boards, run in jsdom: node test/js/boards.test.mjs (see `make test-js`).
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/boards.js', import.meta.url), 'utf8');

const col = (id, total, cards, finish) => `
  <div class="col" data-stage="${id}"><div class="colhead"><span class="colcount">${total}</span></div>
    ${finish ? '' : `<details class="quick"><form method="post" action="/p/GO/tasks" data-quick><input type="hidden" name="stage" value="${id}"><input name="title"></form></details>`}
    <div class="cards">${cards}</div><div class="empty" ${cards ? 'hidden' : ''}>empty</div></div>`;

function page(responses, kind = 'stage') {
  const dom = new JSDOM(`<!doctype html><body>
    <nav><span class="cnt" data-counter="open">3</span><span class="cnt red" data-counter="overdue" hidden>0</span><span class="cnt" data-counter="to_control" hidden>0</span></nav>
    <main class="content">
    <div class="board" data-board="${kind}" data-failed="Не удалось">
      ${col('s1', 1, '<div class="kcard" draggable="true" data-key="GO-1">GO-1<span class="dl over">01.10.2026 18:00</span></div><div class="kcard" draggable="false" data-key="GO-2">GO-2</div>')}
      ${col('s2', 0, '')}
      ${col('s3', 40, '', true)}
    </div></main></body>`, { runScripts: 'outside-only', url: 'http://go24.test/p/GO/board' });
  const w = dom.window;
  w.fetch = async (url, opts) => {
    const r = responses.shift();
    w.lastRequest = { url, body: String(opts.body), accept: opts.headers.Accept };
    return { ok: r.status < 300, status: r.status, json: async () => r.body };
  };
  w.eval(script);
  return w;
}

const tick = () => new Promise(r => setTimeout(r, 10));
function drag(w, card, target) {
  const ev = (type) => { const e = new w.Event(type, { bubbles: true, cancelable: true }); e.dataTransfer = {}; return e; };
  card.dispatchEvent(ev('dragstart'));
  target.dispatchEvent(ev('dragover'));
  target.dispatchEvent(ev('drop'));
  card.dispatchEvent(ev('dragend'));
}
const stage = (w, s) => w.document.querySelector(`.col[data-stage="${s}"]`);
const count = (w, s) => stage(w, s).querySelector('.colcount').textContent;
const card = (w, k) => w.document.querySelector(`[data-key="${k}"]`);

// 1. a move: the card moves at once, the server's card (new status chips) replaces it, counters refresh, no reload
{
  const w = page([{ status: 200, body: { stage: 's2', status: 'in_progress',
    card: '<div class="kcard" draggable="true" data-key="GO-1"><span class="pill">new</span></div>',
    counters: { open: 2, overdue: 1, to_control: 0 } } }]);
  drag(w, card(w, 'GO-1'), stage(w, 's2'));
  assert.equal(card(w, 'GO-1').closest('.col'), stage(w, 's2'), 'card moved optimistically');
  await tick();
  assert.match(w.lastRequest.url, /\/t\/GO-1\/stage$/);
  assert.equal(w.lastRequest.body, 'stage=s2');
  assert.equal(w.lastRequest.accept, 'application/json');
  assert.equal(count(w, 's1'), '0');
  assert.equal(count(w, 's2'), '1');
  assert.equal(stage(w, 's2').querySelector('.empty').hidden, true);
  assert.ok(card(w, 'GO-1').querySelector('.pill'), 'card replaced by the server version');
  assert.equal(w.document.querySelectorAll('[data-key="GO-1"]').length, 1);
  assert.equal(w.document.querySelector('[data-counter="overdue"]').textContent, '1');
  assert.equal(w.document.querySelector('[data-counter="open"]').hidden, true, 'open hidden while overdue shown');
}

// 2. the server puts the task elsewhere (e.g. completed from the card meanwhile): the card follows it;
//    a column total larger than its cards (finish stage cut short) changes by one
{
  const w = page([{ status: 200, body: { stage: 's3', status: 'completed',
    card: '<div class="kcard" draggable="true" data-key="GO-1">done</div>' } }]);
  drag(w, card(w, 'GO-1'), stage(w, 's2'));
  await tick();
  assert.equal(card(w, 'GO-1').closest('.col'), stage(w, 's3'));
  assert.equal(count(w, 's2'), '0');
  assert.equal(count(w, 's3'), '41');
}

// 3. the server refuses: the card goes back and the message is shown
{
  const w = page([{ status: 422, body: { error: 'Действие недоступно', code: 'err.action_not_allowed' } }]);
  drag(w, card(w, 'GO-1'), stage(w, 's3'));
  await tick();
  assert.equal(card(w, 'GO-1').closest('.col'), stage(w, 's1'), 'card returned');
  assert.equal(count(w, 's1'), '1');
  assert.equal(count(w, 's3'), '40');
  assert.equal(w.document.querySelector('.flash').textContent, 'Действие недоступно');
}

// 4. a card the user may not move is not dragged at all
{
  const w = page([]);
  drag(w, card(w, 'GO-2'), stage(w, 's2'));
  await tick();
  assert.equal(card(w, 'GO-2').closest('.col'), stage(w, 's1'));
  assert.equal(w.lastRequest, undefined);
}

// 5. «+» quick add: the new card appears on top of the column, the form is cleared
{
  const w = page([{ status: 201, body: { key: 'GO-3', stage: 's2', card: '<div class="kcard" draggable="true" data-key="GO-3">GO-3</div>' } }]);
  const form = stage(w, 's2').querySelector('form[data-quick]');
  form.querySelector('input[name=title]').value = 'Новая';
  form.dispatchEvent(new w.Event('submit', { bubbles: true, cancelable: true }));
  await tick();
  assert.equal(w.lastRequest.url, 'http://go24.test/p/GO/tasks');
  assert.equal(w.lastRequest.body, 'stage=s2&title=%D0%9D%D0%BE%D0%B2%D0%B0%D1%8F');
  assert.equal(card(w, 'GO-3').closest('.col'), stage(w, 's2'));
  assert.equal(count(w, 's2'), '1');
  assert.equal(form.querySelector('input[name=title]').value, '');
}

// 6. «Сроки»: the move asks for the column's deadline and shows the new one on the card
{
  const w = page([{ status: 200, body: { deadline: '16.10.2026 18:00' } }, { status: 422, body: { error: 'Срок менять нельзя' } }], 'deadline');
  drag(w, card(w, 'GO-1'), stage(w, 's2'));
  await tick();
  assert.equal(w.lastRequest.url, '/deadlines/move');
  assert.equal(w.lastRequest.body, 'task=GO-1&period=s2');
  const dl = card(w, 'GO-1').querySelector('.dl');
  assert.equal(dl.textContent, '16.10.2026 18:00');
  assert.equal(dl.classList.contains('over'), false);
  drag(w, card(w, 'GO-1'), stage(w, 's1'));
  await tick();
  assert.equal(card(w, 'GO-1').closest('.col'), stage(w, 's2'), 'refused: the card returns');
  assert.equal(w.document.querySelector('.flash').textContent, 'Срок менять нельзя');
}

// 7. CRM: the card goes to its entity's stage address
{
  const w = page([{ status: 204, body: {} }], 'crm');
  w.document.querySelector('.board').dataset.move = '/crm/deals/';
  drag(w, card(w, 'GO-1'), stage(w, 's2'));
  await tick();
  assert.equal(w.lastRequest.url, '/crm/deals/GO-1/stage');
  assert.equal(w.lastRequest.body, 'stage=s2');
  assert.equal(card(w, 'GO-1').closest('.col'), stage(w, 's2'));
}

// 8. CRM: the column's sum follows the moved card (and its undo); «+» adds its card and sum, asking for required fields
{
  const ccol = (id, n, sum, cards) => `<div class="col" data-stage="${id}"><div class="colhead"><span class="colcount">${n}</span>
    <div class="colsum" data-sum="${sum}" data-cur="RUB">x</div></div>
    <details class="quick"><form method="post" action="/crm/deals/new" data-quick><input type="hidden" name="stage" value="${id}"><input name="title" value="Новая"></form></details>
    <div class="cards">${cards}</div><div class="empty" ${cards ? 'hidden' : ''}>empty</div></div>`;
  const dom = new JSDOM(`<!doctype html><body><main class="content"><div class="board" data-board="crm" data-move="/crm/deals/" data-failed="Не удалось">
    ${ccol('a', 2, 1001500.5, '<div class="kcard" draggable="true" data-key="D1" data-sum="1000000">D1</div><div class="kcard" draggable="true" data-key="D2" data-sum="1500.5">D2</div>')}
    ${ccol('b', 0, 0, '')}</div></main></body>`, { runScripts: 'outside-only', url: 'http://go24.test/crm/deals' });
  const w = dom.window, responses = [{ status: 422, body: { error: 'Нет поля' } },
    { status: 422, body: { error: 'Заполните', fields: [{ id: 'f1' }] } },
    { status: 201, body: { id: 'D3', card: '<div class="kcard" draggable="true" data-key="D3" data-sum="250">D3</div>' } }];
  const bodies = [];
  w.fetch = async (url, opts) => { bodies.push(String(opts.body)); const r = responses.shift(); return { ok: r.status < 300, status: r.status, json: async () => r.body }; };
  w.crmRequired = async (fields) => ({ ['f_' + fields[0].id]: 'Север' });
  w.eval(script);
  const sum = (s) => stage(w, s).querySelector('.colsum').textContent;
  drag(w, card(w, 'D2'), stage(w, 'b'));
  assert.equal(sum('b'), '1 500,50 RUB', 'the card adds its sum to the new column');
  assert.equal(sum('a'), '1 000 000 RUB');
  await tick();
  assert.equal(card(w, 'D2').closest('.col'), stage(w, 'a'), 'refused: back');
  assert.equal(sum('a'), '1 001 500,50 RUB', 'and its sum with it');
  assert.equal(sum('b'), '0 RUB');
  stage(w, 'b').querySelector('form[data-quick]').dispatchEvent(new w.Event('submit', { cancelable: true }));
  await tick(); await tick();
  assert.match(bodies[2], /f_f1=%D0%A1%D0%B5%D0%B2%D0%B5%D1%80/, 'resent with the required field');
  assert.match(bodies[2], /stage=b/);
  assert.equal(card(w, 'D3').closest('.col'), stage(w, 'b'));
  assert.equal(count(w, 'b'), '1');
  assert.equal(sum('b'), '250 RUB');
}

console.log('boards.js: 8 scenarios passed');
