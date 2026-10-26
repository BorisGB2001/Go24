// «Звонит …» (static/js/ring.js) in jsdom: a ring shows who calls and how; «Ответить» tells go24 and opens the call,
// «Без видео» opens it with sound only, «Отклонить» tells the caller; «stop» (answered on another device, the caller
// gave up) and the end of the time take the window away; a ring already shown is not shown twice.
// node test/js/ring.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const ring = readFileSync(new URL('../../internal/web/static/js/ring.js', import.meta.url), 'utf8');
const dom = new JSDOM(`<!doctype html><body><div hidden data-ring-texts data-calling="%s звонит" data-video="Видеозвонок" data-audio="Аудиозвонок"
  data-in-chat="в чате «%s»" data-accept="Ответить" data-audio-only="Без видео" data-decline="Отклонить"></div></body>`,
  { runScripts: 'outside-only', url: 'http://go24.test/my', pretendToBeVisual: true });
const w = dom.window, d = w.document;
const posts = [], opened = [];
w.fetch = async (url, opts = {}) => { posts.push([url, opts.method]); return { ok: true }; };
w.open = url => { opened.push(url); return {}; };
w.eval(ring);
const call = detail => w.dispatchEvent(new w.CustomEvent('go24:call', { detail }));
const pops = () => [...d.querySelectorAll('.ringpop')];
const btn = (pop, text) => [...pop.querySelectorAll('button')].find(b => b.textContent === text);
const wait = ms => new Promise(r => setTimeout(r, ms));

// a video call in a group chat: who, how, where; «Без видео» opens it with sound only
call({ ring: 'r1', kind: 'ring', from: 'Анна', photo: '/u/a/photo', chat: 'Запуск', video: true, url: 'https://go24/call?room=%21g&video=1', left: 30 });
call({ ring: 'r1', kind: 'ring', from: 'Анна', video: true, url: 'x', left: 30 }); // the same ring again (another tab's copy)
assert.equal(pops().length, 1);
const p = pops()[0];
assert.equal(p.querySelector('b').textContent, 'Анна звонит');
assert.equal(p.querySelector('.small').textContent, 'Видеозвонок · в чате «Запуск»');
assert.equal(p.querySelector('img').getAttribute('src'), '/u/a/photo');
btn(p, 'Без видео').click();
assert.deepEqual(posts.at(-1), ['/call/ring/r1/accept', 'POST']);
assert.deepEqual(opened, ['https://go24/call?room=%21g&video=0']);
assert.equal(pops().length, 0);

// an audio call: no «Без видео»; «Отклонить» tells the caller
call({ ring: 'r2', kind: 'ring', from: 'Борис', video: false, url: 'https://go24/call?room=%21d&video=0', left: 30 });
assert.equal(btn(pops()[0], 'Без видео'), undefined);
assert.equal(pops()[0].querySelector('.small').textContent, 'Аудиозвонок');
btn(pops()[0], 'Отклонить').click();
assert.deepEqual(posts.at(-1), ['/call/ring/r2/decline', 'POST']);
assert.equal(pops().length, 0);

// answered on another device, or the caller gave up: gone; a ring whose time is over is not shown
call({ ring: 'r3', kind: 'ring', from: 'Вера', url: 'u', left: 30 });
call({ ring: 'r3', kind: 'stop' });
assert.equal(pops().length, 0);
call({ ring: 'r4', kind: 'ring', from: 'Вера', url: 'u', left: 0 });
assert.equal(pops().length, 0);
// «Ответить» opens the call as it rang
call({ ring: 'r5', kind: 'ring', from: 'Вера', video: true, url: 'https://go24/call?room=%21v&video=1', left: 30 });
btn(pops()[0], 'Ответить').click();
assert.equal(opened.at(-1), 'https://go24/call?room=%21v&video=1');
// the time runs out by itself
call({ ring: 'r6', kind: 'ring', from: 'Вера', url: 'u', left: 1 });
assert.equal(pops().length, 1);
await wait(1100);
assert.equal(pops().length, 0);
// events about one's own call (answers) are not rings
call({ ring: 'r7', kind: 'declined', person: 'Анна' });
assert.equal(pops().length, 0);
console.log('ring.js: incoming calls passed');
