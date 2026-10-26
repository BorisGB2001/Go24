// New letters and the files of a letter in jsdom: a «mail» event of the live stream sets the menu's counter in both
// tabs, pops a toast with the letter's quick actions and reloads nothing while letters are ticked; a bell item of a
// letter is not toasted twice; the viewer opens a letter's files one after another; the editor's «С Диска» hands
// the picked file to the editor; the tab's title counts the unread; a drop-down menu stays in the window. node test/js/mailx.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const menu = readFileSync(new URL('../../internal/web/static/js/menu.js', import.meta.url), 'utf8');
const mailx = readFileSync(new URL('../../internal/web/static/js/mailx.js', import.meta.url), 'utf8');
const channels = new Set();
const streams = [];
const posts = [];
const tab = (path, extra = '') => {
  const dom = new JSDOM(`<!doctype html><head><title>Почта · go24</title></head><body><div class="bell"><button data-bell></button>
    <span data-counter="notify" hidden>0</span><div data-bellpop hidden></div></div>
    <div class="navitem" data-mail-unread><a href="/mail">Почта</a><a class="cnt" data-counter="mail">2</a>
      <template data-mail-labels><span data-l="open">Открыть</span><span data-l="read">Прочитано</span><span data-l="delete">Удалить</span><span data-l="flag">Флажок</span></template></div>
    ${extra}</body>`, { runScripts: 'outside-only', url: 'http://go24.test' + path, pretendToBeVisual: true });
  const w = dom.window;
  const store = new Map();
  Object.defineProperty(w, 'localStorage', { value: { getItem: k => store.get(k) ?? null, setItem: (k, v) => store.set(k, String(v)), removeItem: k => store.delete(k) } });
  w.BroadcastChannel = class {
    constructor() { channels.add(this); }
    postMessage(d) { for (const c of channels) if (c !== this) c.onmessage?.({ data: structuredClone(d) }); }
  };
  w.EventSource = class {
    constructor(u) { this.u = u; this.h = {}; this.open = true; streams.push(this); }
    addEventListener(t, f) { this.h[t] = f; }
    close() { this.open = false; }
  };
  w.fetch = async (u, o = {}) => {
    posts.push([String(u), o.body ? String(o.body) : '']);
    const json = d => ({ ok: true, json: async () => d, text: async () => JSON.stringify(d) });
    if (String(u).startsWith('/mail/unread')) return json({ unread: 1 });
    if (String(u).includes('/preview')) return json({ kind: 'html', html: '<table><tr><td>A1</td></tr></table>' });
    if (String(u).startsWith('/mail/drive/link')) return json({ url: 'http://go24.test/files/f9', name: 'план.docx' });
    if (String(u).startsWith('/mail/drive')) return json({ drives: [{ which: 'my', name: 'Мой диск' }], which: 'my', path: [], folders: [], files: [{ id: 'f9', name: 'план.docx', size_text: '1 КБ' }] });
    return json({ result: true });
  };
  w.eval(menu);
  return w;
};

// two tabs: the mail page and another page
const page = tab('/mail?folder=in', `<template data-mx-labels><span data-l="copy">Копия</span><span data-l="link">Ссылка</span></template>
  <div data-mail data-folder="in"><section class="maillist"><div class="mailrow" data-letter="l1"><input type="checkbox" class="msel" checked></div></section>
  <section data-mail-read><div data-mfiles="l1"><a href="/files/a" data-preview="a" data-kind="image" data-name="a.png">a</a>
    <a href="/files/b" data-preview="b" data-kind="html" data-name="b.xlsx">b</a></div>
  <form data-compose><button type="button" data-drive-pick>С Диска</button></form></section></div>`);
page.eval(mailx);
const other = tab('/my');
assert.equal(page.document.title, '(2) Почта · go24', 'the title counts the unread');

const live = streams.find(s => s.open);
const letter = { id: 'l2', folder: 'in', from: 'Босс', subject: 'Счёт', preview: 'Оплатите' };
live.h.mail({ data: JSON.stringify({ unread: 5, letters: [letter], desktop: true, new: 1 }) });
await new Promise(r => setTimeout(r, 10));
for (const w of [page, other]) {
  assert.equal(w.document.querySelector('[data-counter="mail"]').textContent, '5', 'the counter in every tab');
  const t = w.document.querySelector('[data-mail-toast="l2"]');
  assert.ok(t, 'a toast of the letter');
  assert.match(t.textContent, /Босс.*Счёт.*Оплатите/s);
  assert.equal(t.querySelector('a').getAttribute('href'), '/mail?folder=in&letter=l2');
}
assert.equal(page.document.title, '(5) Почта · go24');
assert.ok(!posts.some(([u]) => u.startsWith('http://go24.test/mail?')), 'no reload while a letter is ticked');

// «Прочитано» from the toast
const read = [...other.document.querySelectorAll('[data-mail-toast="l2"] button')].find(b => b.textContent === 'Прочитано');
read.click();
await new Promise(r => setTimeout(r, 10));
assert.ok(posts.some(([u, b]) => u === '/mail/act' && b.includes('letter=l2') && b.includes('do=read')), 'read from the toast');
assert.equal(other.document.querySelector('[data-mail-toast="l2"]'), null, 'the toast goes');
assert.equal(other.document.querySelector('[data-counter="mail"]').textContent, '1', 'the counter follows');

// a bell item of a letter is not a toast of its own
const before = other.document.querySelectorAll('.toast').length;
live.h.notify({ data: JSON.stringify({ id: 9, unread: 1, html: '<a class="nitem" data-nid="9" data-kind="mail">✉ Босс · Счёт</a>' }) });
assert.equal(other.document.querySelectorAll('.toast').length, before, 'no second toast');

// the viewer: a picture, then the next file — a document's HTML
page.document.querySelector('a[data-preview="a"]').dispatchEvent(new page.MouseEvent('click', { bubbles: true, button: 0 }));
let v = page.document.querySelector('.mviewer');
assert.ok(v?.querySelector('img[src="/mail/files/a/raw"]'), 'the picture in the viewer');
assert.match(v.textContent, /1 \/ 2/);
page.document.dispatchEvent(new page.KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }));
await new Promise(r => setTimeout(r, 10));
v = page.document.querySelector('.mviewer');
assert.equal(v.querySelector('.mviewer-doc td')?.textContent, 'A1', 'the next file is the document');
page.document.dispatchEvent(new page.KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
assert.equal(page.document.querySelector('.mviewer'), null, 'Escape closes');

// «С Диска»: the picker hands a link to the editor
const form = page.document.querySelector('[data-compose]');
let got = null;
form.addEventListener('go24:drive', e => { got = e.detail; });
page.document.dispatchEvent(new page.CustomEvent('go24:drivepick', { detail: { form } }));
await new Promise(r => setTimeout(r, 10));
const link = [...page.document.querySelectorAll('.mpicker button')].find(b => b.textContent === 'Ссылка');
link.click();
await new Promise(r => setTimeout(r, 10));
assert.deepEqual(JSON.parse(JSON.stringify(got)), { id: 'f9', name: 'план.docx', mode: 'link', url: 'http://go24.test/files/f9' });
assert.equal(page.document.querySelector('.mpicker'), null);

// a drop-down menu stays in the window: pushed left off the right edge, up when there is no room below
{
  const d = page.document;
  Object.defineProperty(d.documentElement, 'clientWidth', { value: 1024, configurable: true });
  const menuAt = (x, y) => {
    const m = d.createElement('details');
    m.className = 'mbox-menu mmore';
    m.innerHTML = '<summary class="button">…</summary><div class="card mbox-pop"><a class="button">Печать</a></div>';
    d.body.append(m);
    m.querySelector('summary').getBoundingClientRect = () => ({ left: x, right: x + 30, top: y, bottom: y + 30, width: 30, height: 30 });
    const pop = m.querySelector('.mbox-pop');
    Object.defineProperty(pop, 'offsetWidth', { value: 260 });
    Object.defineProperty(pop, 'offsetHeight', { value: 300 });
    m.open = true;
    return m;
  };
  const right = menuAt(990, 100);
  await new Promise(r => setTimeout(r, 10));
  let st = right.querySelector('.mbox-pop').style;
  assert.equal(st.position, 'fixed', 'the menu is fixed');
  assert.equal(st.left, '756px', 'pushed left inside the window: 1024 - 260 - 8');
  assert.equal(st.top, '134px', 'under its button');
  const low = menuAt(100, 700);
  await new Promise(r => setTimeout(r, 10));
  st = low.querySelector('.mbox-pop').style;
  assert.equal(st.left, '100px', 'at its button when there is room');
  assert.equal(st.top, '396px', 'up when there is no room below: 700 - 4 - 300');
  low.open = false;
  await new Promise(r => setTimeout(r, 10));
  assert.equal(low.querySelector('.mbox-pop').getAttribute('style'), null, 'closed: its place is forgotten');
}

page.close();
other.close();
console.log('mailx.js: new letters, the viewer, the drive picker, the menus in the window passed');
process.exit(0);
