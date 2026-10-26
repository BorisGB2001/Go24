// «База знаний»: the tree of pages — the branch down to the open page is open, an arrow opens and closes a branch,
// «+» adds a subpage, a page dragged before, after or into another one is sent to /place, in jsdom.
// node test/js/kbtree.test.mjs
import { JSDOM, VirtualConsole } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/kbtree.js', import.meta.url), 'utf8');
const tree = `<ul class="kbtree">
  <li><a href="/kb/s1/a" data-kb-page="a">A</a><ul class="kbtree">
    <li><a href="/kb/s1/a1" data-kb-page="a1">A1</a><ul class="kbtree"><li><a href="/kb/s1/a11" data-kb-page="a11">A11</a></li></ul></li>
    <li><a href="/kb/s1/a2" data-kb-page="a2">A2</a></li></ul></li>
  <li><a href="/kb/s1/b" data-kb-page="b">B</a></li>
  <li><a href="/kb/s1/c" data-kb-page="c">C</a><ul class="kbtree"><li><a href="/kb/s1/c1" data-kb-page="c1">C1</a></li></ul></li></ul>`;
// jsdom cannot reload a page: it reports that, which is counted as the reload
let reloads = 0;
const virtualConsole = new VirtualConsole();
virtualConsole.on('jsdomError', e => { if (/navigation|reload/i.test(e.message)) reloads++; else console.error(e); });
const page = edit => new JSDOM(`<!doctype html><body><div data-kb-current="a11"><nav class="kbnav" data-kb-space="s1" data-kb-add="Добавить подстраницу"${edit ? ' data-kb-edit' : ''}>${tree}</nav></div></body>`,
  { runScripts: 'outside-only', url: 'http://go24.test/kb/s1/a11', virtualConsole });

// reading only: no «+», nothing dragged; the branch down to the open page is open, the others are closed
{
  const w = page(false).window;
  w.eval(script);
  const li = id => w.document.querySelector(`[data-kb-page="${id}"]`).closest('li');
  assert.ok(w.document.querySelector('[data-kb-page="a11"]').classList.contains('on'));
  assert.ok(li('a').classList.contains('open') && li('a1').classList.contains('open'));
  assert.ok(!li('c').classList.contains('open'));
  assert.equal(w.document.querySelectorAll('.kbadd').length, 0);
  assert.ok(!w.document.querySelector('[data-kb-page="b"]').hasAttribute('draggable'));
  assert.ok(li('b').querySelector('.kbtoggle').disabled, 'a page without subpages has no arrow');
  li('c').querySelector('.kbtoggle').click();
  assert.ok(li('c').classList.contains('open'));
  assert.deepEqual(JSON.parse(w.localStorage.getItem('go24.kb.open.s1')).sort(), ['a', 'a1', 'c']);
  // remembered: opened again on the next page
  const w2 = page(false).window;
  w2.localStorage.setItem('go24.kb.open.s1', '["c"]');
  w2.eval(script);
  assert.ok(w2.document.querySelector('[data-kb-page="c"]').closest('li').classList.contains('open'));
}

// writing: «+» at each page, pages dragged
{
  const w = page(true).window;
  const sent = [];
  let status = 204;
  w.fetch = async (url, opts) => { sent.push({ url, body: Object.fromEntries(opts.body) }); return { ok: status < 300, status, statusText: 'x', json: async () => ({ error: 'Нет доступа' }) }; };
  w.alert = msg => sent.push({ alert: msg });
  w.eval(script);
  const a = id => w.document.querySelector(`[data-kb-page="${id}"]`);
  assert.equal(w.document.querySelectorAll('.kbadd').length, 7);
  assert.equal(a('b').nextElementSibling.getAttribute('href'), '/kb/s1?new=1&parent=b');
  assert.equal(a('b').getAttribute('draggable'), 'true');
  // every link is 20px high, at y = 0
  for (const el of w.document.querySelectorAll('a[data-kb-page]')) el.getBoundingClientRect = () => ({ top: 0, height: 20 });
  const tick = async () => { for (let i = 0; i < 5; i++) await new Promise(r => setTimeout(r, 0)); };
  const ev = (type, el, y) => {
    const e = new w.Event(type, { bubbles: true, cancelable: true });
    e.clientY = y;
    e.dataTransfer = { setData() {}, effectAllowed: '' };
    el.dispatchEvent(e);
    return e;
  };
  const drag = async (from, to, y) => {
    sent.length = 0;
    ev('dragstart', a(from), 10);
    const over = ev('dragover', a(to), y);
    const drop = ev('drop', a(to), y);
    ev('dragend', a(from), 10);
    await tick();
    return over.defaultPrevented && drop.defaultPrevented;
  };

  // onto the middle of B: under it
  assert.ok(await drag('c', 'b', 10));
  assert.deepEqual(sent, [{ url: '/kb/s1/c/place', body: { parent: 'b' } }]);
  assert.equal(reloads, 1);
  // onto the top of A2: before it, under A
  await drag('b', 'a2', 2);
  assert.deepEqual(sent, [{ url: '/kb/s1/b/place', body: { parent: 'a', before: 'a2' } }]);
  // onto the bottom of B: after it, at the top — before C
  await drag('a1', 'b', 18);
  assert.deepEqual(sent, [{ url: '/kb/s1/a1/place', body: { parent: '', before: 'c' } }]);
  // onto the bottom of the last page: last
  await drag('a', 'c', 18);
  assert.deepEqual(sent, [{ url: '/kb/s1/a/place', body: { parent: '', before: '' } }]);
  // after the page right above it: stays before the page that was after it
  await drag('c', 'b', 18);
  assert.deepEqual(sent, [{ url: '/kb/s1/c/place', body: { parent: '', before: '' } }]);
  // not onto itself nor under itself
  assert.equal(await drag('a', 'a11', 10), false);
  assert.equal(await drag('a', 'a', 10), false);
  assert.deepEqual(sent, []);
  // refused: the message, no reload
  status = 403;
  reloads = 0;
  await drag('b', 'c', 10);
  assert.deepEqual(sent.at(-1), { alert: 'Нет доступа' });
  assert.equal(reloads, 0);
}
console.log('kbtree ok');
