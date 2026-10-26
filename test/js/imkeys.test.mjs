// Keys and clicks of the messenger (static/js/im.js) in jsdom: ↑ in an empty field edits one's last
// message (not another's, not when something is typed), a double click on a message puts 👍 (not on its links),
// «Прочитать всё» reads every chat and clears the counters, «@» and the marks. node test/js/imkeys.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const im = readFileSync(new URL('../../internal/web/static/js/im.js', import.meta.url), 'utf8');
const tick = () => new Promise(r => setTimeout(r, 20));
const msg = (id, text, mine) => `<div class="msg${mine ? ' mine' : ''}" data-id="${id}"><div class="bubble">
  <details class="im-menu"><summary>…</summary><div class="pop"><button data-act="reply">r</button>${mine ? '<button data-act="edit">e</button>' : ''}</div></details>
  <div class="text" data-body="${text}">${text} <a href="https://x.ru">x</a></div>
  <div class="reacts"><details class="react-add"><summary>☺</summary><div class="react-pick"><button data-react="👍">👍</button></div></details></div></div></div>`;
const row = (id, n, marked) => `<div class="im-row" data-row data-marked="${marked ? '1' : ''}"><a class="im-room" data-room-id="${id}" data-name="${id}">
  <span class="cnt at" data-mention="${n}"${n ? '' : ' hidden'}>@</span><span class="cnt" data-unread${n ? '' : ' hidden'}>${n}</span></a></div>`;
const dom = new JSDOM(`<!doctype html><body><div class="im" data-im data-room="!r" data-failed="Не удалось" data-edit-hint="Изменение" data-reply-hint="Ответ на">
  <button data-read-all>✓✓</button>
  <nav data-list>${row('!r', 0)}${row('!a', 3)}${row('!b', 0, true)}</nav>
  <section class="im-chat"><div class="im-msgs" data-msgs></div>
    <div data-replying hidden><span data-replying-text></span><button data-reply-cancel>x</button></div>
    <form class="chatform" data-send><textarea name="body"></textarea><button>s</button></form></section>
  <aside data-thread-pane hidden><div data-thread-msgs></div><form data-send-thread><textarea name="body"></textarea></form></aside>
  </div></body>`, { runScripts: 'outside-only', url: 'http://go24.test/im?room=!r', pretendToBeVisual: true });
const w = dom.window, d = w.document;
w.CSS = { escape: s => s };
w.HTMLElement.prototype.scrollIntoView = () => {};
const posts = [];
w.fetch = async (url, opts = {}) => {
  if (url.startsWith('/im/messages')) return { ok: true, json: async () => ({ html: msg('$1', 'моё старое', true) + msg('$2', 'моё последнее', true) + msg('$3', 'чужое') }) };
  posts.push([url, Object.fromEntries(new URLSearchParams(String(opts.body)))]);
  return { ok: true, json: async () => ({ ok: true }) };
};
w.EventSource = class { constructor() { this.h = {}; } addEventListener(t, f) { this.h[t] = f; } close() {} };
w.eval(im);
await tick();
const $ = s => d.querySelector(s);
const text = $('[data-send] textarea');
const key = (k, o = {}) => text.dispatchEvent(new w.KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true, ...o }));

// ↑ with something typed — nothing; with Shift — nothing
text.value = 'черновик';
key('ArrowUp');
assert.equal($('[data-send]').dataset.edit, undefined);
text.value = '';
key('ArrowUp', { shiftKey: true });
assert.equal($('[data-send]').dataset.edit, undefined);
// ↑ in the empty field: one's last message (the other's after it is skipped) is edited
key('ArrowUp');
assert.equal($('[data-send]').dataset.edit, '$2');
assert.equal(text.value, 'моё последнее');
assert.equal($('[data-replying]').hidden, false);
key('Escape');
assert.equal($('[data-send]').dataset.edit, undefined);

// a double click on a message: 👍 for it; on its link — nothing
const dbl = el => el.dispatchEvent(new w.MouseEvent('dblclick', { bubbles: true }));
dbl($('[data-id="$3"] .text'));
await tick();
assert.deepEqual(posts.at(-1), ['/im/react', { room: '!r', event: '$3', emoji: '👍' }]);
const n = posts.length;
dbl($('[data-id="$3"] .text a'));
await tick();
assert.equal(posts.length, n);

// «Прочитать всё»: one request, the counters, «@» and the marks are gone
$('[data-read-all]').click();
await tick();
assert.deepEqual(posts.at(-1), ['/im/readall', {}]);
assert.equal($('[data-room-id="!a"] [data-unread]').hidden, true);
assert.equal($('[data-room-id="!a"] [data-mention]').hidden, true);
assert.equal(d.querySelectorAll('[data-row][data-marked="1"]').length, 0);
console.log('imkeys: ok');
