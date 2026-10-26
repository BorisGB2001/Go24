// «Избранное» and «Напомнить» in the messenger (static/js/im.js) in jsdom: «В избранное» marks the message with ★ and
// turns the item into «Убрать из избранного» (and back); «Напомнить» opens the choice, «Через час» tells go24 and
// marks the message with ⏰, «Не напоминать» takes the mark away; the star of the list opens the person's kept
// messages in the side pane without the search box. node test/js/imsaved.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const im = readFileSync(new URL('../../internal/web/static/js/im.js', import.meta.url), 'utf8');
const tick = () => new Promise(r => setTimeout(r, 20));
const msg = id => `<div class="msg" data-id="${id}"><div class="bubble"><details class="im-menu"><summary>…</summary><div class="pop">
  <button data-act="save"><i></i> В избранное</button><button data-act="remind"><i></i> Напомнить</button></div></details>
  <div class="text">${id}</div><span class="time">12:00</span></div></div>`;
const dom = new JSDOM(`<!doctype html><body><div class="im" data-im data-room="!r" data-failed="Не удалось" data-save-label="В избранное"
  data-unsave-label="Убрать из избранного" data-saved-title="Избранное" data-search-here="Поиск по чату" data-search-all-title="Поиск по всем чатам">
  <button data-saved-open>★</button>
  <nav data-list><a class="im-room" data-room-id="!r" data-name="Мы"><span data-preview></span><span data-unread hidden></span></a></nav>
  <section class="im-chat"><div data-pins></div><div class="im-msgs" data-msgs></div>
    <dialog data-remind><form method="dialog"><button type="button" data-remind-at="1h">Через час</button>
      <input type="datetime-local" data-remind-pick><button type="button" data-remind-at="pick">Сохранить</button>
      <button type="button" data-remind-at="">Не напоминать</button></form></dialog>
    <form class="chatform" data-send><textarea name="body"></textarea></form></section>
  <aside data-search-pane hidden><b data-search-title></b><button data-close-search>x</button>
    <form data-search><input type="search" name="q"><input type="checkbox" name="all"></form><div data-hits></div></aside>
  <aside data-thread-pane hidden><div data-thread-msgs></div><form data-send-thread><textarea name="body"></textarea></form></aside>
  </div></body>`, { runScripts: 'outside-only', url: 'http://go24.test/im?room=!r', pretendToBeVisual: true });
const w = dom.window, d = w.document;
w.CSS = { escape: s => s };
w.HTMLElement.prototype.scrollIntoView = () => {};
const posts = [], asked = [];
w.fetch = async (url, opts = {}) => {
  asked.push(url);
  if (url.startsWith('/im/messages')) return { ok: true, json: async () => ({ html: msg('$a') + msg('$b') }) };
  const p = Object.fromEntries(new URLSearchParams(String(opts.body)));
  if (opts.body) posts.push([url, p]);
  if (url === '/im/remind') return { ok: true, json: async () => ({ when: p.at ? 'Напомню 22.10.2026 09:00' : '' }) };
  if (url === '/im/saved') return { ok: true, json: async () => ({ html: '<a class="im-hit" href="/im?room=!r&at=$a" data-hit="$a" data-hit-room="!r">$a</a>' }) };
  return { ok: true, status: 204, json: async () => ({}) };
};
w.EventSource = class { addEventListener() {} close() {} };
w.eval(im);
await tick();
const $ = s => d.querySelector(s);
const a = () => $('[data-id="$a"]');

// «В избранное» and back
a().querySelector('[data-act="save"]').click();
await tick();
assert.deepEqual(posts.at(-1), ['/im/save', { room: '!r', event: '$a', on: '1' }]);
assert.equal(a().querySelector('.im-saved').textContent, '★');
const item = a().querySelector('[data-act="unsave"]');
assert.equal(item.textContent.trim(), 'Убрать из избранного');
item.click();
await tick();
assert.deepEqual(posts.at(-1), ['/im/save', { room: '!r', event: '$a', on: '0' }]);
assert.equal(a().querySelector('.im-saved'), null);
assert.ok(a().querySelector('[data-act="save"]'));

// «Напомнить»: «Через час» marks it with ⏰; a time not chosen sends nothing; «Не напоминать» takes the mark away
a().querySelector('[data-act="remind"]').click();
assert.ok($('[data-remind]').open);
$('[data-remind-at="1h"]').click();
await tick();
assert.deepEqual(posts.at(-1), ['/im/remind', { room: '!r', event: '$a', at: '1h' }]);
assert.equal(a().querySelector('.im-reminder').title, 'Напомню 22.10.2026 09:00');
assert.ok(!$('[data-remind]').open);
const n = posts.length;
a().querySelector('[data-act="remind"]').click();
$('[data-remind-at="pick"]').click();
await tick();
assert.equal(posts.length, n, 'no time chosen');
$('[data-remind-pick]').value = '2026-10-23T14:15';
$('[data-remind-at="pick"]').click();
await tick();
assert.deepEqual(posts.at(-1), ['/im/remind', { room: '!r', event: '$a', at: '2026-10-23T14:15' }]);
a().querySelector('[data-act="remind"]').click();
$('[data-remind-at=""]').click();
await tick();
assert.equal(a().querySelector('.im-reminder'), null);

// the star of the list: the kept messages in the side pane, no search box
$('[data-saved-open]').click();
await tick();
assert.ok(asked.includes('/im/saved'));
assert.equal($('[data-search-pane]').hidden, false);
assert.equal($('[data-search]').hidden, true);
assert.equal($('[data-search-title]').textContent, 'Избранное');
assert.equal($('[data-hits] .im-hit').dataset.hit, '$a');
console.log('im.js: saved messages and reminders passed');
