// «Медиа, файлы и ссылки» of a chat in the messenger (static/js/im.js) in jsdom: the button of the header opens the
// chat's panel in the side pane without the search box, the tabs switch the lists, a date goes to its message.
// node test/js/immedia.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const im = readFileSync(new URL('../../internal/web/static/js/im.js', import.meta.url), 'utf8');
const tick = () => new Promise(r => setTimeout(r, 20));
const msg = id => `<div class="msg" data-id="${id}"><div class="bubble"><div class="text">${id}</div><span class="time">12:00</span></div></div>`;
const panel = `<div class="tabs" data-media-tabs><a href="#" class="on" data-media-tab="media">Медиа</a><a href="#" data-media-tab="files">Файлы</a>
  <a href="#" data-media-tab="links">Ссылки</a></div>
  <div data-media-list="media"><a href="#" data-goto="$a"><img src="/im/file?room=!r&event=$a"></a></div>
  <div data-media-list="files" hidden><div class="im-hit"><a href="/im/file?room=!r&event=$b&dl=1" download>план.pdf</a> <a href="#" data-goto="$b">21.10</a></div></div>
  <div data-media-list="links" hidden></div>`;
const dom = new JSDOM(`<!doctype html><body><div class="im" data-im data-room="!r" data-failed="Не удалось" data-media-title="Медиа, файлы и ссылки"
  data-search-here="Поиск по чату" data-search-all-title="Поиск по всем чатам">
  <nav data-list><a class="im-room" data-room-id="!r" data-name="Мы"><span data-preview></span><span data-unread hidden></span></a></nav>
  <section class="im-chat"><button data-media-open>▦</button><div data-pins></div><div class="im-msgs" data-msgs></div>
    <form class="chatform" data-send><textarea name="body"></textarea></form></section>
  <aside data-search-pane hidden><b data-search-title></b><button data-close-search>x</button>
    <form data-search><input type="search" name="q"><input type="checkbox" name="all"></form><div data-hits></div></aside>
  <aside data-thread-pane hidden><div data-thread-msgs></div><form data-send-thread><textarea name="body"></textarea></form></aside>
  </div></body>`, { runScripts: 'outside-only', url: 'http://go24.test/im?room=!r', pretendToBeVisual: true });
const w = dom.window, d = w.document;
w.CSS = { escape: s => s };
let scrolled = null;
w.HTMLElement.prototype.scrollIntoView = function () { scrolled = this; };
const asked = [];
w.fetch = async url => {
  asked.push(url);
  if (url.startsWith('/im/messages')) return { ok: true, json: async () => ({ html: msg('$a') + msg('$b') }) };
  if (url.startsWith('/im/media')) return { ok: true, json: async () => ({ html: panel }) };
  return { ok: true, status: 204, json: async () => ({}) };
};
w.EventSource = class { addEventListener() {} close() {} };
w.eval(im);
await tick();
const $ = s => d.querySelector(s);

$('[data-media-open]').click();
await tick();
assert.ok(asked.includes('/im/media?room=!r'));
assert.equal($('[data-search-pane]').hidden, false);
assert.equal($('[data-search]').hidden, true);
assert.equal($('[data-search-title]').textContent, 'Медиа, файлы и ссылки');
const list = k => $(`[data-media-list="${k}"]`);
assert.equal(list('media').hidden, false);
assert.equal(list('files').hidden, true);

// the tabs
$('[data-media-tab="files"]').click();
assert.equal(list('media').hidden, true);
assert.equal(list('files').hidden, false);
assert.ok($('[data-media-tab="files"]').classList.contains('on'));
assert.ok(!$('[data-media-tab="media"]').classList.contains('on'));
$('[data-media-tab="links"]').click();
assert.equal(list('files').hidden, true);
assert.equal(list('links').hidden, false);

// a date goes to its message in the chat
$('[data-media-tab="files"]').click();
list('files').querySelector('[data-goto]').click();
await tick();
assert.equal(scrolled?.dataset.id, '$b');
console.log('im.js: the chat media panel passed');
