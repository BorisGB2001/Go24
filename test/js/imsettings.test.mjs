// «Настройки» of the messenger in jsdom (static/js/im.js): the gear opens the window; Enter sends (Shift+Enter — a new
// line) or, when chosen, Ctrl+Enter sends and Enter is a new line. node test/js/imsettings.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const im = readFileSync(new URL('../../internal/web/static/js/im.js', import.meta.url), 'utf8');
const tick = () => new Promise(r => setTimeout(r, 20));
const page = async ctrl => {
  const dom = new JSDOM(`<!doctype html><body><div class="im" data-im data-room="!r" data-failed="x"${ctrl ? ' data-ctrl-enter' : ''}>
    <button data-settings-open>⚙</button><dialog data-settings><form><button type="button" data-settings-close>x</button></form></dialog>
    <nav data-list></nav><section class="im-chat"><div class="im-msgs" data-msgs></div>
    <form class="chatform" data-send><textarea name="body"></textarea><button>s</button></form></section>
    <aside data-thread-pane hidden><div data-thread-msgs></div></aside></div></body>`,
  { runScripts: 'outside-only', url: 'http://go24.test/im?room=!r', pretendToBeVisual: true });
  const w = dom.window;
  w.CSS = { escape: s => s };
  w.HTMLElement.prototype.scrollIntoView = () => {};
  w.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
  w.HTMLDialogElement.prototype.close = function () { this.open = false; };
  w.fetch = async () => ({ ok: true, json: async () => ({ html: '' }) });
  w.EventSource = class { addEventListener() {} close() {} };
  w.eval(im);
  await tick();
  const d = w.document, form = d.querySelector('[data-send]'), text = form.querySelector('textarea');
  let sent = 0;
  form.requestSubmit = () => { sent++; };
  const key = o => { const e = new w.KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true, ...o }); text.dispatchEvent(e); return e.defaultPrevented; };
  return { d, key, sent: () => sent };
};

let p = await page(false);
assert.equal(p.key({}), true, 'Enter sends');
assert.equal(p.sent(), 1);
assert.equal(p.key({ shiftKey: true }), false, 'Shift+Enter — a new line');
assert.equal(p.key({ ctrlKey: true }), true, 'Ctrl+Enter sends as well');
assert.equal(p.sent(), 2);
p.d.querySelector('[data-settings-open]').click();
assert.equal(p.d.querySelector('[data-settings]').open, true, 'the gear opens «Настройки»');
p.d.querySelector('[data-settings-close]').click();
assert.equal(p.d.querySelector('[data-settings]').open, false);

p = await page(true);
assert.equal(p.key({}), false, 'Ctrl+Enter chosen: Enter — a new line');
assert.equal(p.sent(), 0);
assert.equal(p.key({ ctrlKey: true }), true, 'Ctrl+Enter sends');
assert.equal(p.key({ metaKey: true }), true, '⌘+Enter sends');
assert.equal(p.sent(), 2);
console.log('im.js: settings passed');
