// «Контроль времени» (timecontrol.js) in jsdom: the page reports activity at most once a minute for all the tabs and
// shows the question about an absence on top of the page. node test/js/timecontrol.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/timecontrol.js', import.meta.url), 'utf8');
const tpl = `<template id="idle-tpl"><div class="flash idle-ask"><span>Вас не было с {from} до {to} ({dur}). Где вы были?</span>
  <form method="post" action="/worktime/idle/0"><button name="kind" value="work">По работе</button></form></div></template>`;

function page({ reply = {}, last = 0 } = {}) {
  const dom = new JSDOM(`<!doctype html><body><main class="content"><h1>Сотрудники</h1></main>${tpl}</body>`,
    { runScripts: 'outside-only', url: 'http://go24.test/company' });
  const w = dom.window;
  if (last) w.localStorage.setItem('go24.tc.alive', String(last));
  w.posts = [];
  w.fetch = async (url, opts = {}) => { w.posts.push([url, opts.method]); return { ok: true, json: async () => reply }; };
  w.eval(script);
  return w;
}
const tick = () => new Promise(r => setTimeout(r, 10));

// 1. opening the page reports once; more activity within the minute does not
{
  const w = page();
  await tick();
  w.dispatchEvent(new w.Event('mousemove'));
  w.dispatchEvent(new w.Event('keydown'));
  await tick();
  assert.deepEqual(w.posts, [['/worktime/alive', 'POST']]);
  assert.ok(+w.localStorage.getItem('go24.tc.alive') > 0);
  assert.equal(w.document.querySelector('.idle-ask'), null);
}

// 2. another tab has reported a moment ago — this one waits
{
  const w = page({ last: Date.now() - 1000 });
  await tick();
  w.dispatchEvent(new w.Event('mousemove'));
  await tick();
  assert.equal(w.posts.length, 0);
}

// 3. the server counted an absence: the question is put on top with its times and its form, once
{
  const w = page({ reply: { idle: { id: 42, from: '12:05', to: '12:50', dur: '45 мин', ask: true } } });
  await tick();
  const box = w.document.querySelector('main.content > .idle-ask');
  assert.ok(box);
  assert.equal(w.document.querySelector('main.content').firstElementChild, box);
  assert.equal(box.querySelector('span').textContent, 'Вас не было с 12:05 до 12:50 (45 мин). Где вы были?');
  assert.equal(box.querySelector('form').getAttribute('action'), '/worktime/idle/42');
}

// 4. without asking (the setting is off) nothing is shown
{
  const w = page({ reply: { idle: { id: 7, from: '10:00', to: '10:30', dur: '30 мин', ask: false } } });
  await tick();
  assert.equal(w.document.querySelector('.idle-ask'), null);
}
console.log('timecontrol.js: ok');
