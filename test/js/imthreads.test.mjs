// Unread replies of threads in jsdom (static/js/im.js): a live reply counts under its root («новых») and beside the
// chat; Matrix's counts («unread») set the chat's and «Треды»'s badges; «Треды» opens the list, a thread opened from it
// is read; a reply in the open thread is read at once. node test/js/imthreads.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const im = readFileSync(new URL('../../internal/web/static/js/im.js', import.meta.url), 'utf8');
const tick = () => new Promise(r => setTimeout(r, 20));
const root = `<div class="msg" data-id="$1" data-ts="1"><div class="bubble"><div class="text">вопрос</div>
  <button type="button" class="link small im-thlink new" data-open-thread="$1" data-replies="2" data-new="1"><span data-replies-text>Ответов: 2</span><span class="im-thnew" data-new-text>новых: 1</span></button></div></div>`;
const row = (id, on) => `<div class="im-row" data-row><a href="/im?room=${id}" class="im-room${on ? ' on' : ''}" data-room-id="${id}" data-kind="dm"><span data-preview></span>
  <span class="cnt at" data-mention="0" hidden>@</span><span class="cnt" data-unread hidden>0</span><span class="cnt th" data-threads="0" hidden><i>0</i></span></a></div>`;
const dom = new JSDOM(`<!doctype html><html lang="ru-RU"><body><div class="im" data-im data-room="!r" data-failed="x" data-threads-title="Треды"
  data-replies-fmt="Ответов: %s" data-replies-new-fmt="новых: %s">
  <nav data-list>${row('!r', true)}${row('!o')}</nav><section class="im-chat"><div class="im-head"><button type="button" data-threads-open><span class="cnt" data-threads-cnt hidden>0</span></button></div>
  <div class="im-msgs" data-msgs>${root}</div><form class="chatform" data-send><textarea name="body"></textarea><button>s</button></form></section>
  <aside data-search-pane hidden><b data-search-title></b><form data-search><input name="q"></form><div data-hits></div></aside>
  <aside data-thread-pane hidden><div data-thread-msgs></div></aside></div></body></html>`,
{ runScripts: 'outside-only', url: 'http://go24.test/im?room=!r', pretendToBeVisual: true });
const w = dom.window;
w.CSS = { escape: s => s };
w.HTMLElement.prototype.scrollIntoView = () => {};
const posts = [], gets = [];
w.fetch = async (u, opts = {}) => {
  if (opts.method === 'POST') posts.push({ u, body: String(opts.body) }); else gets.push(u);
  let data = { html: '' };
  if (u.startsWith('/im/messages?') && !u.includes('thread=')) data = { html: root };
  if (u.startsWith('/im/threads')) data = { html: '<a href="#" class="im-hit im-thitem new" data-open-thread="$1">вопрос</a>' };
  return { ok: true, json: async () => data };
};
const es = {};
w.EventSource = class { addEventListener(type, fn) { es[type] = fn; } close() {} };
w.eval(im);
await tick();
const d = w.document, $ = s => d.querySelector(s);
const live = (type, data) => es[type]({ data: JSON.stringify(data) });
const link = () => $('[data-msgs] [data-open-thread="$1"]');
const badge = id => $(`.im-room[data-room-id="${id}"] [data-threads]`);

// a reply in a thread of the open chat, the thread not open: one more reply, one more new
posts.length = 0;
live('msg', { id: '$2', room: '!r', thread: '$1', mine: false, preview: 'да', name: 'Боб', html: '<div class="msg" data-id="$2"></div>' });
assert.equal(link().dataset.replies, '3');
assert.equal(link().querySelector('[data-replies-text]').textContent, 'Ответов: 3');
assert.equal(link().querySelector('[data-new-text]').textContent, 'новых: 2');
assert.ok(link().classList.contains('new'));
assert.equal(badge('!r').hidden, false, 'the chat shows its threads have news');
assert.equal($('[data-threads-cnt]').textContent, '1', '«Треды» of the open chat counts them');
assert.deepEqual(posts.filter(p => p.u === '/im/read'), [], 'a thread not open is not read');
assert.equal($('.im-room[data-room-id="!r"] [data-unread]').hidden, true, 'the chat\'s own count stays');

// in another chat: its threads' badge, not its own count
live('msg', { id: '$9', room: '!o', thread: '$8', mine: false, preview: 'x', name: 'Ева', html: '' });
assert.equal(badge('!o').hidden, false);
assert.equal(badge('!o').querySelector('i').textContent, '1');
assert.equal($('.im-room[data-room-id="!o"] [data-unread]').hidden, true);

// Matrix's counts
live('unread', { rooms: { '!r': 0, '!o': 0 }, threads: { '!r': 4 }, mentions: {} });
assert.equal(badge('!r').querySelector('i').textContent, '4');
assert.equal($('[data-threads-cnt]').textContent, '4');
assert.equal(badge('!o').hidden, true, 'read elsewhere: the badge goes');

// «Треды»: the list in the side pane; a thread opened from it — its root has nothing new
$('[data-threads-open]').click();
await tick();
assert.ok(gets.includes('/im/threads?room=!r'));
assert.equal($('[data-search-pane]').hidden, false);
assert.equal($('[data-search-title]').textContent, 'Треды');
$('[data-hits] [data-open-thread]').click();
await tick();
assert.ok(gets.some(u => u.startsWith('/im/messages?') && u.includes('thread=%241')), 'the thread is loaded (and so read)');
assert.equal($('[data-thread-pane]').hidden, false);
assert.equal(link().dataset.new, '0');
assert.equal(link().querySelector('[data-new-text]').hidden, true);
assert.ok(!link().classList.contains('new'));

// a reply in the open thread: read at once, not new
posts.length = 0;
live('msg', { id: '$3', room: '!r', thread: '$1', mine: false, preview: 'ещё', name: 'Боб', html: '<div class="msg" data-id="$3"></div>' });
await tick();
assert.deepEqual(posts.filter(p => p.u === '/im/read').map(p => p.body), ['room=%21r&thread=%241']);
assert.equal(link().dataset.replies, '4');
assert.equal(link().dataset.new, '0');
console.log('imthreads: ok');
