// The people picker (static/js/people.js) in jsdom: the list opens with «Последние», Enter adds a chip with a hidden
// input, typing searches the server, a chosen person toggles off, Backspace removes the last chip, Esc closes;
// a single choice replaces its chip and closes, «Отделы» lists a department's people, «Весь отдел» adds them all,
// chips without names are named by the server. node test/js/people.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/people.js', import.meta.url), 'utf8');
const pick = (name, attrs, chips) => `<div class="upick" data-upick data-name="${name}" ${attrs} data-tabs="Последние|Отделы|Все"
  data-empty="Никого" data-more="Ещё" data-dept-all="Весь отдел" data-in-dept="в отделе" data-everywhere="Искать везде">${chips}<input type="text" class="upick-q"></div>`;
const dom = new JSDOM(`<!doctype html><body>
  <form id="f1">${pick('auditors', 'data-multiple data-self', '<span class="upick-chip" data-id="u1"><span class="upick-name">Анна</span><input type="hidden" name="auditors" value="u1"><button type="button" data-rm>x</button></span>')}</form>
  <form id="f2">${pick('responsible', 'data-required data-submit data-missing', '<span class="upick-chip" data-id="u9"><span class="upick-name"></span><input type="hidden" name="responsible" value="u9"></span>')}</form>
  <form id="f3"><select name="project"><option value="GO">Go</option><option value="CL1" data-collab selected>Коллаб</option></select>
    ${pick('accomplices', 'data-multiple data-collab="@project"', '')}</form>
  <form id="f4"><input type="hidden" name="role" value="accomplice">${pick('user_id', 'data-submit data-collab="CL1"', '')}<button>+</button></form>
  </body>`, { runScripts: 'outside-only', url: 'http://go24.test/' });
const w = dom.window, d = w.document;
w.CSS = { escape: s => s };
w.TextEncoder = TextEncoder;
const people = {
  u1: { id: 'u1', name: 'Анна', position: 'Юрист', dept: '', photo: '', active: true },
  u2: { id: 'u2', name: 'Пётр', position: '', dept: 'Бухгалтерия', photo: '', active: true },
  u3: { id: 'u3', name: 'Вера', position: '', dept: 'Бухгалтерия', photo: '/u/u3/photo', active: true },
  u9: { id: 'u9', name: 'Уволенный', position: '', dept: '', photo: '', active: false },
};
const asked = [];
w.fetch = async url => {
  const u = new URL(url, 'http://go24.test/');
  asked.push(u.pathname + u.search);
  const q = u.searchParams;
  let body;
  if (u.pathname === '/people/departments') body = [{ id: 'd0', parent: '', name: 'Компания', depth: 0, people: 3 },
    { id: 'd1', parent: 'd0', name: 'Бухгалтерия', depth: 1, people: 2 }, { id: 'd2', parent: 'd1', name: 'Касса', depth: 2, people: 1 }];
  else if (q.get('ids')) body = q.get('ids').split(',').map(id => people[id]);
  else if (q.get('recent')) body = [people.u2];
  else if (q.get('dept')) { // d0: Анна; d1: Пётр and, in its Касса, Вера
    const deep = q.get('deep') === '1';
    body = { d0: deep ? [people.u1, people.u2, people.u3] : [people.u1], d1: deep ? [people.u2, people.u3] : [people.u2], d2: [people.u3] }[q.get('dept')]
      .filter(p => !q.get('q') || p.name.toLowerCase().startsWith(q.get('q')));
  }
  else if (q.get('q')) body = Object.values(people).filter(p => p.active && p.name.toLowerCase().startsWith(q.get('q')));
  else body = [people.u1, people.u2, people.u3];
  return { ok: true, json: async () => body };
};
const wait = ms => new Promise(r => setTimeout(r, ms));
let submitted = 0;
d.getElementById('f2').addEventListener('submit', e => { e.preventDefault(); submitted++; });
w.eval(script);
await wait(10);

// the chip the page could not name
const fired = d.querySelector('#f2 .upick-chip');
assert.equal(fired.textContent.trim(), 'Уволенный');
assert.ok(fired.classList.contains('fired'));

const [q1, q2] = d.querySelectorAll('.upick-q');
const key = (el, k) => el.dispatchEvent(new w.KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true }));
const rows = () => [...d.querySelectorAll('.upick-pop .upick-row')].map(r => r.querySelector('.upick-text')
  ? [...r.querySelector('.upick-text').children].map(c => c.textContent).join(' ') : r.textContent.replace(/\s+/g, ' ').trim());
const posted = (f, n) => new w.FormData(d.getElementById(f)).getAll(n);

// multiple: «Последние» on focus, Enter adds
q1.focus();
await wait(10);
assert.ok(asked.some(a => a.startsWith('/people?recent=1') && a.includes('self=1')), asked.join(' '));
assert.deepEqual(rows(), ['Пётр Бухгалтерия']);
key(q1, 'Enter');
assert.deepEqual(posted('f1', 'auditors'), ['u1', 'u2']);
assert.ok(d.querySelector('.upick-pop'), 'stays open for more');

// typing searches; a chosen person toggles off
q1.value = 'ан';
q1.dispatchEvent(new w.Event('input', { bubbles: true }));
await wait(260);
assert.ok(asked.some(a => a.includes('q=%D0%B0%D0%BD')));
assert.equal(d.querySelector('.upick-pop .upick-row').classList.contains('chosen'), true);
key(q1, 'Enter');
assert.deepEqual(posted('f1', 'auditors'), ['u2']);

// Backspace in the empty field removes the last chip; Esc closes
q1.value = '';
key(q1, 'Backspace');
assert.deepEqual(posted('f1', 'auditors'), []);
key(q1, 'Escape');
assert.equal(d.querySelector('.upick-pop'), null);

// «Отделы»: the company opens with its sub-departments and only its own people; a department shows only its
// people, «‹» goes up, typing searches within it, «Искать везде» leaves it; «+» and «Весь отдел» add everybody
q1.dispatchEvent(new w.MouseEvent('mousedown', { bubbles: true, cancelable: true }));
await wait(10);
const tab = name => [...d.querySelectorAll('.upick-tabs button')].find(b => b.dataset.tab === name);
const down = el => el.dispatchEvent(new w.MouseEvent('mousedown', { bubbles: true, cancelable: true }));
const crumbs = () => d.querySelector('.upick-crumbs').textContent;
down(tab('depts'));
await wait(10);
assert.deepEqual(rows(), ['Весь отдел · Компания', 'Бухгалтерия2', 'Анна Юрист']);
assert.equal(crumbs(), 'Компания');
down(d.querySelectorAll('.upick-pop .upick-row')[1]); // the name opens the department
await wait(10);
assert.deepEqual(rows(), ['‹ Компания', 'Весь отдел · Бухгалтерия', 'Касса1', 'Пётр Бухгалтерия'], 'only its people');
assert.equal(crumbs(), 'Компания › Бухгалтерия');
assert.deepEqual(posted('f1', 'auditors'), [], 'opening a department chooses nobody');
q1.value = 'ве';
q1.dispatchEvent(new w.Event('input', { bubbles: true }));
await wait(260);
assert.ok(asked.some(a => a.includes('dept=d1') && a.includes('deep=1') && a.includes('q=')), asked.slice(-2).join(' '));
assert.deepEqual(rows(), ['в отделе «Бухгалтерия»Искать везде', 'Вера Бухгалтерия'], 'searched within');
key(q1, 'Enter');
await wait(10);
assert.deepEqual(posted('f1', 'auditors'), ['u3']);
assert.deepEqual(rows()[0], '‹ Компания', 'back in the department after choosing');
q1.value = 'ан';
q1.dispatchEvent(new w.Event('input', { bubbles: true }));
await wait(260);
assert.deepEqual(rows().slice(1), [], 'Анна is not in Бухгалтерия');
down(d.querySelector('.upick-pop .upick-row.scope'));
await wait(10);
assert.deepEqual(rows(), ['Анна Юрист'], 'everywhere');
q1.value = '';
q1.dispatchEvent(new w.Event('input', { bubbles: true }));
await wait(260);
down(tab('depts'));
await wait(10);
down(d.querySelector('.upick-pop .upick-row.dept [data-add]')); // «+» at Бухгалтерия
await wait(10);
assert.deepEqual(posted('f1', 'auditors'), ['u3', 'u2']);
assert.equal(crumbs(), 'Компания', '«+» does not open it');
down(d.querySelector('.upick-pop .upick-row.deptall'));
await wait(10);
assert.ok(asked.some(a => a.includes('dept=d0') && a.includes('deep=1')));
assert.deepEqual(posted('f1', 'auditors'), ['u3', 'u2', 'u1']);
d.body.dispatchEvent(new w.MouseEvent('mousedown', { bubbles: true }));
assert.equal(d.querySelector('.upick-pop'), null, 'a click outside closes');

// single and required: «Все», the choice replaces the chip, closes, sends the form; no ×
q2.focus();
await wait(10);
tab('all').dispatchEvent(new w.MouseEvent('mousedown', { bubbles: true, cancelable: true }));
await wait(10);
assert.deepEqual(rows(), ['Анна Юрист', 'Пётр Бухгалтерия', 'Вера Бухгалтерия']);
d.querySelectorAll('.upick-pop .upick-row')[2].dispatchEvent(new w.MouseEvent('mousedown', { bubbles: true, cancelable: true }));
assert.deepEqual(posted('f2', 'responsible'), ['u3']);
assert.equal(d.querySelector('.upick-pop'), null);
assert.equal(d.querySelector('#f2 [data-rm]'), null);
assert.equal(submitted, 1);
key(q2, 'Backspace');
assert.deepEqual(posted('f2', 'responsible'), ['u3'], 'a required choice stays');

// a collab's task: only its members are asked for (the collab chosen in the form), and «Отделы» is not offered
{
  const q3 = d.querySelector('#f3 .upick-q');
  asked.length = 0;
  q3.focus();
  await wait(30);
  assert.ok(asked.length && asked.every(a => a.includes('collab=CL1')), asked.join(' '));
  assert.equal(d.querySelector('.upick-pop [data-tab="depts"]'), null);
  key(q3, 'Escape');
  q3.blur();
  d.querySelector('#f3 select').value = 'GO';
  asked.length = 0;
  q3.focus();
  await wait(30);
  assert.ok(asked.length && !asked.some(a => a.includes('collab=')), asked.join(' '));
  assert.ok(d.querySelector('.upick-pop [data-tab="depts"]'));
}
// «+» of a task's «Соисполнители» with no one picked: nothing is sent (user_id = "" broke the server), the list opens
{
  key(d.querySelector('#f3 .upick-q'), 'Escape');
  let sent = 0;
  const f4 = d.querySelector('#f4');
  d.addEventListener('submit', e => { if (e.target === f4 && !e.defaultPrevented) sent++; e.preventDefault(); });
  f4.querySelector('button').click();
  await wait(30);
  assert.equal(sent, 0);
  assert.equal(d.activeElement, f4.querySelector('.upick-q'));
  assert.ok(d.querySelector('.upick-pop'));
  key(f4.querySelector('.upick-q'), 'Enter'); // the first in the list is picked and the form goes
  await wait(30);
  assert.equal(sent, 1);
  assert.ok(f4.querySelector('.upick-chip input[name="user_id"]').value);
}
console.log('people.js: people picker passed');
