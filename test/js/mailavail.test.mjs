// «Почта» → «Вставить свободное время» of the editor, in jsdom: the dialog leaves the letter's form, shows the free
// spans of each day ticked, asks again when the length changes, «Вставить в письмо» sends the ticked ones and hands
// the block back to the letter; with none ticked nothing is asked.
// node test/js/mailavail.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/mailavail.js', import.meta.url), 'utf8');
const dom = new JSDOM(`<!doctype html><body><form data-compose><input name="subject" value="Встреча">
  <dialog data-avail-dlg><input type="date" data-a="from"><select data-a="days"><option value="5" selected>5</option></select>
    <select data-a="length"><option value="30" selected>30</option><option value="60">60</option></select>
    <div data-avail-days></div><p data-avail-tz></p><div data-avail-error hidden></div>
    <button type="button" data-avail-insert></button><button type="button" data-avail-close></button>
    <template data-avail-labels><span data-l="none">none</span><span data-l="no_spans">tick one</span><span data-l="tz">TZ %s</span></template>
  </dialog></form></body>`, { runScripts: 'outside-only', url: 'http://go24.test/mail?new=1' });
const w = dom.window;
w.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
w.HTMLDialogElement.prototype.close = function () { this.open = false; };
const calls = [];
w.fetch = async (url, opt = {}) => {
  calls.push({ url, body: opt.body ? new URLSearchParams(opt.body.toString()) : null });
  if (opt.method === 'POST') return { ok: true, json: async () => ({ html: '<div>FREE</div>' }) };
  const long = url.includes('length=60');
  return { ok: true, json: async () => ({ tz: 'Europe/Amsterdam', labels: ['пн, 07.01.2030', 'вт, 08.01.2030'],
    days: [{ date: '2030-01-07', spans: long ? [{ start: '11:00', end: '18:00' }] : [{ start: '09:00', end: '10:00' }, { start: '11:00', end: '18:00' }] },
      { date: '2030-01-08', spans: [{ start: '09:00', end: '12:00' }] }] }) };
};
w.eval(script);
const tick = (ms = 20) => new Promise(r => setTimeout(r, ms));
const d = w.document;
const form = d.querySelector('[data-compose]');
let inserted = null;
form.addEventListener('go24:insert', e => { inserted = e.detail.html; });

d.dispatchEvent(new w.CustomEvent('go24:availpick', { detail: { form } }));
await tick();
const dlg = d.querySelector('[data-avail-dlg]');
assert.equal(dlg.parentElement, d.body, 'the dialog is out of the letter\'s form');
assert.equal(dlg.open, true);
assert.match(calls[0].url, /^\/mail\/availability\?days=5&length=30$/);
const boxes = () => [...dlg.querySelectorAll('[data-avail-days] input[type="checkbox"]')];
assert.equal(boxes().length, 3);
assert.ok(boxes().every(b => b.checked));
assert.equal(dlg.querySelector('legend').textContent, 'пн, 07.01.2030');
assert.equal(dlg.querySelector('[data-avail-tz]').textContent, 'TZ Europe/Amsterdam');

// an hour at least, from a date
dlg.querySelector('[data-a="length"]').value = '60';
dlg.querySelector('[data-a="from"]').value = '2030-01-07';
dlg.querySelector('[data-a="length"]').dispatchEvent(new w.Event('change'));
await tick();
assert.match(calls.at(-1).url, /length=60&from=2030-01-07/);
assert.equal(boxes().length, 2);

// the Tuesday unticked; the block of the rest goes into the letter
boxes()[1].checked = false;
dlg.querySelector('[data-avail-insert]').click();
await tick();
const post = calls.at(-1);
assert.equal(post.url, '/mail/availability');
assert.deepEqual(post.body.getAll('span'), ['2030-01-07T11:00/18:00']);
assert.equal(inserted, '<div>FREE</div>');
assert.equal(dlg.open, false);

// opened again, none ticked: nothing is asked
d.dispatchEvent(new w.CustomEvent('go24:availpick', { detail: { form } }));
await tick();
boxes().forEach(b => { b.checked = false; });
const n = calls.length;
dlg.querySelector('[data-avail-insert]').click();
await tick();
assert.equal(calls.length, n);
assert.equal(dlg.querySelector('[data-avail-error]').textContent, 'tick one');

console.log('mailavail: ok');
