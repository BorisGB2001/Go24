// One pop-up at a time (pop.js) in jsdom: menus (<details> with floating content) and pop-ups built by scripts close
// when another opens, on a click outside and on Esc. node test/js/pop.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/pop.js', import.meta.url), 'utf8');

const html = `<style>.m { position: relative } .pop { position: absolute }</style>
  <details class="m" id="box"><summary>Ящик</summary><div class="pop">Новая папка
    <details class="m" id="sub"><summary>Ещё</summary><div class="pop">Подменю</div></details>
    <details id="inline"><summary>Подробнее</summary><p>текст</p></details>
    <input id="who"></div></details>
  <details class="m" id="folder"><summary>Папка</summary><div class="pop">Переименовать</div></details>
  <details class="m" id="keep" data-pop="keep"><summary>Панель</summary><div class="pop">остаётся</div></details>
  <details class="card" id="card"><summary>Карточка</summary><p>не меню</p></details>
  <dialog id="dlg"><details class="m" id="indlg"><summary>В окне</summary><div class="pop">меню</div></details><p id="dtext">текст</p></dialog>
  <p id="out">вне</p>`;

function page() {
  const dom = new JSDOM(`<!doctype html><body>${html}</body>`, { runScripts: 'outside-only', url: 'http://go24.test/mail' });
  const w = dom.window;
  w.eval(script);
  return w;
}
const tick = () => new Promise(r => setTimeout(r, 10));
const $ = (w, id) => w.document.getElementById(id);
const open = async (w, id) => { $(w, id).open = true; await tick(); }; // the toggle event comes after
const down = (w, el) => el.dispatchEvent(new w.Event('pointerdown', { bubbles: true, cancelable: true }));
const esc = w => { const e = new w.KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }); w.document.body.dispatchEvent(e); return e; };

// 1. opening a menu closes the other one (the mailbox's and the folder's — Борис's screenshot); its own sub-menu
// keeps it open; panels that are not pop-ups are not touched
{
  const w = page();
  await open(w, 'card');
  await open(w, 'keep');
  await open(w, 'box');
  await open(w, 'sub');
  await open(w, 'inline');
  assert.ok($(w, 'box').open && $(w, 'sub').open, 'a sub-menu keeps its menu open');
  await open(w, 'folder');
  assert.equal($(w, 'box').open, false, 'the mailbox menu closed');
  assert.equal($(w, 'sub').open, false);
  assert.ok($(w, 'folder').open && $(w, 'card').open && $(w, 'keep').open && $(w, 'inline').open);
}

// 2. a click outside closes the menu, a click inside does not (outside its sub-menu — closes the sub-menu); closing a
// menu closes its sub-menus
{
  const w = page();
  await open(w, 'box');
  await open(w, 'sub');
  down(w, $(w, 'sub').querySelector('.pop'));
  assert.ok($(w, 'box').open && $(w, 'sub').open);
  down(w, $(w, 'who'));
  assert.ok($(w, 'box').open, 'a click in the menu keeps it');
  assert.equal($(w, 'sub').open, false, 'the sub-menu closed: the click was outside it');
  await open(w, 'sub');
  down(w, $(w, 'out'));
  assert.equal($(w, 'box').open, false);
  await tick();
  assert.equal($(w, 'sub').open, false, 'the sub-menu closed with it');
}

// 3. Esc closes the open menus; nothing open — Esc is left to others (a modal window closes by itself)
{
  const w = page();
  await open(w, 'folder');
  assert.ok(esc(w).defaultPrevented);
  assert.equal($(w, 'folder').open, false);
  assert.equal(esc(w).defaultPrevented, false);
}

// 4. a pop-up built by a script (go24Pop): opening it closes the menus not holding its owner; a menu opening closes
// it; a click in it counts as a click on its owner (a picker of a field in a menu keeps the menu open)
{
  const w = page();
  await open(w, 'box');
  const picker = w.document.createElement('div');
  w.document.body.append(picker);
  let closed = 0;
  w.go24Pop(picker, () => { closed++; picker.remove(); }, $(w, 'who')); // the field is in the mailbox menu
  assert.ok($(w, 'box').open, 'the menu of the field stays');
  down(w, picker);
  assert.ok($(w, 'box').open, 'a click in the picker keeps its menu');
  assert.equal(closed, 0);
  down(w, $(w, 'out'));
  assert.equal(closed, 1);
  assert.equal($(w, 'box').open, false);

  const card = w.document.createElement('div');
  w.document.body.append(card);
  let cardClosed = 0;
  await open(w, 'box');
  w.go24Pop(card, () => { cardClosed++; card.remove(); }, $(w, 'out'));
  assert.equal($(w, 'box').open, false, 'the card closes the menu');
  await open(w, 'folder');
  assert.equal(cardClosed, 1, 'a menu opening closes the card');

  const bell = w.document.createElement('div');
  w.document.body.append(bell);
  w.go24Pop(bell, () => { bell.hidden = true; }, $(w, 'out'));
  assert.equal($(w, 'folder').open, false);
  assert.ok(esc(w).defaultPrevented);
  assert.equal(bell.hidden, true, 'Esc closes it');
}

// 5. a modal window over a menu: clicks in it do not close the menu; Esc closes the window's own menu only
{
  const w = page();
  await open(w, 'box');
  $(w, 'dlg').setAttribute('open', ''); // as showModal()
  await open(w, 'indlg');
  assert.ok($(w, 'box').open, 'the menu under the window stays');
  down(w, $(w, 'dtext'));
  assert.ok($(w, 'box').open);
  assert.equal($(w, 'indlg').open, false, 'a click in the window closes its menu');
  await open(w, 'indlg');
  assert.ok(esc(w).defaultPrevented);
  assert.equal($(w, 'indlg').open, false);
  assert.ok($(w, 'box').open);
  assert.equal(esc(w).defaultPrevented, false, 'then Esc closes the window');
}

console.log('pop.test.mjs: ok');
