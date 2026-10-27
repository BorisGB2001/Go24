// «База знаний»: the editor keeps its text as a draft — at once (the others see it open), a few seconds after
// typing stops; the answer fills the line under it; a submitted form stops it, typing again resumes it; leaving
// without saving sends act=leave; leaving asks nothing once the draft is kept (kbedit.js), in jsdom.
// node test/js/kbdraft.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const draft = readFileSync(new URL('../../internal/web/static/js/kbdraft.js', import.meta.url), 'utf8');
const edit = readFileSync(new URL('../../internal/web/static/js/kbedit.js', import.meta.url), 'utf8');
const dom = new JSDOM(`<!doctype html><body><form method="post" action="/kb/s1/p1" data-kb-draft="/kb/s1/p1/draft">
  <input type="hidden" name="base" value="1700000000000000"><input name="title" value="Отпуск">
  <textarea name="body" data-kb-leave="Уйти?">текст</textarea>
  <p><span data-kb-draft-saved></span> <span data-kb-editing></span> <b data-kb-changed></b></p></form></body>`,
{ runScripts: 'outside-only', url: 'http://go24.test/kb/s1/p1?edit=1' });
const w = dom.window, d = w.document;
const sent = [], beacons = [];
let answer = {};
w.fetch = async (url, opts) => {
  sent.push({ url, title: opts.body.get('title'), body: opts.body.get('body'), base: opts.body.get('base'), act: opts.body.get('act') });
  return { ok: true, status: 200, json: async () => answer };
};
w.navigator.sendBeacon = (url, data) => { beacons.push({ url, act: data.get('act'), body: data.get('body') }); return true; };
// timers by hand: the pause after typing and the beat
const timers = new Map();
let next = 1;
w.setTimeout = (fn, ms) => { timers.set(next, { fn, ms }); return next++; };
w.clearTimeout = id => timers.delete(id);
w.setInterval = (fn, ms) => { timers.set(next, { fn, ms, every: true }); return next++; };
w.clearInterval = id => timers.delete(id);
const fire = ms => { for (const [id, t] of [...timers]) if (t.ms === ms) { if (!t.every) timers.delete(id); t.fn(); } };
const tick = async () => { for (let i = 0; i < 5; i++) await new Promise(r => setImmediate(r)); };
w.eval(edit);
w.eval(draft);
await tick();

// at once: the editor is open, with the base the page was opened at
assert.equal(sent.length, 1);
assert.deepEqual([sent[0].url, sent[0].base, sent[0].act], ['/kb/s1/p1/draft', '1700000000000000', null]);

// typing: saved after the pause, the line shows the answer
const ta = d.querySelector('textarea');
ta.value = 'текст 2';
ta.dispatchEvent(new w.Event('input', { bubbles: true }));
assert.equal(sent.length, 1);
answer = { saved: 'Черновик сохранён в 14:05', editing: 'Сейчас редактирует: Босс', changed: 'Пока вы правили, страницу сохранили: Босс' };
fire(3000);
await tick();
assert.equal(sent.length, 2);
assert.equal(sent[1].body, 'текст 2');
assert.equal(d.querySelector('[data-kb-draft-saved]').textContent, 'Черновик сохранён в 14:05');
assert.equal(d.querySelector('[data-kb-editing]').textContent, 'Сейчас редактирует: Босс');
assert.equal(d.querySelector('[data-kb-changed]').textContent, 'Пока вы правили, страницу сохранили: Босс');

// the beat: every half a minute, even unchanged
answer = {};
fire(30000);
await tick();
assert.equal(sent.length, 3);
assert.equal(d.querySelector('[data-kb-editing]').textContent, '');

// the text kept as a draft: leaving asks nothing
let leave = new w.Event('beforeunload', { cancelable: true });
w.dispatchEvent(leave);
assert.ok(!leave.defaultPrevented);
// typed, not yet kept: it asks
ta.value = 'текст 3';
ta.dispatchEvent(new w.Event('input', { bubbles: true }));
leave = new w.Event('beforeunload', { cancelable: true });
w.dispatchEvent(leave);
assert.ok(leave.defaultPrevented);

// submitted: no more drafts, nor a leave as the page goes
const f = d.querySelector('form');
f.dispatchEvent(new w.Event('submit', { cancelable: true }));
fire(3000);
fire(30000);
await tick();
assert.equal(sent.length, 3);
// the form came back with a mistake: typing on keeps the draft again
ta.value = 'текст 4';
ta.dispatchEvent(new w.Event('input', { bubbles: true }));
fire(3000);
await tick();
assert.equal(sent.length, 4);
assert.equal(sent[3].body, 'текст 4');

// closing the tab unsaved: the text once more, act=leave
w.dispatchEvent(new w.Event('pagehide'));
assert.deepEqual(beacons, [{ url: '/kb/s1/p1/draft', act: 'leave', body: 'текст 4' }]);
w.dispatchEvent(new w.Event('pagehide'));
assert.equal(beacons.length, 1);

// «Отменить черновик»: nothing more is sent, not even as the page goes
const dom2 = new JSDOM(`<!doctype html><body><form data-kb-draft-drop><input name="act" value="drop"></form>
  <form data-kb-draft="/kb/s1/p1/draft"><input name="title" value="x"><textarea name="body">черновик</textarea></form></body>`, { runScripts: 'outside-only' });
const beacons2 = [];
dom2.window.fetch = async () => ({ ok: true, json: async () => ({}) });
dom2.window.navigator.sendBeacon = (url, data) => { beacons2.push(data.get('act')); return true; };
dom2.window.eval(draft);
dom2.window.document.querySelector('[data-kb-draft-drop]').dispatchEvent(new dom2.window.Event('submit', { cancelable: true }));
dom2.window.dispatchEvent(new dom2.window.Event('pagehide'));
assert.deepEqual(beacons2, []);
console.log('kbdraft ok');
