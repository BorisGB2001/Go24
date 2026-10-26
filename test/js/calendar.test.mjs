// The planner of the meeting form in jsdom: the busy time of the people, who is busy at the meeting's time, «Nearest
// free time» moves the meeting keeping its length, a new attendee asks again. node test/js/calendar.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/calendar.js', import.meta.url), 'utf8');
const dom = new JSDOM(`<!doctype html><html lang="ru"><body><form>
  <input type="datetime-local" name="start" value="2026-10-21T10:00"><input type="datetime-local" name="end" value="2026-10-21T11:00">
  <div data-upick><span class="upick-chip"><input type="hidden" name="invite" value="anna"></span></div>
  <div data-planner data-owner="me" data-except="" data-t-free="free" data-t-busy="busy: %s" data-t-none="none" data-t-take="take %s">
    <div class="pl-head"><span class="pl-state"></span><button type="button" class="pl-take" hidden></button></div><div class="pl-grid"></div>
  </div></form></body></html>`, { runScripts: 'outside-only', url: 'http://go24.test/calendar/new' });
const w = dom.window;
const asked = [];
let answer;
w.fetch = async u => { asked.push(new URL(u, 'http://go24.test')); return { ok: true, json: async () => answer() }; };
const day = (start, end) => ({
  Day: '2026-10-21', Hour0: 7, Hour1: 22,
  People: [
    { ID: 'me', Name: 'Я', Busy: false, Slots: [] },
    { ID: 'anna', Name: 'Анна', Busy: start < '2026-10-21T11:30' && end > '2026-10-21T10:30',
      Slots: [{ Start: '2026-10-21T10:30', End: '2026-10-21T11:30', Kind: 'busy', Title: 'Планёрка' }] },
  ],
  Free: start >= '2026-10-21T11:30' || end <= '2026-10-21T10:30' ? start : '2026-10-21T11:30',
  FreeIsSame: start >= '2026-10-21T11:30' || end <= '2026-10-21T10:30',
});
const f = w.document.querySelector('form');
const val = n => f.querySelector(`[name=${n}]`).value;
answer = () => day(val('start'), val('end'));
const tick = () => new Promise(r => setTimeout(r, 300));
w.eval(script);
await tick();

const q = asked.at(-1).searchParams;
assert.deepEqual(q.getAll('user'), ['me', 'anna'], 'the organizer and the invited');
assert.equal(q.get('start'), '2026-10-21T10:00');
const rows = w.document.querySelectorAll('.pl-row:not(.pl-hours)');
assert.equal(rows.length, 2);
assert.ok(rows[1].classList.contains('clash'), 'anna is busy at 10:00–11:00');
const slot = rows[1].querySelector('.pl-slot.busy');
assert.equal(slot.style.left, (3.5 * 60 / (15 * 60) * 100) + '%', 'placed on the 7:00–22:00 scale');
assert.match(slot.title, /Планёрка/);
assert.equal(w.document.querySelector('.pl-state').textContent, 'busy: Анна');
const take = w.document.querySelector('.pl-take');
assert.equal(take.hidden, false);

take.click();
await tick();
assert.equal(val('start'), '2026-10-21T11:30');
assert.equal(val('end'), '2026-10-21T12:30', 'the length is kept');
assert.equal(w.document.querySelector('.pl-state').textContent, 'free');
assert.equal(take.hidden, true, 'nothing to offer: the time is free');

// a new attendee: the planner asks again
const n = asked.length;
const chip = w.document.createElement('span');
chip.innerHTML = '<input type="hidden" name="invite" value="petr">';
f.querySelector('[data-upick]').append(chip);
f.querySelector('[data-upick]').dispatchEvent(new w.Event('change', { bubbles: true }));
await tick();
assert.equal(asked.length, n + 1);
assert.deepEqual(asked.at(-1).searchParams.getAll('user'), ['me', 'anna', 'petr']);
console.log('calendar.js: planner passed');
