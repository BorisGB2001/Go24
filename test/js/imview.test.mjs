// The viewer of chat files in jsdom (static/js/im.js): a picture, a PDF or a document of a message opens over the
// chat with the sender and the time; ‹ › and ← → go through the files of the chat; a document comes as HTML from
// /im/file/preview; «К сообщению» closes the viewer; Esc closes it; Ctrl + click is left to the browser.
// node test/js/imview.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const im = readFileSync(new URL('../../internal/web/static/js/im.js', import.meta.url), 'utf8');
const tick = (ms = 20) => new Promise(r => setTimeout(r, ms));
const file = (id, kind, name) => `<div class="msg" data-id="${id}" data-sender="Анна Петрова" data-when="25.10.2026 14:2${id.slice(1)}"><div class="bubble">
  <div class="im-file"><a href="/im/file?room=!r&amp;event=${id}" data-im-view="${kind}" data-name="${name}">${name}</a></div></div></div>`;
const list = file('$1', 'image', 'фото.jpg') + `<div class="msg" data-id="$2"><div class="text">просто текст</div></div>` +
  file('$3', 'pdf', 'счёт.pdf') + file('$4', 'html', 'договор.docx');
const dom = new JSDOM(`<!doctype html><html lang="ru-RU"><body><div class="im" data-im data-room="!r" data-failed="ошибка"
  data-view-prev="Предыдущий" data-view-next="Следующий" data-view-download="Скачать" data-view-goto="К сообщению" data-view-close="Закрыть"
  data-view-none="Нет просмотра" data-view-loading="Загрузка…">
  <nav data-list></nav><section class="im-chat"><div class="im-msgs" data-msgs>${list}</div>
  <form class="chatform" data-send><textarea name="body"></textarea><button>s</button></form></section>
  <aside data-thread-pane hidden><div data-thread-msgs></div></aside></div></body></html>`,
{ runScripts: 'outside-only', url: 'http://go24.test/im?room=!r', pretendToBeVisual: true });
const w = dom.window;
w.CSS = { escape: s => s };
w.HTMLElement.prototype.scrollIntoView = () => {};
w.matchMedia = () => ({ matches: false });
const asked = [];
w.fetch = async (u) => {
  asked.push(String(u));
  if (String(u).startsWith('/im/file/preview')) return { ok: true, json: async () => ({ kind: 'html', html: '<p>Договор поставки</p>' }) };
  return { ok: true, json: async () => ({ html: String(u).startsWith('/im/messages') ? list : '' }) };
};
w.EventSource = class { addEventListener() {} close() {} };
w.eval(im);
await tick();
const d = w.document, $ = s => d.querySelector(s);
const link = id => $(`[data-msgs] [data-id="${id}"] a[data-im-view]`);
const click = (a, opts = {}) => { const e = new w.MouseEvent('click', { bubbles: true, cancelable: true, button: 0, ...opts }); a.dispatchEvent(e); return e; };
const key = k => d.dispatchEvent(new w.KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true }));
const bar = () => $('.mviewer .mviewer-bar').textContent;

// a picture: over the chat, with its sender and time, «1 / 3» — only messages with files count
const ev = click(link('$1'));
assert.ok(ev.defaultPrevented, 'the link does not open a tab');
assert.ok($('.mviewer'), 'the viewer is open');
assert.equal($('.mviewer-body img').getAttribute('src'), '/im/file?room=!r&event=$1');
assert.match(bar(), /фото\.jpg/);
assert.match(bar(), /1 \/ 3/);
assert.match(bar(), /Анна Петрова · 25\.10\.2026 14:21/);
assert.equal([...d.querySelectorAll('.mviewer-bar a')].find(a => a.textContent === 'Скачать').getAttribute('href'), '/im/file?room=!r&event=$1&dl=1');

// › — the PDF, in a frame
$('.mviewer .mv-next').click();
await tick();
assert.equal(d.querySelectorAll('.mviewer').length, 1, 'one viewer at a time');
assert.equal($('.mviewer-body iframe.mviewer-pdf').getAttribute('src'), '/im/file?room=!r&event=$3');
assert.match(bar(), /2 \/ 3/);

// → — the document, as HTML from the server
key('ArrowRight');
await tick();
assert.ok(asked.includes('/im/file/preview?room=%21r&event=%244'), asked.join(' '));
assert.equal($('.mviewer-doc').innerHTML, '<p>Договор поставки</p>');
assert.match(bar(), /договор\.docx/);

// → past the last — back to the first; ← — to the last again
key('ArrowRight');
await tick();
assert.match(bar(), /фото\.jpg/);
key('ArrowLeft');
await tick();
assert.match(bar(), /3 \/ 3/);

// Esc closes it
key('Escape');
assert.equal($('.mviewer'), null);

// «К сообщению» closes it too
click(link('$3'));
[...d.querySelectorAll('.mviewer-bar button')].find(b => b.textContent === 'К сообщению').click();
await tick();
assert.equal($('.mviewer'), null);

// a click beside the picture closes it
click(link('$1'));
$('.mviewer').dispatchEvent(new w.MouseEvent('click', { bubbles: true }));
assert.equal($('.mviewer'), null);

// Ctrl + click — the browser's own (a new tab)
const ctrl = click(link('$1'), { ctrlKey: true });
assert.ok(!ctrl.defaultPrevented);
assert.equal($('.mviewer'), null);

console.log('imview: ok');
process.exit(0);
