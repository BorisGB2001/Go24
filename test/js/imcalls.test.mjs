// The messenger's «Каналы» and «Звонки» tabs in jsdom: «Каналы» shows only the channels (or says there are none);
// «Звонки» loads the call history in place of the chats, all or only the missed ones, the search filters it too, and
// the tab's counter of missed calls goes once they are looked at. node test/js/imcalls.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const im = readFileSync(new URL('../../internal/web/static/js/im.js', import.meta.url), 'utf8');
const tick = () => new Promise(r => setTimeout(r, 10));

const room = (id, kind) => `<div data-row><a class="im-room" data-room-id="${id}" data-name="${id}" data-kind="${kind}"><span data-preview></span><span data-unread hidden>0</span></a></div>`;
const call = (name, kind) => `<div class="im-row im-call k-${kind}" data-call data-name="${name}"><a class="im-room" href="#">${name}</a></div>`;
const page = (tab, rooms) => {
  const dom = new JSDOM(`<!doctype html><body><div data-im data-room="" data-tab="${tab}" data-failed="x">
    <div class="tabs"><a href="/im" data-im-tab="all">Все</a><a href="/im?tab=channel" data-im-tab="channel"><span data-tab-unread hidden></span></a>
      <a href="/im?tab=calls" data-im-tab="calls"><span data-calls-unseen>2</span></a></div>
    <input type="search" data-filter><p data-tab-empty="channel" hidden>нет каналов</p><div data-calls hidden></div>
    <div data-list>${rooms}</div>
    <div data-thread-pane hidden><div data-thread-msgs></div></div></div></body>`, { runScripts: 'outside-only', url: 'http://go24.test/im', pretendToBeVisual: true });
  const w = dom.window;
  w.EventSource = class { addEventListener() {} close() {} };
  w.asked = [];
  w.fetch = async (u) => {
    w.asked.push(String(u));
    const missed = String(u).includes('missed=1');
    const html = '<div class="tabs"><a href="#" data-calls-filter="">Все</a><a href="#" data-calls-filter="1">Пропущенные</a></div>' +
      (missed ? call('Ева', 'missed') : call('Анна', 'in') + call('Ева', 'missed') + call('Отдел', 'out'));
    return { ok: true, json: async () => ({ html }) };
  };
  w.CSS = { escape: s => s };
  w.eval(im);
  return w;
};
const shown = (w) => [...w.document.querySelectorAll('[data-list] .im-room')].filter(a => !a.closest('[data-row]').hidden).map(a => a.dataset.roomId);
const calls = (w) => [...w.document.querySelectorAll('[data-call]')].filter(r => !r.hidden).map(r => r.dataset.name);

// «Каналы»: only the channels
let w = page('all', room('Новости', 'channel') + room('Анна', 'dm') + room('Отдел', 'group'));
await tick();
assert.deepEqual(shown(w), ['Новости', 'Анна', 'Отдел']);
w.document.querySelector('[data-im-tab="channel"]').click();
assert.deepEqual(shown(w), ['Новости']);
assert.equal(w.document.querySelector('[data-tab-empty="channel"]').hidden, true);
assert.equal(w.document.querySelector('[data-calls]').hidden, true);

// «Звонки»: the history in place of the chats; the counter goes
w.document.querySelector('[data-im-tab="calls"]').click();
await tick();
assert.deepEqual(w.asked, ['/im/calls']);
assert.deepEqual(shown(w), [], 'no chats on the calls tab');
assert.equal(w.document.querySelector('[data-calls]').hidden, false);
assert.deepEqual(calls(w), ['Анна', 'Ева', 'Отдел']);
assert.equal(w.document.querySelector('[data-calls-unseen]').hidden, true);
const q = w.document.querySelector('[data-filter]');
q.value = 'ев';
q.dispatchEvent(new w.Event('input'));
assert.deepEqual(calls(w), ['Ева'], 'the search filters the calls');
q.value = '';
q.dispatchEvent(new w.Event('input'));
w.document.querySelector('[data-calls-filter="1"]').click();
await tick();
assert.equal(w.asked.at(-1), '/im/calls?missed=1');
assert.deepEqual(calls(w), ['Ева']);

// back to all the chats
w.document.querySelector('[data-im-tab="all"]').click();
assert.equal(w.document.querySelector('[data-calls]').hidden, true);
assert.deepEqual(shown(w), ['Новости', 'Анна', 'Отдел']);

// no channels: the tab says so; a page opened on «Звонки» (/im?tab=calls) loads them at once
w = page('channel', room('Анна', 'dm'));
await tick();
assert.equal(w.document.querySelector('[data-tab-empty="channel"]').hidden, false);
w = page('calls', room('Анна', 'dm'));
await tick();
assert.deepEqual(w.asked, ['/im/calls']);
assert.deepEqual(calls(w), ['Анна', 'Ева', 'Отдел']);

console.log('imcalls.test.mjs: ok');
