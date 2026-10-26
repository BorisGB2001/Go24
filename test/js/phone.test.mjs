// The softphone from a page (static/js/phone.js) in jsdom: a click on a tel: number opens the softphone window
// with the number when it is not open, or sends the number to the open one; an incoming call of the softphone pops
// up with «Ответить» and «Отклонить», which go to that window; the top bar button shows a call going on.
// node test/js/phone.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const phone = readFileSync(new URL('../../internal/web/static/js/phone.js', import.meta.url), 'utf8');
const dom = new JSDOM(`<!doctype html><body><div hidden data-phone-texts data-url="https://go24:8444/phone" data-open="Телефон" data-calling="Входящий звонок: %s"
  data-answer="Ответить" data-decline="Отклонить" data-talking="Разговор: %s" data-hangup="Завершить"></div>
  <button data-phone-open></button>
  <a id="mob" href="tel:+7 (900) 123-45-67">+7 900</a><a id="mail" href="mailto:a@b">a</a><a id="bad" href="tel:abc">?</a></body>`,
  { runScripts: 'outside-only', url: 'http://go24.test/u/1', pretendToBeVisual: true });
const w = dom.window, d = w.document;
const posts = [], opened = [];
let phoneWin = null; // what window.open('', 'go24phone') finds
w.fetch = async (url, opts = {}) => { posts.push([url, Object.fromEntries(opts.body)]); return { json: async () => ({ sent: 'p1' }) }; };
w.open = (url, name) => {
  opened.push([url, name]);
  if (phoneWin) return phoneWin;
  const blank = { location: { href: 'about:blank' }, focus() {} };
  return blank;
};
w.eval(phone);
const click = id => d.getElementById(id).dispatchEvent(new w.MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }));
const wait = ms => new Promise(r => setTimeout(r, ms));

// no softphone window yet: a blank one opens and goes to /phone with the number
let blank;
w.open = (url, name) => { opened.push([url, name]); blank = { location: { href: 'about:blank' }, focus() {} }; return blank; };
click('mob');
assert.deepEqual(opened.at(-1), ['', 'go24phone']);
assert.equal(blank.location.href, 'https://go24:8444/phone?dial=%2B79001234567');
assert.equal(posts.length, 0);

// the softphone is open (its own page, another origin — location unreadable): the number goes to it, it is not reloaded
let focused = 0;
phoneWin = { focus() { focused++; } };
Object.defineProperty(phoneWin, 'location', { get() { throw new Error('cross-origin'); } });
w.open = (url, name) => { opened.push([url, name]); return phoneWin; };
click('mob');
await wait(0);
assert.deepEqual(posts.at(-1), ['/telephony/cmd', { cmd: 'dial', number: '+79001234567' }]);
assert.equal(focused, 1);
// not a phone number: the browser does as usual
const n = posts.length;
click('mail'); click('bad');
assert.equal(posts.length, n);

// the top bar button just shows the window
d.querySelector('[data-phone-open]').click();
assert.equal(focused, 2);
assert.equal(posts.length, n);

// an incoming call of the softphone: who, «Ответить» goes to that window
const call = detail => w.dispatchEvent(new w.CustomEvent('go24:call', { detail }));
call({ kind: 'phone', state: 'ringing', dir: 'in', number: '102', from: 'Анна', user: 'u2', photo: '/u/u2/photo', phone: 'p1' });
let pop = d.querySelector('.ringpop');
assert.equal(pop.querySelector('b').textContent, 'Входящий звонок: Анна');
assert.equal(pop.querySelector('span').textContent, '102');
assert.equal(d.querySelector('[data-phone-open]').dataset.state, 'ringing');
[...pop.querySelectorAll('button')].find(b => b.textContent === 'Ответить').click();
assert.deepEqual(posts.at(-1), ['/telephony/cmd', { cmd: 'answer', phone: 'p1' }]);
assert.equal(d.querySelectorAll('.ringpop').length, 0);
// talking: the button says so; «Отклонить» of another call hangs up; idle takes the pop-up away
call({ kind: 'phone', state: 'talking', dir: 'in', number: '102', from: 'Анна', phone: 'p1' });
assert.equal(d.querySelector('[data-phone-open]').title, 'Разговор: Анна');
call({ kind: 'phone', state: 'ringing', dir: 'in', number: '+79001234567', phone: 'p1' });
pop = d.querySelector('.ringpop');
assert.equal(pop.querySelector('b').textContent, 'Входящий звонок: +79001234567');
[...pop.querySelectorAll('button')].find(b => b.textContent === 'Отклонить').click();
assert.deepEqual(posts.at(-1), ['/telephony/cmd', { cmd: 'hangup', phone: 'p1' }]);
call({ kind: 'phone', state: 'ringing', dir: 'in', number: '103', phone: 'p1' });
call({ kind: 'phone', state: 'idle', dir: 'in', number: '103', phone: 'p1' });
assert.equal(d.querySelectorAll('.ringpop').length, 0);
assert.equal(d.querySelector('[data-phone-open]').dataset.state, '');
// one's own outgoing call does not pop up; other calls (chat rings) are not the softphone's
call({ kind: 'phone', state: 'ringing', dir: 'out', number: '104', phone: 'p1' });
call({ kind: 'ring', ring: 'r1', from: 'Вера', left: 30 });
assert.equal(d.querySelectorAll('.ringpop').length, 0);
console.log('phone.test.mjs: ok');
