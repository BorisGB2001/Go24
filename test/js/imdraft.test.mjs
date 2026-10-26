// Drafts in the messenger (static/js/im.js) in jsdom: the field starts with the chat's draft, typing keeps it on the
// server a moment later, editing a message does not touch it and gives it back, sending clears it, leaving the page
// keeps it at once; each thread keeps its own in the thread's field; a live message does not hide another chat's draft in the list. node test/js/imdraft.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const im = readFileSync(new URL('../../internal/web/static/js/im.js', import.meta.url), 'utf8');
const wait = ms => new Promise(r => setTimeout(r, ms));
const msg = (id, text) => `<div class="msg" data-id="${id}"><div class="bubble"><details class="im-menu"><summary>…</summary>
  <div class="pop"><button data-act="edit">e</button></div></details><div class="text">${text}</div></div></div>`;
const dom = new JSDOM(`<!doctype html><body><div class="im" data-im data-room="!r" data-failed="Не удалось" data-edit-hint="Изменение">
  <nav data-list><a class="im-room" data-room-id="!r" data-name="Мы"><span data-preview></span><span data-unread hidden></span></a>
    <a class="im-room" data-room-id="!other" data-name="Другой"><span data-preview data-draft>Черновик: потом</span><span data-unread hidden></span></a></nav>
  <section class="im-chat"><div class="im-msgs" data-msgs></div>
    <div data-replying hidden><span data-replying-text></span><button data-reply-cancel>x</button></div>
    <form class="chatform" data-send><textarea name="body" data-draft="начал писать"></textarea><button>s</button></form></section>
  <aside data-thread-pane hidden><div data-thread-msgs></div><form data-send-thread><textarea name="body"></textarea></form></aside>
  </div></body>`, { runScripts: 'outside-only', url: 'http://go24.test/im?room=!r', pretendToBeVisual: true });
const w = dom.window, d = w.document;
w.CSS = { escape: s => s };
w.HTMLElement.prototype.scrollIntoView = () => {};
const posts = [], beacons = [];
w.fetch = async (url, opts = {}) => {
  if (url.startsWith('/im/messages')) {
    const t = new URL(url, 'http://go24.test').searchParams.get('thread');
    return { ok: true, json: async () => ({ html: msg('$b', 'моё'), draft: t === '$t1' ? 'в тред 1' : '' }) };
  }
  const p = Object.fromEntries(new URLSearchParams(String(opts.body)));
  posts.push([url, p]);
  if (url === '/im/send') return { ok: true, json: async () => ({ id: '$n', html: msg('$n', p.body) }) };
  if (url === '/im/edit') return { ok: true, json: async () => ({ id: p.event, html: msg(p.event, p.body) }) };
  return { ok: true, json: async () => ({}) };
};
w.navigator.sendBeacon = (url, body) => { beacons.push([url, Object.fromEntries(body)]); return true; };
const streams = [];
w.EventSource = class { constructor() { this.h = {}; streams.push(this); } addEventListener(t, f) { this.h[t] = f; } close() {} };
w.eval(im);
await wait(20);
const $ = s => d.querySelector(s);
const text = $('[data-send] textarea');
const drafts = () => posts.filter(p => p[0] === '/im/draft').map(p => p[1].text);

// the field starts with the draft; typing keeps it a moment later, not at each key
assert.equal(text.value, 'начал писать');
text.value = 'начал писать письмо';
text.dispatchEvent(new w.Event('input'));
text.value = 'начал писать письмо Ане';
text.dispatchEvent(new w.Event('input'));
assert.deepEqual(drafts(), []);
await wait(1100);
assert.deepEqual(drafts(), ['начал писать письмо Ане']);

// editing a message: its text in the field, not a draft; the draft comes back after it
$('[data-id="$b"] [data-act="edit"]').click();
assert.equal(text.value, 'моё');
text.value = 'моё, исправлено';
text.dispatchEvent(new w.Event('input'));
$('[data-send]').requestSubmit();
await wait(1100);
assert.equal(text.value, 'начал писать письмо Ане');
assert.deepEqual(drafts(), ['начал писать письмо Ане']);

// sending clears it
$('[data-send]').requestSubmit();
await wait(20);
assert.deepEqual(drafts(), ['начал писать письмо Ане', '']);
assert.equal(text.value, '');

// leaving the page keeps what is typed at once
text.value = 'на потом';
text.dispatchEvent(new w.Event('input'));
w.dispatchEvent(new w.Event('pagehide'));
assert.deepEqual(beacons, [['/im/draft', { room: '!r', text: 'на потом' }]]);

// a live message of another chat: its draft stays in the list
streams[0].h.msg({ data: JSON.stringify({ room: '!other', id: '$x', name: 'Боб', preview: 'привет', html: '' }) });
assert.equal($('[data-room-id="!other"] [data-preview]').textContent, 'Черновик: потом');

// a thread: its field holds that thread's draft; leaving the thread keeps what is typed, under that thread
const ttext = $('[data-send-thread] textarea');
$('[data-im]').insertAdjacentHTML('beforeend', '<button data-open-thread="$t1" id="ot1"></button><button data-open-thread="$t2" id="ot2"></button>');
d.getElementById('ot1').click();
await wait(20);
assert.equal(ttext.value, 'в тред 1');
ttext.value = 'в тред 1, дописал';
ttext.dispatchEvent(new w.Event('input'));
d.getElementById('ot2').click();
await wait(20);
assert.equal(ttext.value, '');
const tdrafts = () => posts.filter(p => p[0] === '/im/draft' && p[1].thread).map(p => [p[1].thread, p[1].text]);
assert.deepEqual(tdrafts(), [['$t1', 'в тред 1, дописал']]);
assert.equal(text.value, 'на потом'); // the chat's own field is not touched
// sending in the thread clears that thread's draft
ttext.value = 'ответ';
ttext.dispatchEvent(new w.Event('input'));
await wait(1100);
$('[data-send-thread]').requestSubmit();
await wait(20);
assert.deepEqual(tdrafts(), [['$t1', 'в тред 1, дописал'], ['$t2', 'ответ'], ['$t2', '']]);
assert.equal(ttext.value, '');
console.log('imdraft: ok');
