// The people picker (static/js/people.js) for a guest of collabs and with guests in it: a guest's page (body
// data-guest) has no «Отделы» tab — the structure of the company is not theirs; a guest in the list and in a chip is
// marked «гость» (a chip named by the server too). node test/js/guest.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/people.js', import.meta.url), 'utf8');
const dom = new JSDOM(`<!doctype html><body data-guest>
  <form id="f1"><div class="upick" data-upick data-name="auditors" data-multiple data-tabs="Последние|Отделы|Все" data-empty="Никого"
    data-more="Ещё" data-guest-mark="гость"><input type="text" class="upick-q"></div></form>
  <form id="f2"><div class="upick" data-upick data-name="responsible" data-required data-missing data-guest-mark="гость">
    <span class="upick-chip" data-id="g1"><span class="upick-name"></span><input type="hidden" name="responsible" value="g1"></span>
    <input type="text" class="upick-q"></div></form>
  </body>`, { runScripts: 'outside-only', url: 'http://go24.test/' });
const w = dom.window, d = w.document;
w.CSS = { escape: s => s };
w.TextEncoder = TextEncoder;
const asked = [];
w.fetch = async url => {
  const u = new URL(url, 'http://go24.test/');
  asked.push(u.pathname);
  const body = [{ id: 'o1', name: 'Ольга', position: 'Менеджер', dept: '', photo: '', active: true },
    { id: 'g1', name: 'Борис Клиентов', position: '', dept: '', photo: '', active: true, guest: true }];
  return { ok: true, json: async () => body };
};
const wait = ms => new Promise(r => setTimeout(r, ms));
w.eval(script);
await wait(10);
// a chip the page could not name: named by the server, a guest marked
const named = d.querySelector('#f2 .upick-chip');
assert.equal(named.querySelector('.upick-name').firstChild.textContent, 'Борис Клиентов');
assert.equal(named.querySelector('.pill.guest')?.textContent, 'гость');

const q = d.querySelector('.upick-q');
q.focus();
await wait(10);
const tabs = [...d.querySelectorAll('.upick-tabs button')].map(b => b.dataset.tab);
assert.deepEqual(tabs, ['recent', 'all'], 'a guest has no «Отделы»');
const rows = [...d.querySelectorAll('.upick-pop .upick-row')];
assert.equal(rows.length, 2);
assert.equal(rows[0].querySelector('.pill.guest'), null);
assert.equal(rows[1].querySelector('.pill.guest')?.textContent, 'гость');
// the guest chosen: the chip is marked too
q.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true }));
q.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }));
const chip = d.querySelector('.upick-chip');
assert.equal(chip.dataset.id, 'g1');
assert.equal(chip.querySelector('.pill.guest')?.textContent, 'гость');
assert.ok(!asked.includes('/people/departments'), 'the structure is never asked for');
console.log('guest.test.mjs ok');
