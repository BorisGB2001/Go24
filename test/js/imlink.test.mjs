// The card of a link under a message (static/js/im.js) in jsdom: the first outside link of a message
// gets the card from /im/link (once per link), live messages and edits too; links inside go24, in code and pages
// without a card have none; the texts of the page never become HTML.
// node test/js/imlink.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const im = readFileSync(new URL('../../internal/web/static/js/im.js', import.meta.url), 'utf8');
const tick = () => new Promise(r => setTimeout(r, 20));
const msg = (id, text) => `<div class="msg" data-id="${id}"><div class="bubble"><div class="text">${text}</div><span class="time">9:00</span></div></div>`;
const dom = new JSDOM(`<!doctype html><body><div class="im" data-im data-room="!r" data-failed="Не удалось">
  <nav data-list></nav>
  <section class="im-chat"><div class="im-msgs" data-msgs></div>
  <button data-latest hidden></button><form class="chatform" data-send><textarea name="body"></textarea></form></section>
  <aside data-thread-pane hidden><div data-thread-msgs></div><form data-send-thread><textarea name="body"></textarea></form></aside>
  </div></body>`, { runScripts: 'outside-only', url: 'http://go24.test/im?room=!r', pretendToBeVisual: true });
const w = dom.window, d = w.document;
w.CSS = { escape: s => s };
w.HTMLElement.prototype.scrollIntoView = () => {};
const asked = [];
w.fetch = async (url) => {
  if (url.startsWith('/im/link?')) {
    const u = new URLSearchParams(url.split('?')[1]).get('url');
    asked.push(u);
    if (u === 'https://news.example/a') return { ok: true, json: async () => ({ url: u, site: 'news.example', title: 'Заголовок <b>x</b>', text: 'Описание', image: '/im/link/image?url=p' }) };
    if (u === 'https://c.example/') return { ok: true, json: async () => ({ url: u, site: 'c.example', title: 'Си' }) };
    return { ok: true, status: 204, json: async () => { throw new Error('no body'); } };
  }
  if (url.startsWith('/im/messages')) return { ok: true, json: async () => ({ html: msg('$1', 'см. <a href="https://news.example/a">статью</a> и <a href="https://b.example/">ещё</a>') }) };
  return { ok: true, status: 204, json: async () => ({}) };
};
const streams = [];
w.EventSource = class { constructor() { this.h = {}; streams.push(this); } addEventListener(t, f) { this.h[t] = f; } close() {} };
w.eval(im);
await tick();
const $ = s => d.querySelector(s);
const card = id => $(`[data-id="${id}"] .im-link`);

// the messages of the chat: the card of its first link, after the text; the title stays text
let c = card('$1');
assert.ok(c, 'no card');
assert.equal(c.getAttribute('href'), 'https://news.example/a');
assert.equal(c.target, '_blank');
assert.match(c.rel, /noopener/);
assert.equal(c.querySelector('img').getAttribute('src'), '/im/link/image?url=p');
assert.equal(c.querySelector('.title').textContent, 'Заголовок <b>x</b>');
assert.equal(c.querySelector('.title b'), null);
assert.equal(c.querySelector('.site').textContent, 'news.example');
assert.equal(c.querySelector('.desc').textContent, 'Описание');
assert.equal(c.previousElementSibling.className, 'text');
assert.deepEqual(asked, ['https://news.example/a']);

// live: the same link is asked once; inside go24, in code and without a card — nothing
const send = (id, text) => streams[0].h.msg({ data: JSON.stringify({ room: '!r', id, name: 'Анна', preview: '', html: msg(id, text) }) });
send('$2', '<a href="https://news.example/a">снова</a>');
send('$3', '<a href="http://go24.test/tasks/GO24DEV-1">задача</a>');
send('$4', '<code><a href="https://code.example/">x</a></code>');
send('$5', '<a href="https://none.example/">нет</a>');
send('$6', 'без ссылок');
await tick();
assert.ok(card('$2'));
assert.equal(card('$3'), null);
assert.equal(card('$4'), null);
assert.equal(card('$5'), null);
assert.equal(card('$6'), null);
assert.deepEqual(asked, ['https://news.example/a', 'https://none.example/']);

// an edit: the card of the new link takes the old one's place; a card without a picture or text
streams[0].h.change({ data: JSON.stringify({ room: '!r', id: '$1', change: 'edit', body: 'https://c.example/', text: '<a href="https://c.example/">c</a>' }) });
await tick();
c = card('$1');
assert.equal(c.getAttribute('href'), 'https://c.example/');
assert.equal(d.querySelectorAll('[data-id="$1"] .im-link').length, 1);
assert.equal(c.querySelector('img'), null);
assert.equal(c.querySelector('.desc'), null);
console.log('imlink: ok');
