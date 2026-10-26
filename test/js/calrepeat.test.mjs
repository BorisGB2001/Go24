// The repetition of the meeting form in jsdom: the days show for a weekly meeting only, the end's field for the end
// chosen, nothing with no repetition. node test/js/calrepeat.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/calendar.js', import.meta.url), 'utf8');
const dom = new JSDOM(`<!doctype html><html lang="ru"><body><form>
  <select name="repeat"><option value="">no</option><option value="daily">d</option><option value="weekly">w</option></select>
  <div data-ev-repeat hidden><div data-ev-days hidden><input type="checkbox" name="days" value="1"></div>
    <select name="ends" data-ev-ends><option value="">never</option><option value="count">count</option><option value="until">until</option></select>
    <span data-ev-end="count" hidden></span><span data-ev-end="until" hidden></span></div>
  </form></body></html>`, { runScripts: 'outside-only', url: 'http://go24.test/calendar/new' });
const w = dom.window, d = w.document;
w.fetch = async () => ({ ok: true, json: async () => ({}) });
w.eval(script);
const box = d.querySelector('[data-ev-repeat]'), days = d.querySelector('[data-ev-days]');
const set = (name, v) => { const x = d.querySelector(`[name=${name}]`); x.value = v; x.dispatchEvent(new w.Event('change')); };
const shown = v => [...d.querySelectorAll('[data-ev-end]')].filter(x => !x.hidden).map(x => x.dataset.evEnd).join() === v;

set('repeat', 'daily');
assert.ok(!box.hidden && days.hidden, 'daily: the end, no days');
set('repeat', 'weekly');
assert.ok(!days.hidden, 'weekly: the days');
set('ends', 'count');
assert.ok(shown('count'), 'after N meetings');
set('ends', 'until');
assert.ok(shown('until'), 'on a day');
set('ends', '');
assert.ok(shown(''), 'never');
set('repeat', '');
assert.ok(box.hidden, 'no repetition');
console.log('calrepeat: ok');
