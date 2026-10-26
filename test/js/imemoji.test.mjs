// Smileys beside the field of the messenger (static/js/im.js) in jsdom: ☺ opens the choice by groups, a click puts
// the smiley where the cursor is and it comes first among «Недавние»; ☺ again, Escape or a click elsewhere closes it;
// the thread's field has its own. node test/js/imemoji.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const im = readFileSync(new URL('../../internal/web/static/js/im.js', import.meta.url), 'utf8');
const tick = () => new Promise(r => setTimeout(r, 20));
const dom = new JSDOM(`<!doctype html><body><div class="im" data-im data-room="!r" data-failed="Не удалось">
  <nav data-list><a class="im-room" data-room-id="!r" data-name="Мы"><span data-preview></span><span data-unread hidden></span></a></nav>
  <section class="im-chat"><div class="im-msgs" data-msgs></div>
    <div data-emoji-pop hidden data-recent="Недавние" data-smileys="Смайлы" data-gestures="Жесты" data-hearts="Сердца" data-objects="Разное"></div>
    <form class="chatform" data-send><button type="button" data-emoji>☺</button><textarea name="body"></textarea></form></section>
  <aside data-thread-pane hidden><div data-thread-msgs></div><form data-send-thread><button type="button" data-emoji>☺</button><textarea name="body"></textarea></form></aside>
  </div></body>`, { runScripts: 'outside-only', url: 'http://go24.test/im?room=!r', pretendToBeVisual: true });
const w = dom.window, d = w.document;
w.CSS = { escape: s => s };
w.HTMLElement.prototype.scrollIntoView = () => {};
w.fetch = async () => ({ ok: true, json: async () => ({ html: '' }) });
w.EventSource = class { addEventListener() {} close() {} };
w.eval(im);
await tick();
const $ = s => d.querySelector(s);
const pop = $('[data-emoji-pop]');
const text = $('[data-send] textarea');
const pick = e => [...pop.querySelectorAll("[data-emoji-pick]")].find(b => b.dataset.emojiPick === e).click();

// the choice by groups, no «Недавние» yet
$('[data-send] [data-emoji]').click();
assert.equal(pop.hidden, false);
assert.equal(pop.parentElement, $('[data-send]'));
assert.deepEqual([...pop.querySelectorAll('b')].map(b => b.textContent), ['Смайлы', 'Жесты', 'Сердца', 'Разное']);
// at the cursor; the field hears it (typing, the height)
let typed = 0;
text.addEventListener('input', () => typed++);
text.value = 'привет мир';
text.selectionStart = text.selectionEnd = 6;
pick('😀');
assert.equal(text.value, 'привет😀 мир');
assert.equal(text.selectionStart, 6 + '😀'.length);
assert.equal(typed, 1);
pick('👍');
assert.equal(text.value, 'привет😀👍 мир');
// ☺ again closes; opened anew, the recent ones come first
$('[data-send] [data-emoji]').click();
assert.equal(pop.hidden, true);
$('[data-send] [data-emoji]').click();
assert.equal(pop.querySelector('b').textContent, 'Недавние');
assert.deepEqual([...pop.querySelector('.im-emoji-row').children].map(b => b.textContent), ['👍', '😀']);
// Escape, a click elsewhere
d.querySelector('.im').dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
assert.equal(pop.hidden, true);
$('[data-send] [data-emoji]').click();
$('[data-msgs]').click();
assert.equal(pop.hidden, true);
// the thread's field
$('[data-send-thread] [data-emoji]').click();
assert.equal(pop.parentElement, $('[data-send-thread]'));
pick('🎉');
assert.equal($('[data-send-thread] textarea').value, '🎉');
assert.equal(text.value, 'привет😀👍 мир');
console.log('im.js: smileys passed');
