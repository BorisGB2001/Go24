// Pinned messages and the search of messages in the messenger (static/js/im.js) in jsdom: pinning puts the bar the
// server gives under the header and turns the menu's item into «Открепить»; a pin of an older message loads the page
// around it with «К последним»; the search goes in the chat or in all chats, a hit in the open chat jumps to it, the
// list's search box offers the search of messages. node test/js/impins.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const im = readFileSync(new URL('../../internal/web/static/js/im.js', import.meta.url), 'utf8');
const tick = () => new Promise(r => setTimeout(r, 20));
const msg = (id, text) => `<div class="msg" data-id="${id}"><div class="bubble"><details class="im-menu"><summary>…</summary><div class="pop">
  <button data-act="pin"><i></i> Закрепить</button></div></details><div class="text">${text}</div></div></div>`;
const bar = id => `<details class="im-pinbar"><summary>Закреплено</summary><div class="pop"><div class="im-pin"><a href="#" data-goto="${id}">${id}</a>
  <button data-unpin="${id}">x</button></div></div></details>`;
const dom = new JSDOM(`<!doctype html><body><div class="im" data-im data-room="!r" data-failed="Не удалось" data-pin-label="Закрепить"
  data-unpin-label="Открепить" data-search-in="Искать в сообщениях:" data-search-here="Поиск по чату" data-search-all-title="Поиск по всем чатам">
  <input type="search" data-filter><button data-search-all hidden><span></span></button>
  <nav data-list><a class="im-room" data-room-id="!r" data-name="Мы"><span data-preview></span><span data-unread hidden></span></a></nav>
  <section class="im-chat"><button data-search-open>?</button><div data-pins></div><div class="im-msgs" data-msgs></div><button data-latest hidden>↓</button>
    <form class="chatform" data-send><textarea name="body"></textarea></form></section>
  <aside data-search-pane hidden><b data-search-title></b><button data-close-search>x</button>
    <form data-search><input type="search" name="q"><input type="checkbox" name="all"></form><div data-hits></div></aside>
  <aside data-thread-pane hidden><div data-thread-msgs></div><form data-send-thread><textarea name="body"></textarea></form></aside>
  </div></body>`, { runScripts: 'outside-only', url: 'http://go24.test/im?room=!r', pretendToBeVisual: true });
const w = dom.window, d = w.document;
w.CSS = { escape: s => s };
w.HTMLElement.prototype.scrollIntoView = () => {};
const asked = [];
w.fetch = async (url, opts = {}) => {
  asked.push(url);
  if (url === '/im/messages?room=!r') return { ok: true, json: async () => ({ html: msg('$a', 'привет') + msg('$b', 'план') }) };
  if (url === '/im/messages?room=!r&at=%24old') return { ok: true, json: async () => ({ html: msg('$z', 'раньше') + msg('$old', 'давнее') }) };
  if (url.startsWith('/im/search')) {
    const hit = (id, room) => `<a class="im-hit" href="/im?room=${room}&at=${id}" data-hit="${id}" data-hit-room="${room}" data-hit-thread="">${id}</a>`;
    return { ok: true, json: async () => ({ html: hit('$b', '!r') + hit('$x', '!other') }) };
  }
  if (url === '/im/pin') {
    const p = Object.fromEntries(new URLSearchParams(String(opts.body)));
    asked.push(JSON.stringify(p));
    return { ok: true, json: async () => ({ html: p.on === '1' ? bar(p.event) : '' }) };
  }
  return { ok: true, json: async () => ({}) };
};
w.EventSource = class { addEventListener() {} };
w.eval(im);
await tick();
const $ = s => d.querySelector(s);

// pin: the bar comes, the menu offers to unpin; the ✕ in the bar takes it back
$('[data-id="$b"] [data-act="pin"]').click();
await tick();
assert.equal(asked.at(-1), JSON.stringify({ room: '!r', event: '$b', on: '1' }));
assert.ok($('[data-pins] [data-goto="$b"]'), 'the bar of pins');
const item = $('[data-id="$b"] .pop button');
assert.equal(item.dataset.act, 'unpin');
assert.equal(item.textContent.trim(), 'Открепить');
$('[data-unpin="$b"]').click();
await tick();
assert.equal(asked.at(-1), JSON.stringify({ room: '!r', event: '$b', on: '0' }));
assert.equal($('[data-pins]').innerHTML, '');
assert.equal(item.dataset.act, 'pin');

// a pin of a message older than the page: the page around it, «К последним» goes back
$('[data-pins]').innerHTML = bar('$old');
$('[data-goto="$old"]').click();
await tick();
assert.ok($('[data-msgs] [data-id="$old"]') && !$('[data-msgs] [data-id="$a"]'), 'the page around the pin');
assert.equal($('[data-latest]').hidden, false);
$('[data-latest]').click();
await tick();
assert.ok($('[data-msgs] [data-id="$a"]'));
assert.equal($('[data-latest]').hidden, true);

// the search in the chat; a hit of this chat jumps to it, one of another chat is a link
$('[data-search-open]').click();
assert.equal($('[data-search-pane]').hidden, false);
assert.equal($('[data-search-title]').textContent, 'Поиск по чату');
$('[data-search] [name="q"]').value = 'план';
$('[data-search]').requestSubmit();
await tick();
assert.equal(asked.find(u => u.startsWith('/im/search')), '/im/search?q=%D0%BF%D0%BB%D0%B0%D0%BD&room=!r');
const ev = new w.MouseEvent('click', { bubbles: true, cancelable: true });
$('[data-hit="$b"]').dispatchEvent(ev);
assert.equal(ev.defaultPrevented, true, 'a hit of the open chat stays on the page');
await tick();
assert.ok($('[data-id="$b"]').classList.contains('flash-msg'));
const other = new w.MouseEvent('click', { bubbles: true, cancelable: true });
$('[data-hit="$x"]').dispatchEvent(other);
assert.equal(other.defaultPrevented, false, 'another chat opens by its link');

// all chats: the list's search box offers it, Enter searches without the room
const filter = $('[data-filter]');
filter.value = 'отчёт';
filter.dispatchEvent(new w.Event('input', { bubbles: true }));
assert.equal($('[data-search-all]').hidden, false);
assert.equal($('[data-search-all] span').textContent, 'Искать в сообщениях: «отчёт»');
asked.length = 0;
filter.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }));
await tick();
assert.equal(asked[0], '/im/search?q=%D0%BE%D1%82%D1%87%D1%91%D1%82');
assert.equal($('[data-search-title]').textContent, 'Поиск по всем чатам');
assert.equal($('[data-search] [name="all"]').checked, true);
console.log('im.js: pins and search passed');
