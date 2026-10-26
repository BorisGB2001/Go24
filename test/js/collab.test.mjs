// The messenger's «Коллабы» tab in jsdom: the tab and the search box filter the chat list together; the tab shows
// the unread messages of the collabs' chats. node test/js/collab.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const im = readFileSync(new URL('../../internal/web/static/js/im.js', import.meta.url), 'utf8');
const tick = () => new Promise(r => setTimeout(r, 10));
const streams = [];

const room = (id, kind, n) => `<a class="im-room" data-room-id="${id}" data-name="${id}" data-kind="${kind}"><span data-preview></span><span data-unread ${n ? '' : 'hidden'}>${n}</span></a>`;
const page = (tab) => {
  const dom = new JSDOM(`<!doctype html><body><div data-im data-room="" data-tab="${tab}" data-failed="x">
    <div class="tabs"><a href="/im" data-im-tab="all">Все</a><a href="/im?tab=collab" data-im-tab="collab">Коллабы <span data-tab-unread hidden></span></a></div>
    <input type="search" data-filter><p data-tab-empty="collab" hidden>нет</p>
    <div data-list>${room('Проект', 'project', 2)}${room('Клиент Альфа', 'collab', 3)}${room('Клиент Бета', 'collab', 0)}${room('Анна', 'dm', 1)}</div>
    <div data-thread-pane hidden><div data-thread-msgs></div></div></div></body>`, { runScripts: 'outside-only', url: 'http://go24.test/im', pretendToBeVisual: true });
  const w = dom.window;
  w.EventSource = class {
    constructor(u) { this.u = u; this.h = {}; streams.push(this); }
    addEventListener(t, f) { this.h[t] = f; }
    close() {}
  };
  w.fetch = async () => ({ ok: true, json: async () => ({ html: '' }) });
  w.CSS = { escape: s => s };
  w.eval(im);
  return w;
};
const shown = (w) => [...w.document.querySelectorAll('.im-room')].filter(a => !a.hidden).map(a => a.dataset.roomId);
const tabCount = (w) => w.document.querySelector('[data-tab-unread]');

// all chats at first; the collab tab counts the unread messages of the collabs' chats only
let w = page('all');
await tick();
assert.deepEqual(shown(w), ['Проект', 'Клиент Альфа', 'Клиент Бета', 'Анна']);
assert.equal(tabCount(w).hidden, false);
assert.equal(tabCount(w).textContent, '3', 'only the collabs are counted');
assert.ok(w.document.querySelector('[data-im-tab="all"]').classList.contains('on'));

// the «Коллабы» tab: collabs only, and the search works inside it
w.document.querySelector('[data-im-tab="collab"]').click();
assert.deepEqual(shown(w), ['Клиент Альфа', 'Клиент Бета']);
assert.ok(w.document.querySelector('[data-im-tab="collab"]').classList.contains('on'));
assert.equal(w.document.querySelector('[data-tab-empty]').hidden, true);
const q = w.document.querySelector('[data-filter]');
q.value = 'бета';
q.dispatchEvent(new w.Event('input'));
assert.deepEqual(shown(w), ['Клиент Бета']);
w.document.querySelector('[data-im-tab="all"]').click();
assert.deepEqual(shown(w), ['Клиент Бета'], 'the search stays when the tab changes');
q.value = '';
q.dispatchEvent(new w.Event('input'));
assert.deepEqual(shown(w), ['Проект', 'Клиент Альфа', 'Клиент Бета', 'Анна']);

// the unread counts from the server move the tab's counter; a new message in a collab's chat too
const live = streams.find(s => s.u === '/im/live');
live.h.unread({ data: JSON.stringify({ rooms: { 'Клиент Альфа': 0, 'Проект': 5 } }) });
assert.equal(tabCount(w).hidden, true, 'no unread collab messages');
live.h.msg({ data: JSON.stringify({ id: '$1', room: 'Клиент Бета', name: 'Гость', preview: 'привет', html: '' }) });
assert.equal(tabCount(w).textContent, '1');

// a page opened on the collab tab (/im?tab=collab, or a collab's chat open) starts filtered
w = page('collab');
await tick();
assert.deepEqual(shown(w), ['Клиент Альфа', 'Клиент Бета']);

// no collabs at all: the tab says so
const empty = new JSDOM(`<!doctype html><body><div data-im data-room="" data-tab="collab" data-failed="x">
  <a data-im-tab="all">Все</a><a data-im-tab="collab">Коллабы</a><p data-tab-empty="collab" hidden>нет</p>
  <div data-list>${room('Проект', 'project', 0)}</div><div data-thread-pane hidden><div data-thread-msgs></div></div></div></body>`,
  { runScripts: 'outside-only', url: 'http://go24.test/im', pretendToBeVisual: true });
empty.window.EventSource = class { addEventListener() {} };
empty.window.CSS = { escape: s => s };
empty.window.eval(im);
assert.equal(empty.window.document.querySelector('[data-tab-empty]').hidden, false);
assert.equal(empty.window.document.querySelector('.im-room').hidden, true);

console.log('collab.test.mjs: ok');
