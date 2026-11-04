// «Местоположение» of the working day (workgeo.js) in jsdom: starting and finishing the day ask the browser where it
// is and send the position with the form; pausing does not ask; a refusal sends the form without it.
// node test/js/workgeo.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/workgeo.js', import.meta.url), 'utf8');

function page(geo) {
  const dom = new JSDOM(`<!doctype html><body>
    <form id="day" method="post" action="/worktime/day" data-geo><input type="hidden" name="back" value="/my">
      <button name="action" value="start">Начать</button><button name="action" value="pause">Перерыв</button></form>
    <form id="end" method="post" action="/worktime/day" data-geo><textarea name="report">Отчёт</textarea>
      <button name="action" value="finish">Завершить</button></form>
    <form id="plain" method="post" action="/worktime/day"><button name="action" value="start">Начать</button></form>
  </body>`, { runScripts: 'outside-only', url: 'http://go24.test/my' });
  const w = dom.window;
  w.asked = 0;
  if (geo !== undefined) {
    Object.defineProperty(w.navigator, 'geolocation', { value: { getCurrentPosition(ok, fail) {
      w.asked++;
      setTimeout(() => geo ? ok({ coords: geo }) : fail({ code: 1 }), 0);
    } } });
  }
  w.eval(script);
  // what reaches forms.js: the fields of each submit that was not stopped
  w.sent = [];
  w.document.addEventListener('submit', e => {
    e.preventDefault();
    w.sent.push([e.target.id, Object.fromEntries(new w.FormData(e.target, e.submitter))]);
  });
  return w;
}
const tick = () => new Promise(r => setTimeout(r, 10));
const press = (w, form, value) => w.document.querySelector(`#${form} button[value=${value}]`).click();

// 1. start: the position is asked once and goes with the pressed button
{
  const w = page({ latitude: 52.370216, longitude: 4.895168, accuracy: 25.4 });
  press(w, 'day', 'start');
  press(w, 'day', 'start'); // a second press while waiting does nothing
  assert.equal(w.sent.length, 0);
  await tick();
  assert.equal(w.asked, 1);
  assert.deepEqual(w.sent, [['day', { back: '/my', action: 'start', lat: '52.370216', lon: '4.895168', acc: '25.4' }]]);
}

// 2. finish sends the report with the position; pause is not asked about
{
  const w = page({ latitude: 55.75, longitude: 37.61, accuracy: 10 });
  press(w, 'day', 'pause');
  assert.deepEqual(w.sent, [['day', { back: '/my', action: 'pause' }]]);
  press(w, 'end', 'finish');
  await tick();
  assert.equal(w.asked, 1);
  assert.deepEqual(w.sent[1], ['end', { report: 'Отчёт', action: 'finish', lat: '55.75', lon: '37.61', acc: '10' }]);
}

// 3. refused: the day starts anyway, without a position
{
  const w = page(null);
  press(w, 'day', 'start');
  await tick();
  assert.deepEqual(w.sent, [['day', { back: '/my', action: 'start', lat: '', lon: '', acc: '' }]]);
}

// 4. a form without data-geo, or a browser without geolocation, goes at once
{
  const w = page({ latitude: 1, longitude: 2, accuracy: 3 });
  press(w, 'plain', 'start');
  assert.deepEqual(w.sent, [['plain', { action: 'start' }]]);
  const w2 = page();
  press(w2, 'day', 'start');
  assert.deepEqual(w2.sent, [['day', { back: '/my', action: 'start' }]]);
}

console.log('workgeo: ok');
