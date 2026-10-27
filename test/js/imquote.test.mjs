// «Цитировать» and «Сохранить на Диск» of a message's menu in jsdom (static/js/im.js): the quote with its author and
// time goes into the field after what is typed there (the message's own quotes left out); the file goes to the drive
// and a toast leads to the folder; a click on the author's name mentions them. node test/js/imquote.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const im = readFileSync(new URL('../../internal/web/static/js/im.js', import.meta.url), 'utf8');
const tick = () => new Promise(r => setTimeout(r, 20));
const ts = new Date(2026, 9, 25, 11, 20).getTime();
const msg = (id, sender, inner, act) => `<div class="msg" data-id="${id}" data-sender="${sender}" data-ts="${ts}"><div class="bubble">
  <details class="im-menu" open><div class="pop"><button type="button" data-act="${act}">x</button></div></details>${inner}</div></div>`;
const list = `${msg('$1', 'Анна', '<div class="text" data-body="[quote]старое[/quote]\n> ещё старое\nСверить *счета*\nдо пятницы">…</div>', 'quote')}
  ${msg('$2', 'Борис', '<div class="im-file"><a class="im-filecard" href="#"><span>акт.pdf</span></a></div>', 'quote')}
  ${msg('$3', 'Борис', '<div class="im-file"><a class="im-filecard" href="#"><span>акт.pdf</span></a></div>', 'drive')}
  ${msg('$4', 'Анна Петрова', '<b class="who" data-mention-name>Анна Петрова</b><div class="text">Привет</div>', 'copy')}`;
const dom = new JSDOM(`<!doctype html><html lang="ru-RU"><body><div class="im" data-im data-room="!r" data-failed="x">
  <nav data-list></nav><section class="im-chat"><div class="im-msgs" data-msgs>${list}</div>
  <form class="chatform" data-send><textarea name="body"></textarea><button>s</button></form></section>
  <aside data-thread-pane hidden><div data-thread-msgs></div></aside></div></body></html>`,
{ runScripts: 'outside-only', url: 'http://go24.test/im?room=!r', pretendToBeVisual: true });
const w = dom.window;
w.CSS = { escape: s => s };
w.HTMLElement.prototype.scrollIntoView = () => {};
const posts = [];
w.fetch = async (u, opts = {}) => {
  if (opts.method === 'POST') posts.push({ u, body: String(opts.body) });
  return { ok: true, json: async () => (u === '/im/file/drive' ? { text: 'Сохранено', url: '/disk/my?folder=f1' } : { html: u.startsWith('/im/messages') ? list : '' }) };
};
w.EventSource = class { addEventListener() {} close() {} };
w.eval(im);
await tick();
const d = w.document, text = d.querySelector('[data-send] textarea');
let inputs = 0;
text.addEventListener('input', () => inputs++);
const at = new Date(ts).toLocaleString('ru-RU', { day: '2-digit', month: '2-digit', year: 'numeric', hour: '2-digit', minute: '2-digit' });

text.value = 'Смотри';
d.querySelector('[data-id="$1"] [data-act]').click();
await tick();
assert.equal(text.value, `Смотри\n[quote]*Анна*, ${at}\nСверить *счета*\nдо пятницы[/quote]\n`, 'the quote after the typed text, without old quotes');
assert.equal(d.activeElement, text, 'the field is focused');
assert.equal(text.selectionStart, text.value.length, 'the cursor after the quote');
assert.equal(inputs, 1, 'the field hears of the change (its height, the draft)');
assert.equal(d.querySelector('[data-id="$1"] details').open, false, 'the menu is closed');

d.querySelector('[data-id="$2"] [data-act]').click();
await tick();
assert.ok(text.value.endsWith(`[quote]*Борис*, ${at}\nакт.pdf[/quote]\n`), 'a file is quoted by its name');
assert.deepEqual(posts.filter(p => !['/im/read', '/im/draft', '/im/typing'].includes(p.u)), [], 'a quote sends nothing');
posts.length = 0;

d.querySelector('[data-id="$3"] [data-act]').click();
await tick();
assert.deepEqual(posts.filter(p => p.u === '/im/file/drive'), [{ u: '/im/file/drive', body: 'room=%21r&event=%243' }], 'the file is saved to the drive');
const toast = d.querySelector('.toasts .im-toast');
assert.equal(toast?.textContent, 'Сохранено');
assert.equal(toast.getAttribute('href'), '/disk/my?folder=f1', 'the toast leads to the folder');

// the author's name: «@Name » at the caret, with a space before it when glued to a word
text.value = 'Привет,спасибо';
text.setSelectionRange(7, 7);
d.querySelector('[data-id="$4"] .who').click();
await tick();
assert.equal(text.value, 'Привет, @Анна Петрова спасибо', 'the mention goes where the caret is');
assert.equal(text.selectionStart, 'Привет, @Анна Петрова '.length, 'the caret after the mention');
assert.equal(d.activeElement, text);
console.log('imquote: ok');
