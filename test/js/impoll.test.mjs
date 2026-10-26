// Polls in the messenger (static/js/im.js) in jsdom: «Опрос» opens the form, «Добавить вариант» adds answers (10 at
// most), the poll goes to the chat; a click on an answer votes (one answer: a new choice; several: on and off),
// «Отменить голос» and «Завершить опрос» (after a confirmation); the poll comes back drawn anew.
// node test/js/impoll.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const im = readFileSync(new URL('../../internal/web/static/js/im.js', import.meta.url), 'utf8');
const tick = () => new Promise(r => setTimeout(r, 20));
const poll = (id, multi, mine = []) => `<div class="msg" data-id="${id}"><div class="bubble"><div class="im-poll" data-poll data-multi="${multi}">
  ${['a1', 'a2', 'a3'].map(a => `<button data-poll-answer="${a}" data-mine="${mine.includes(a)}">${a}</button>`).join('')}
  <button data-poll-retract>x</button><button data-poll-end>end</button></div></div></div>`;
const dom = new JSDOM(`<!doctype html><body><div class="im" data-im data-room="!r" data-failed="Не удалось" data-poll-end-confirm="Завершить?">
  <nav data-list><a class="im-room" data-room-id="!r" data-name="Мы"><span data-preview></span><span data-unread hidden></span></a></nav>
  <section class="im-chat"><div class="im-msgs" data-msgs></div>
    <dialog data-poll-dlg><form data-poll-form><input name="question"><div data-poll-answers><input name="answer"><input name="answer"></div>
      <button type="button" data-poll-add data-max="4">+</button><input type="checkbox" name="multi" value="1"><input type="checkbox" name="hidden" value="1">
      <button>Создать</button><button type="button" data-poll-cancel>Отмена</button></form></dialog>
    <form class="chatform" data-send><button type="button" data-poll-new>📊</button><textarea name="body"></textarea></form></section>
  <aside data-thread-pane hidden><div data-thread-msgs></div><form data-send-thread><textarea name="body"></textarea></form></aside>
  </div></body>`, { runScripts: 'outside-only', url: 'http://go24.test/im?room=!r', pretendToBeVisual: true });
const w = dom.window, d = w.document;
w.CSS = { escape: s => s };
w.HTMLElement.prototype.scrollIntoView = () => {};
const posts = [];
let confirmed = false;
w.confirm = () => confirmed;
w.fetch = async (url, opts = {}) => {
  if (url.startsWith('/im/messages')) return { ok: true, json: async () => ({ html: poll('$one', false, ['a1']) + poll('$many', true, ['a2']) }) };
  const p = new URLSearchParams(String(opts.body));
  posts.push([url, Object.fromEntries(p), p.getAll('answer')]);
  if (url === '/im/poll') return { ok: true, json: async () => ({ id: '$new', html: poll('$new', false) }) };
  return { ok: true, json: async () => ({ id: p.get('event'), html: poll(p.get('event'), true, p.getAll('answer')).replace('im-poll', 'im-poll redrawn') }) };
};
w.EventSource = class { addEventListener() {} close() {} };
w.eval(im);
await tick();
const $ = s => d.querySelector(s);
const answers = () => d.querySelectorAll('[data-poll-answers] [name="answer"]');

// the form: answers added up to the most, a new poll goes to the chat
$('[data-poll-new]').click();
assert.ok($('[data-poll-dlg]').open);
for (let i = 0; i < 4; i++) $('[data-poll-add]').click();
assert.equal(answers().length, 4);
$('[name="question"]').value = 'Когда?';
answers()[0].value = 'Пт'; answers()[1].value = 'Пн'; answers()[2].value = 'Вт';
$('[name="multi"]').checked = true;
$('[data-poll-form]').requestSubmit();
await tick();
assert.deepEqual(posts.at(-1).slice(0, 1), ['/im/poll']);
assert.equal(posts.at(-1)[1].question, 'Когда?');
assert.equal(posts.at(-1)[1].multi, '1');
assert.equal(posts.at(-1)[1].room, '!r');
assert.deepEqual(posts.at(-1)[2], ['Пт', 'Пн', 'Вт', '']);
assert.ok($('[data-msgs] [data-id="$new"]'));
assert.ok(!$('[data-poll-dlg]').open);
// opened again: two empty answers
$('[data-poll-new]').click();
assert.equal(answers().length, 2);
assert.equal(answers()[0].value, '');
$('[data-poll-cancel]').click();

// one answer: a new choice votes, the one chosen already does nothing
$('[data-id="$one"] [data-poll-answer="a1"]').click();
await tick();
assert.notEqual(posts.at(-1)[0], '/im/poll/vote');
$('[data-id="$one"] [data-poll-answer="a3"]').click();
await tick();
assert.deepEqual(posts.at(-1), ['/im/poll/vote', { room: '!r', event: '$one', answer: 'a3' }, ['a3']]);
assert.ok($('[data-id="$one"] .im-poll.redrawn'));
// several: on and off
$('[data-id="$many"] [data-poll-answer="a3"]').click();
await tick();
assert.deepEqual(posts.at(-1)[2], ['a2', 'a3']);
$('[data-id="$many"] [data-poll-answer="a2"]').click();
await tick();
assert.deepEqual(posts.at(-1)[2], ['a3']);
// «Отменить голос»; «Завершить опрос» only when confirmed
$('[data-id="$many"] [data-poll-retract]').click();
await tick();
assert.deepEqual(posts.at(-1).slice(0, 1).concat([posts.at(-1)[2]]), ['/im/poll/vote', []]);
const n = posts.length;
$('[data-id="$many"] [data-poll-end]').click();
await tick();
assert.equal(posts.length, n, 'not confirmed');
confirmed = true;
$('[data-id="$many"] [data-poll-end]').click();
await tick();
assert.deepEqual(posts.at(-1)[0], '/im/poll/end');
assert.equal(posts.at(-1)[1].event, '$many');
console.log('im.js: polls passed');
