// «Почта» → «Быстрые действия»: each step of the settings form shows only what its action takes, in jsdom.
// node test/js/mailquick.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/mailquick.js', import.meta.url), 'utf8');
const step = (i, what) => `<div data-step><select name="do_${i}"><option value=""></option><option value="move"${what === 'move' ? ' selected' : ''}>m</option>
  <option value="read">r</option><option value="replyall"${what === 'replyall' ? ' selected' : ''}>a</option></select>
  <select name="folder_${i}" data-for="move"></select><input name="to_${i}" data-for="forward"><select name="tpl_${i}" data-for="reply replyall"></select></div>`;
const dom = new JSDOM(`<!doctype html><body><form data-quick-form>${step(0, 'move')}${step(1, 'replyall')}${step(2, '')}</form></body>`,
  { runScripts: 'outside-only', url: 'http://go24.test/mail/settings?tab=quick' });
const w = dom.window;
w.eval(script);
const shown = i => [...w.document.querySelectorAll('[data-step]')[i].querySelectorAll('[data-for]')].filter(f => !f.hidden && !f.disabled).map(f => f.name);

// as it opens: a move shows the folder, a reply the template, an empty step nothing
assert.deepEqual(shown(0), ['folder_0']);
assert.deepEqual(shown(1), ['tpl_1']);
assert.deepEqual(shown(2), []);

// another action chosen: its field instead (hidden ones are not sent)
const sel = w.document.querySelector('[name="do_0"]');
sel.value = 'read';
sel.dispatchEvent(new w.Event('change', { bubbles: true }));
assert.deepEqual(shown(0), []);
assert.equal(w.document.querySelector('[name="folder_0"]').disabled, true);

console.log('mailquick: ok');
