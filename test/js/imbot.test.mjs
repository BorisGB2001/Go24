// Chat bots in the messenger (static/js/im.js) in jsdom: «/» at the start of the field lists the
// commands of the chat's bots (↓ and Enter put one into the field), the buttons under a bot's message — a command
// goes to the bot (a blocking one takes the buttons off), PUT and SEND put a text into the field; an item of the
// bot's in the message's menu runs its command as MENU.
// node test/js/imbot.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const im = readFileSync(new URL('../../internal/web/static/js/im.js', import.meta.url), 'utf8');
const tick = () => new Promise(r => setTimeout(r, 20));
const bot = `<div class="msg" data-id="$b"><div class="bubble"><details class="im-menu" open><summary>…</summary><div class="pop">
  <button type="button" class="im-botmenu" data-kbd="command" data-command="weather" data-params="неделя">Прогноз</button></div></details><div class="text" data-body="Погода">Погода</div>
  <div class="im-kbd"><div class="im-kbd-row"><button type="button" data-kbd="command" data-command="weather" data-params="Москва" data-block="1">Ещё</button></div>
  <div class="im-kbd-row"><button type="button" data-kbd="PUT" data-value="/weather ">Город</button><button type="button" data-kbd="SEND" data-value="Спасибо">Спасибо</button></div></div></div></div>`;
const dom = new JSDOM(`<!doctype html><body><div class="im" data-im data-room="!r" data-failed="Не удалось">
  <section class="im-chat"><div class="im-msgs" data-msgs></div>
    <form class="chatform" data-send><textarea name="body"></textarea><button>s</button></form></section>
  <aside data-thread-pane hidden><div data-thread-msgs></div></aside>
  </div></body>`, { runScripts: 'outside-only', url: 'http://go24.test/im?room=!r', pretendToBeVisual: true });
const w = dom.window, d = w.document;
w.CSS = { escape: s => s };
w.HTMLElement.prototype.scrollIntoView = () => {};
const posts = [], gets = [];
w.fetch = async (url, opts = {}) => {
  if (url.startsWith('/im/messages')) return { ok: true, json: async () => ({ html: bot }) };
  if (url.startsWith('/im/commands')) {
    gets.push(url);
    return { ok: true, json: async () => [{ command: 'weather', title: 'Погода', params: 'город', bot: 'Помощник' }, { command: 'help', title: 'Справка', params: '', bot: 'Помощник' }] };
  }
  if (opts.method === 'POST') posts.push([url, opts.body instanceof w.FormData ? Object.fromEntries(opts.body) : Object.fromEntries(new URLSearchParams(String(opts.body)))]);
  if (url === '/im/send') return { ok: true, json: async () => ({ id: '$n', html: '<div class="msg mine" data-id="$n"></div>' }) };
  return { ok: true, status: 204, json: async () => ({}) };
};
w.EventSource = class { constructor() { this.h = {}; } addEventListener(t, f) { this.h[t] = f; } close() {} };
w.eval(im);
await tick();
const $ = s => d.querySelector(s);
const text = $('[data-send] textarea');
const type = v => { text.value = v; text.setSelectionRange(v.length, v.length); text.dispatchEvent(new w.Event('input', { bubbles: true })); };
const key = k => text.dispatchEvent(new w.KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true }));

// «/»: the commands of the chat, loaded once
type('/');
await tick();
assert.deepEqual(gets, ['/im/commands?room=!r']);
let items = [...d.querySelectorAll('.im-cmds .mention-item')].map(e => e.textContent);
assert.deepEqual(items, ['/weather город Погода · Помощник', '/help Справка · Помощник']);
type('/he');
await tick();
assert.equal(gets.length, 1);
items = [...d.querySelectorAll('.im-cmds .mention-item')].map(e => e.textContent);
assert.deepEqual(items, ['/help Справка · Помощник']);
type('/');
await tick();
key('ArrowDown');
key('Enter');
assert.equal(text.value, '/help ');
assert.equal($('.im-cmds'), null);
// not at the start of the text — no list
type('см. /we');
await tick();
assert.equal($('.im-cmds'), null);

// a command button: to the bot, and the buttons of the message are off
$('.im-kbd [data-command="weather"]').click();
await tick();
assert.deepEqual(posts.at(-1), ['/im/button', { room: '!r', event: '$b', command: 'weather', params: 'Москва' }]);
assert.ok([...d.querySelectorAll('.im-kbd button')].every(b => b.disabled));
d.querySelectorAll('.im-kbd button').forEach(b => { b.disabled = false; });

// PUT — into the field; SEND — sent
$('[data-kbd="PUT"]').click();
assert.equal(text.value, '/weather ');
$('[data-kbd="SEND"]').click();
await tick();
assert.equal(posts.at(-1)[0], '/im/send');
assert.equal(posts.at(-1)[1].body, 'Спасибо');

// an item of the menu: to the bot as MENU, the menu closes, the buttons stay on
$('.im-botmenu').click();
await tick();
assert.deepEqual(posts.at(-1), ['/im/button', { room: '!r', event: '$b', command: 'weather', params: 'неделя', menu: '1' }]);
assert.equal($('details.im-menu').open, false);
assert.ok([...d.querySelectorAll('.im-kbd button')].every(b => !b.disabled));

console.log('imbot: ok');
