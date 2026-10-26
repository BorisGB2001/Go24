// «Структура компании»: the search leaves the departments found and the ones above them, marks the people and opens
// their lists; an administrator drags a person onto another department and a department under another, in jsdom.
// node test/js/structure.test.mjs
import { JSDOM, VirtualConsole } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/structure.js', import.meta.url), 'utf8');
const dept = (id, name, people, kids = '') => `<div class="dept" data-dept="${id}"><div class="dcard card">
  <div class="dhead"><b class="dname"><a href="/company?dept=${id}">${name}</a></b></div>
  <div class="dlead"></div>
  ${people.length ? `<details class="dpeople"><summary>Сотрудники</summary>${people.map(([u, n]) => `<a href="/u/${u}" class="person" data-user="${u}"><span>${n}</span></a>`).join('')}</details>` : ''}
  </div>${kids ? `<div class="dkids">${kids}</div>` : ''}</div>`;
const tree = dept('root', 'Ромашка', [['u0', 'Иванов Иван']],
  dept('it', 'ИТ', [['u1', 'Петров Пётр']], dept('dev', 'Разработка', [['u2', 'Сидоров Сидор'], ['u3', 'Кузнецова Анна']])) +
  dept('sales', 'Продажи', [['u4', 'Смирнов Олег']], dept('b2b', 'Корпоративные', [['u5', 'Попова Мария']])));
let reloads = 0;
const virtualConsole = new VirtualConsole();
virtualConsole.on('jsdomError', e => { if (/navigation|reload/i.test(e.message)) reloads++; else console.error(e); });
const page = edit => new JSDOM(`<!doctype html><body><div class="row structbar"><input type="search" class="structfind"><span class="structnone" hidden>Ничего не найдено</span></div>
  <div class="orgtree"${edit ? ' data-struct-edit' : ''}>${tree.replace('class="dept"', 'class="dept root"')}</div></body>`,
  { runScripts: 'outside-only', url: 'http://go24.test/company/structure', virtualConsole });

// the search
{
  const w = page(false).window, doc = w.document;
  w.eval(script);
  const find = doc.querySelector('.structfind');
  const type = v => { find.value = v; find.dispatchEvent(new w.Event('input')); };
  const hidden = () => [...doc.querySelectorAll('.dept.struct-hide')].map(d => d.dataset.dept).sort();
  const d = id => doc.querySelector(`[data-dept="${id}"]`);
  type('анна');
  assert.deepEqual(hidden(), ['b2b', 'sales'], 'the branch down to the person stays');
  assert.ok(d('dev').querySelector('[data-user="u3"]').classList.contains('found'));
  assert.ok(!d('dev').querySelector('[data-user="u2"]').classList.contains('found'));
  assert.ok(d('dev').querySelector('.dpeople').open, 'the list with the person found is opened');
  assert.ok(!d('it').querySelector(':scope > .dcard .dpeople').open);
  assert.ok(doc.querySelector('.structnone').hidden);
  type('продаж'); // a department: all of it stays
  assert.deepEqual(hidden(), ['dev', 'it'], 'only the other branch is hidden');
  assert.ok(d('sales').querySelector(':scope > .dcard').classList.contains('found'));
  assert.ok(!d('dev').querySelector('.dpeople').open, 'what the last search opened is closed');
  type('нет такого');
  assert.ok(!doc.querySelector('.structnone').hidden);
  assert.ok(d('root').classList.contains('struct-hide'));
  type('');
  assert.deepEqual(hidden(), []);
  assert.equal(doc.querySelectorAll('.found').length, 0);
  assert.ok(doc.querySelector('.structnone').hidden);
  type('смирнов');
  find.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Escape' }));
  assert.equal(find.value, '');
  assert.deepEqual(hidden(), []);
  // reading only: nothing is dragged
  assert.equal(doc.querySelectorAll('[draggable="true"]').length, 0);
}

// the drags
{
  const w = page(true).window, doc = w.document;
  const sent = [];
  let status = 204;
  w.fetch = async (url, opts) => { sent.push({ url, body: Object.fromEntries(opts.body) }); return { ok: status < 300, status, statusText: 'x', json: async () => ({ error: 'Нельзя' }) }; };
  w.alert = msg => sent.push({ alert: msg });
  w.eval(script);
  const d = id => doc.querySelector(`[data-dept="${id}"]`);
  const card = id => d(id).querySelector(':scope > .dcard');
  const head = id => card(id).querySelector('.dhead');
  const person = u => doc.querySelector(`[data-user="${u}"]`);
  assert.equal(person('u2').getAttribute('draggable'), 'true');
  assert.equal(head('it').getAttribute('draggable'), 'true');
  assert.ok(!head('root').hasAttribute('draggable'), 'the company is not dragged');
  const tick = async () => { for (let i = 0; i < 5; i++) await new Promise(r => setTimeout(r, 0)); };
  const ev = (type, el) => {
    const e = new w.Event(type, { bubbles: true, cancelable: true });
    e.dataTransfer = { setData() {}, effectAllowed: '' };
    el.dispatchEvent(e);
    return e;
  };
  const drag = async (from, to) => {
    ev('dragstart', from);
    const over = ev('dragover', to);
    const accepted = over.defaultPrevented;
    ev('drop', to);
    ev('dragend', from);
    await tick();
    return accepted;
  };

  // a person onto their own department: nothing; onto another: moved
  assert.equal(await drag(person('u2'), card('dev').querySelector('.dlead')), false);
  assert.equal(sent.length, 0);
  assert.equal(await drag(person('u2'), card('b2b').querySelector('.dname')), true);
  assert.deepEqual(sent.pop(), { url: '/company/departments', body: { op: 'move_user', user: 'u2', from: 'dev', to: 'b2b' } });
  assert.equal(reloads, 1);

  // a department: not under itself, its own sub-department nor where it is; under another one — moved
  assert.equal(await drag(head('it'), card('dev')), false);
  assert.equal(await drag(head('it'), card('it')), false);
  assert.equal(await drag(head('dev'), card('it')), false);
  assert.equal(sent.length, 0);
  assert.equal(await drag(head('dev'), card('sales')), true);
  assert.deepEqual(sent.pop(), { url: '/company/departments', body: { op: 'move', id: 'dev', parent: 'sales' } });
  assert.equal(doc.querySelectorAll('.drop-in, .dragging').length, 0, 'the marks are gone after the drag');

  // refused: the message is shown, the page stays
  status = 422;
  await drag(head('b2b'), card('root'));
  assert.deepEqual(sent.pop(), { alert: 'Нельзя' });
  assert.equal(reloads, 2);
}
console.log('structure.test.mjs: ok');
