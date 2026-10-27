// Open chats in the messenger's list (static/js/im.js) in jsdom: the open chats one is not in stay
// hidden on «Все чаты»; «Каналы» shows the open channels, the search box — any open chat whose name or description
// has the words; «Звонки» hides them.
// node test/js/imopen.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const im = readFileSync(new URL('../../internal/web/static/js/im.js', import.meta.url), 'utf8');
const tick = () => new Promise(r => setTimeout(r, 20));
const open = (id, name, kind) => `<a href="/im?room=${id}" class="im-room im-orow" data-open-row data-name="${name}" data-kind="${kind}"><b>${name}</b></a>`;
const dom = new JSDOM(`<!doctype html><body><div class="im" data-im data-room="" data-tab="all" data-failed="Не удалось">
  <a href="/im" data-im-tab="all">Все</a><a href="/im?tab=channel" data-im-tab="channel">К</a><a href="/im?tab=calls" data-im-tab="calls">З</a>
  <input type="search" data-filter><div data-calls hidden></div>
  <nav data-list><div class="im-row" data-row><a class="im-room" data-room-id="!a" data-name="Анна" data-kind="dm"></a></div></nav>
  <div class="im-open" data-open-list hidden><b>Открытые</b>${open('!n', 'Новости компании Объявления', 'channel')}${open('!o', 'Обеды ', 'group')}</div>
  </div></body>`, { runScripts: 'outside-only', url: 'http://go24.test/im', pretendToBeVisual: true });
const w = dom.window, d = w.document;
w.CSS = { escape: s => s };
w.fetch = async () => ({ ok: true, json: async () => ({ html: '' }) });
w.EventSource = class { addEventListener() {} close() {} };
w.eval(im);
await tick();
const $ = s => d.querySelector(s);
const shown = () => [...d.querySelectorAll('[data-open-row]')].filter(a => !a.hidden).map(a => a.getAttribute('href').slice(9)).join(' ');
const search = q => { const f = $('[data-filter]'); f.value = q; f.dispatchEvent(new w.Event('input')); };

// «Все чаты»: not shown
assert.equal($('[data-open-list]').hidden, true);
// «Каналы»: the open channels only
$('[data-im-tab="channel"]').click();
assert.equal($('[data-open-list]').hidden, false);
assert.equal(shown(), '!n');
// the search: any open chat by its name or description, on any tab
$('[data-im-tab="all"]').click();
search('обед');
assert.equal($('[data-open-list]').hidden, false);
assert.equal(shown(), '!o');
search('объявл');
assert.equal(shown(), '!n');
search('нет такого');
assert.equal($('[data-open-list]').hidden, true);
// «Звонки»: hidden
search('');
$('[data-im-tab="channel"]').click();
$('[data-im-tab="calls"]').click();
assert.equal($('[data-open-list]').hidden, true);
console.log('imopen ok');
