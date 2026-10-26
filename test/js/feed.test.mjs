// «Лента» in jsdom: a post shows once even when the live event beats the publish answer. node test/js/feed.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/feed.js', import.meta.url), 'utf8');
const dom = new JSDOM(`<!doctype html><body><main class="content"><div data-feed data-failed="x">
  <form method="post" action="/feed" data-post><div data-error hidden></div><textarea name="body">текст</textarea></form>
  <div data-items><div data-empty>пусто</div></div></div></main></body>`, { runScripts: 'outside-only', url: 'http://go24.test/feed' });
const w = dom.window;
let es;
w.EventSource = class { constructor(u) { this.u = u; this.h = {}; es = this; } addEventListener(t, f) { this.h[t] = f; } };
const html = '<article data-post-id="p1">пост</article>';
let release;
w.fetch = () => new Promise(r => { release = () => r({ ok: true, json: async () => ({ id: 'p1', html }) }); });
w.eval(script);
w.document.querySelector('form[data-post]').dispatchEvent(new w.Event('submit', { bubbles: true, cancelable: true }));
es.h.post({ data: JSON.stringify({ id: 'p1', html }) }); // the live event comes first
release();
await new Promise(r => setTimeout(r, 20));
assert.equal(w.document.querySelectorAll('[data-post-id="p1"]').length, 1, 'no duplicate');
assert.equal(w.document.querySelector('[data-empty]'), null);
console.log('feed.js: no duplicate post passed');

// a post with files goes as multipart (FormData), one without as a plain form
{
  const dom = new JSDOM(`<!doctype html><body><main class="content"><div data-feed data-failed="x">
    <form method="post" action="/feed" data-post><div data-error hidden></div><textarea name="body">текст</textarea>
      <input type="file" name="files" data-files><span data-files-names>a.png</span></form>
    <div data-items></div></div></main></body>`, { runScripts: 'outside-only', url: 'http://go24.test/feed' });
  const w = dom.window;
  w.EventSource = class { addEventListener() {} };
  const sent = [];
  w.fetch = async (url, opts) => { sent.push(opts.body); return { ok: true, json: async () => ({ id: 'p' + sent.length, html: `<article data-post-id="p${sent.length}"></article>` }) }; };
  w.eval(script);
  const form = w.document.querySelector('form[data-post]');
  const Base = w.FormData;
  w.FormData = class extends Base { constructor(f) { super(f); this.set('files', new w.File(['x'], 'a.png', { type: 'image/png' })); } };
  form.dispatchEvent(new w.Event('submit', { bubbles: true, cancelable: true }));
  await new Promise(r => setTimeout(r, 20));
  w.FormData = Base;
  form.dispatchEvent(new w.Event('submit', { bubbles: true, cancelable: true }));
  await new Promise(r => setTimeout(r, 20));
  assert.ok(sent[0] instanceof Base, 'with files: multipart');
  assert.ok(sent[1] instanceof w.URLSearchParams, 'without files: a plain form');
  assert.equal(w.document.querySelector('[data-files-names]').textContent, '', 'the chosen names are cleared');
  console.log('feed.js: files in a post passed');
}

// a post of a chat message («Создать пост в ленте»): the message's address goes once, then the form is a plain one
{
  const dom = new JSDOM(`<!doctype html><body><main class="content"><div data-feed data-failed="x">
    <form method="post" action="/feed" data-post><div data-error hidden></div>
      <div data-im-src><input type="hidden" name="im_room" value="!r"><input type="hidden" name="im_event" value="$e"></div>
      <textarea name="body">Сообщение от Анна</textarea>
      <select name="project"><option value="">все</option><option value="p1" selected>Бухгалтерия</option></select></form>
    <div data-items></div></div></main></body>`, { runScripts: 'outside-only', url: 'http://go24.test/feed?im_room=!r&im_event=$e' });
  const w = dom.window;
  w.EventSource = class { addEventListener() {} };
  const sent = [];
  w.fetch = async (url, opts) => { sent.push(Object.fromEntries(opts.body)); return { ok: true, json: async () => ({ id: 'p' + sent.length, html: `<article data-post-id="p${sent.length}"></article>` }) }; };
  w.eval(script);
  const form = w.document.querySelector('form[data-post]');
  form.dispatchEvent(new w.Event('submit', { bubbles: true, cancelable: true }));
  await new Promise(r => setTimeout(r, 20));
  assert.equal(sent[0].im_event, '$e');
  assert.equal(sent[0].project, 'p1');
  assert.equal(form.elements.body.value, '', 'the message does not come back');
  assert.equal(form.elements.project.value, '', 'nor its project');
  assert.equal(w.location.search, '');
  form.elements.body.value = 'ещё';
  form.dispatchEvent(new w.Event('submit', { bubbles: true, cancelable: true }));
  await new Promise(r => setTimeout(r, 20));
  assert.equal(sent[1].im_event, undefined, 'the next post is not of the message');
  console.log('feed.js: a post of a chat message passed');
}

// files of a comment: pasted from the clipboard or dropped on the form, they join the chosen ones and
// go with the comment; the form is cleared after it (GO24DEV-463)
{
  const dom = new JSDOM(`<!doctype html><body><main class="content"><div data-feed data-failed="x"><div data-items>
    <article data-post-id="p1"><div data-comments></div><form class="fcomment-form" data-comment><input type="text" name="body">
      <input type="file" name="files" multiple data-files><div class="fcomment-files"><span data-files-names></span>
      <span data-disk-picked><span class="dpick-chip">смета.pdf</span></span></div></form></article></div></div></main></body>`,
  { runScripts: 'outside-only', url: 'http://go24.test/feed' });
  const w = dom.window;
  w.EventSource = class { addEventListener() {} };
  w.DataTransfer = class { constructor() { this.list = []; this.items = { add: f => this.list.push(f) }; } get files() { return this.list; } };
  const form = w.document.querySelector('form[data-comment]'), input = form.querySelector('[data-files]');
  let chosen = [];
  Object.defineProperty(input, 'files', { get: () => chosen, set: v => { chosen = [...v]; } });
  let sent;
  w.fetch = async (url, opts) => { sent = { url, body: opts.body }; return { ok: true, json: async () => ({ html: '<div class="fc">c</div>' }) }; };
  w.eval(script);
  const ev = (type, init) => Object.assign(new w.Event(type, { bubbles: true, cancelable: true }), init);
  const text = form.querySelector('[name=body]');
  const paste = ev('paste', { clipboardData: { files: [new w.File(['x'], 'снимок.png', { type: 'image/png' })] } });
  text.dispatchEvent(paste);
  assert.ok(paste.defaultPrevented, 'a pasted picture is taken');
  const over = ev('dragover', { dataTransfer: { types: ['Files'] } });
  text.dispatchEvent(over);
  assert.ok(over.defaultPrevented && form.classList.contains('fdrop'), 'the form shows the drop');
  text.dispatchEvent(ev('drop', { dataTransfer: { files: [new w.File(['y'], 'акт.pdf')] } }));
  assert.ok(!form.classList.contains('fdrop'));
  assert.deepEqual(chosen.map(f => f.name), ['снимок.png', 'акт.pdf'], 'pasted and dropped files together');
  assert.equal(form.querySelector('[data-files-names]').textContent, 'снимок.png, акт.pdf');
  const plain = ev('paste', { clipboardData: { files: [] } }); // text is pasted as usual
  text.dispatchEvent(plain);
  assert.ok(!plain.defaultPrevented);
  form.dispatchEvent(ev('submit'));
  await new Promise(r => setTimeout(r, 20));
  assert.equal(sent.url, '/feed/p1/comments');
  assert.equal(w.document.querySelectorAll('[data-comments] .fc').length, 1, 'the comment is shown');
  assert.equal(form.querySelector('.dpick-chip'), null, 'the drive files are cleared');
  console.log('feed.js: files in a comment passed');
}

// «Изменить» a post as a whole (GO24DEV-469): the form comes from the server in place of the text; «Кому» and
// «Важное» show their fields in it; «Отмена» brings the text back, saving replaces the post
{
  const dom = new JSDOM(`<!doctype html><body><main class="content"><div data-feed data-failed="x"><div data-items>
    <article data-post-id="p1"><button data-edit>✎</button><div data-view>текст</div></article></div></div></main></body>`,
  { runScripts: 'outside-only', url: 'http://go24.test/feed' });
  const w = dom.window;
  w.EventSource = class { addEventListener() {} };
  const form = `<form class="fedit" data-edit-form><input type="hidden" name="full" value="1"><textarea name="body">текст</textarea>
    <select name="project" data-feed-to><option value="">все</option><option value="people">люди</option></select>
    <div data-feed-people hidden></div>
    <input type="checkbox" name="important" data-important><span data-important-for hidden>
      <select name="important_for" data-until-sel><option value="always">всегда</option><option value="date">до</option></select>
      <input type="date" name="important_until" data-until-date hidden></span>
    <button type="button" data-edit-cancel>отмена</button></form>`;
  const calls = [];
  w.fetch = async (url, opts) => {
    calls.push({ url, method: opts?.method || 'GET', body: opts?.body });
    if (!opts?.method) return { ok: true, json: async () => ({ html: form }) };
    return { ok: true, json: async () => ({ html: '<article data-post-id="p1"><div data-view>новый</div></article>' }) };
  };
  w.eval(script);
  const card = () => w.document.querySelector('[data-post-id="p1"]');
  const click = el => el.dispatchEvent(new w.MouseEvent('click', { bubbles: true }));
  const change = el => el.dispatchEvent(new w.Event('change', { bubbles: true }));
  click(card().querySelector('[data-edit]'));
  await new Promise(r => setTimeout(r, 20));
  assert.equal(calls[0].url, '/feed/p1/edit');
  const f = card().querySelector('[data-edit-form]');
  assert.ok(f && card().querySelector('[data-view]').hidden, 'the form in place of the text');
  click(card().querySelector('[data-edit]'));
  await new Promise(r => setTimeout(r, 20));
  assert.equal(card().querySelectorAll('[data-edit-form]').length, 1, 'opened once');
  f.elements.project.value = 'people'; change(f.elements.project);
  assert.ok(!f.querySelector('[data-feed-people]').hidden, '«Кому»: the picker of people');
  f.elements.important.checked = true; change(f.elements.important);
  f.elements.important_for.value = 'date'; change(f.elements.important_for);
  assert.ok(!f.querySelector('[data-important-for]').hidden && !f.elements.important_until.hidden && f.elements.important_until.required, '«Важное до» a date');
  click(f.querySelector('[data-edit-cancel]'));
  assert.ok(!card().querySelector('[data-edit-form]') && !card().querySelector('[data-view]').hidden, 'cancelled');
  click(card().querySelector('[data-edit]'));
  await new Promise(r => setTimeout(r, 20));
  card().querySelector('[data-edit-form]').dispatchEvent(new w.Event('submit', { bubbles: true, cancelable: true }));
  await new Promise(r => setTimeout(r, 20));
  const saved = calls.find(c => c.method === 'POST');
  assert.equal(saved.url, '/feed/p1/edit');
  assert.equal(new w.URLSearchParams(saved.body).get('full'), '1');
  assert.equal(card().querySelector('[data-view]').textContent, 'новый', 'the post is replaced');
  console.log('feed.js: the whole edit of a post passed');
}
