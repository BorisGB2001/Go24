// «Почта» → «Опрос о времени встречи» of the editor, in jsdom: the dialog leaves the letter's form, shows who is busy
// at a time, «Подобрать время» adds the free times, «Вставить в письмо» makes the poll of To and Cc and hands its
// block back to the letter.
// node test/js/mailpoll.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/mailpoll.js', import.meta.url), 'utf8');
const dom = new JSDOM(`<!doctype html><body><form data-compose><input name="to" value="Ева <eve@x.ru>"><input name="cc" value="client@out.ru">
  <input name="subject" value="Планёрка"><input name="box" value="b1">
  <dialog data-poll-dlg><input data-p="title"><select data-p="length"><option value="30" selected>30</option><option value="60">60</option></select>
    <input data-p="location"><input type="checkbox" data-p="video" checked><input type="checkbox" data-p="auto">
    <div data-poll-times></div><button type="button" data-poll-add></button><button type="button" data-poll-suggest></button>
    <p data-poll-people></p><div data-poll-error hidden></div><button type="button" data-poll-insert></button><button type="button" data-poll-close></button>
    <template data-poll-labels><span data-l="free">free</span><span data-l="busy">Busy:</span><span data-l="people">%s known</span><span data-l="no_times">no times</span></template>
  </dialog></form></body>`, { runScripts: 'outside-only', url: 'http://go24.test/mail?new=1' });
const w = dom.window;
w.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
w.HTMLDialogElement.prototype.close = function () { this.open = false; };
const calls = [];
w.fetch = async (url, opt) => {
  const body = new URLSearchParams(opt.body.toString());
  calls.push({ url, body });
  if (url === '/mail/polls/check') {
    const starts = body.getAll('start');
    return { ok: true, json: async () => ({ people: 2, options: starts.map((s, i) => ({ start: s, busy: i === 0 ? ['Ева'] : [] })),
      suggest: body.get('suggest') ? ['2030-01-07T11:00'] : [] }) };
  }
  return { ok: true, json: async () => ({ id: 'p1', html: '<div>POLL</div>' }) };
};
w.eval(script);
const tick = (ms = 400) => new Promise(r => setTimeout(r, ms));
const d = w.document;
const form = d.querySelector('[data-compose]');
const fields = () => new w.FormData(form);
let inserted = null;
form.addEventListener('go24:insert', e => { inserted = e.detail.html; });

d.dispatchEvent(new w.CustomEvent('go24:pollpick', { detail: { form, fields } }));
const dlg = d.querySelector('[data-poll-dlg]');
assert.equal(dlg.parentElement, d.body, 'the dialog leaves the letter form');
assert.ok(dlg.open);
assert.equal(dlg.querySelector('[data-p="title"]').value, 'Планёрка');
const rows = () => [...dlg.querySelectorAll('[data-poll-times] input')];
assert.equal(rows().length, 2, 'two times to start with');
assert.ok(rows().every(r => ![0, 6].includes(new Date(r.value).getDay())), 'working days only');
await tick();
const check = calls.find(c => c.url === '/mail/polls/check');
assert.deepEqual(check.body.getAll('addr'), ['Ева <eve@x.ru>', 'client@out.ru']);
const notes = () => [...dlg.querySelectorAll('.mpoll-note')].map(n => n.textContent);
assert.deepEqual(notes(), ['Busy: Ева', 'free']);
assert.equal(dlg.querySelector('[data-poll-people]').textContent, '2 known');

// «Подобрать время» adds the suggested time
dlg.querySelector('[data-poll-suggest]').click();
await tick(50);
assert.equal(rows().length, 3);
assert.equal(rows()[2].value, '2030-01-07T11:00');

// «Вставить в письмо»
dlg.querySelector('[data-p="location"]').value = 'Переговорная';
dlg.querySelector('[data-poll-insert]').click();
await tick(50);
const made = calls.find(c => c.url === '/mail/polls');
assert.equal(made.body.get('title'), 'Планёрка');
assert.equal(made.body.get('location'), 'Переговорная');
assert.equal(made.body.get('video'), '1');
assert.equal(made.body.get('auto'), null);
assert.equal(made.body.get('box'), 'b1');
assert.deepEqual(made.body.getAll('to'), ['Ева <eve@x.ru>']);
assert.deepEqual(made.body.getAll('cc'), ['client@out.ru']);
assert.equal(made.body.getAll('start').length, 3);
assert.equal(inserted, '<div>POLL</div>');
assert.equal(dlg.open, false);
assert.equal(rows().length, 0, 'the times are cleared for the next poll');

// opened again: two new times; without times nothing is made
d.dispatchEvent(new w.CustomEvent('go24:pollpick', { detail: { form, fields } }));
assert.equal(rows().length, 2);
dlg.querySelectorAll('[data-poll-times] button').forEach(b => b.click());
const n = calls.length;
dlg.querySelector('[data-poll-insert]').click();
await tick(50);
assert.equal(calls.filter((c, i) => i >= n && c.url === '/mail/polls').length, 0);
assert.equal(dlg.querySelector('[data-poll-error]').textContent, 'no times');

console.log('mailpoll: ok');
