// The phone's left menu (drawer.js) in jsdom: «Меню» of the bottom bar and ☰ open it, a tap on the shade, a link in
// it, Esc and a swipe to the left close it. node test/js/drawer.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/drawer.js', import.meta.url), 'utf8');
const html = `<aside class="side"><nav><a id="feed" href="/feed">Лента</a><a id="sec" href="#">Раздел</a></nav></aside>
  <header><button id="burger" data-drawer-open>☰</button></header>
  <div class="mnav-shade" id="shade" data-drawer-close hidden></div>
  <nav class="mnav"><button id="menu" data-drawer-open>Меню</button></nav>`;

const dom = new JSDOM(`<!doctype html><body>${html}</body>`, { runScripts: 'outside-only', url: 'http://go24.test/my' });
const w = dom.window;
w.matchMedia = () => ({ matches: true, addEventListener() {} });
w.eval(script);
const $ = id => w.document.getElementById(id);
const opened = () => w.document.documentElement.hasAttribute('data-drawer');
const click = el => el.dispatchEvent(new w.MouseEvent('click', { bubbles: true, cancelable: true }));

click($('menu'));
assert.ok(opened() && !$('shade').hidden, '«Меню» opens the drawer with the shade');
click($('sec'));
assert.ok(opened(), 'a section without a page keeps it open');
click($('shade'));
assert.ok(!opened() && $('shade').hidden, 'the shade closes it');
click($('burger'));
assert.ok(opened(), '☰ opens it');
click($('burger'));
assert.ok(!opened(), '☰ again closes it');
click($('menu'));
click($('feed'));
assert.ok(!opened(), 'a link closes it');
click($('menu'));
w.document.body.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
assert.ok(!opened(), 'Esc closes it');
click($('menu'));
const side = w.document.querySelector('.side');
const touch = (type, x) => { const e = new w.Event(type, { bubbles: true }); e[type === 'touchstart' ? 'touches' : 'changedTouches'] = [{ clientX: x }]; side.dispatchEvent(e); };
touch('touchstart', 250); touch('touchend', 230);
assert.ok(opened(), 'a short move keeps it');
touch('touchstart', 250); touch('touchend', 120);
assert.ok(!opened(), 'a swipe to the left closes it');

// a phone: the current tab of a section's tabs, past the right edge of the scrolled row, is brought into sight
const d2 = new JSDOM(`<!doctype html><body>${html}<div class="tabs" id="tabs"><a href="/a">A</a><a id="on" class="on" href="/b">B</a></div></body>`,
  { runScripts: 'outside-only', url: 'http://go24.test/crm' });
d2.window.matchMedia = () => ({ matches: true, addEventListener() {} });
const tabs = d2.window.document.getElementById('tabs'), on = d2.window.document.getElementById('on');
const def = (el, k, v) => Object.defineProperty(el, k, { configurable: true, get: () => v });
def(tabs, 'scrollWidth', 900); def(tabs, 'clientWidth', 370); def(on, 'offsetLeft', 600); def(on, 'offsetWidth', 90);
d2.window.eval(script);
assert.equal(tabs.scrollLeft, 584, 'the current tab is scrolled into sight');
console.log('drawer.test.mjs: ok');
