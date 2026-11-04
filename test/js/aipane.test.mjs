// CoPilot of a section (aipane.js) in jsdom with a fake server: the button opens the pane and loads it from its
// address, a quick question goes with the open task, a question «about the open task» is gone where none is open,
// «Подтвердить» posts to the action and reloads the page once nothing waits, the pane stays open for the next visit
// on its conversation. node test/js/aipane.test.mjs
import { JSDOM, VirtualConsole } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/aipane.js', import.meta.url), 'utf8');

const pane = (run, pending) => `<div class="mai-log" data-ai-log data-run="${run}"></div>
  <div class="mai-quick"><button id="hot" data-ai-say="Что горит?">Горит</button><button id="sum" data-ai-say="Итоги" data-ai-open>Итоги</button>
  <button id="fill" data-ai-fill="Поставь задачу: ">Новая</button></div>
  ${pending ? '<button id="yes" data-ai-decide="a1" data-yes="1">Подтвердить</button>' : ''}
  <form data-ai-form><textarea name="text"></textarea><button>→</button></form>`;

const page = async (task, storage = {}) => {
  let reloaded = 0; // jsdom does not navigate: a reload is its «not implemented» note
  const vc = new VirtualConsole();
  vc.on('jsdomError', e => { if (/navigation|reload/i.test(e.message)) reloaded++; });
  const dom = new JSDOM(`<!doctype html><body><button id="toggle" data-ai-toggle>CoPilot</button>
    <aside id="aipane" data-ai data-ai-url="/tasks/agent"${task ? ` data-ai-task="${task}"` : ''} hidden></aside></body>`,
  { runScripts: 'outside-only', url: 'http://go24.test/t/WEB-1', virtualConsole: vc });
  const w = dom.window;
  for (const [k, v] of Object.entries(storage)) w.localStorage.setItem(k, v);
  const calls = [];
  let answer = pane('r1', false);
  w.fetch = async (url, opts = {}) => {
    calls.push({ url, method: opts.method || 'GET', body: opts.body ? Object.fromEntries(opts.body) : null });
    return { ok: true, text: async () => answer };
  };
  w.alert = () => {};
  w.eval(script);
  const tick = () => new Promise(r => setTimeout(r, 0));
  const click = el => el.dispatchEvent(new w.MouseEvent('click', { bubbles: true, cancelable: true }));
  return { w, calls, tick, click, $: id => w.document.getElementById(id), set: a => { answer = a; }, reloaded: () => reloaded };
};

{
  const p = await page('WEB-1');
  assert.ok(p.$('aipane').hidden, 'closed at first');
  p.click(p.$('toggle'));
  await p.tick();
  assert.ok(!p.$('aipane').hidden && p.$('toggle').classList.contains('on'), 'the button opens the pane');
  assert.equal(p.calls[0].url, '/tasks/agent', 'the pane is loaded from its address');
  assert.ok(p.$('sum'), 'a question about the open task is there with a task open');
  p.click(p.$('hot'));
  await p.tick(); await p.tick();
  assert.deepEqual(p.calls[1], { url: '/tasks/agent', method: 'POST', body: { task: 'WEB-1', run: 'r1', text: 'Что горит?' } }, 'a quick question goes with the task and the conversation');
  p.click(p.$('fill'));
  assert.equal(p.w.document.querySelector('[data-ai-form] textarea').value, 'Поставь задачу: ', 'a request to finish waits in the box');
  p.set(pane('r1', true));
  p.click(p.$('hot'));
  await p.tick(); await p.tick();
  p.set(pane('r1', false));
  p.click(p.$('yes'));
  await p.tick(); await p.tick();
  assert.equal(p.calls.at(-1).url, '/tasks/agent/r1/a1', '«Подтвердить» posts to the action');
  assert.equal(p.calls.at(-1).body.yes, '1');
  assert.equal(p.reloaded(), 1, 'the page shows the change');
  assert.equal(p.w.localStorage.getItem('ai./tasks/agent'), '1', 'open is kept');
  assert.equal(p.w.localStorage.getItem('ai./tasks/agent.run'), 'r1', 'so is the conversation');
}
{
  const p = await page('', { 'ai./tasks/agent': '1', 'ai./tasks/agent.run': 'r1' });
  await p.tick();
  assert.ok(!p.$('aipane').hidden, 'open again on the next visit');
  assert.equal(p.calls[0].url, '/tasks/agent?run=r1', 'on its conversation');
  assert.ok(!p.$('sum') && p.$('hot'), 'no question about the open task where none is open');
}
console.log('aipane.test.mjs: ok');
