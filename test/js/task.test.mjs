// Task card in jsdom: chips open blocks, the chat sends without a reload. node test/js/task.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/task.js', import.meta.url), 'utf8');

function page(responses) {
  const dom = new JSDOM(`<!doctype html><body><main class="content">
    <div class="tcard" data-copied="ok">
      <div class="card tblock" id="checklist" hidden><input name="title"></div>
      <div class="tchips"><button class="tchip" data-show="checklist" data-focus="#checklist input">chip</button></div>
      <aside class="tchat"><div class="msgs" data-msgs><div class="chat-empty">empty</div></div>
        <form method="post" action="/t/GO-1/comments" data-chat data-failed="Не удалось"><textarea name="body"></textarea><button>send</button></form></aside>
    </div></main></body>`, { runScripts: 'outside-only', url: 'http://go24.test/t/GO-1', pretendToBeVisual: true });
  const w = dom.window;
  w.HTMLElement.prototype.scrollIntoView = () => {};
  w.fetch = async (url, opts) => {
    const r = responses.shift();
    w.lastRequest = { url, body: String(opts.body), accept: opts.headers.Accept };
    return { ok: r.status < 300, status: r.status, json: async () => r.body };
  };
  w.eval(script);
  return w;
}
const tick = () => new Promise(r => setTimeout(r, 10));

// 1. a chip opens its block and hides itself
{
  const w = page([]);
  w.document.querySelector('.tchip').click();
  assert.equal(w.document.getElementById('checklist').hidden, false);
  assert.equal(w.document.querySelector('.tchip').hidden, true);
  assert.equal(w.document.activeElement, w.document.querySelector('#checklist input'));
}

// 2. Enter sends the message; it appears in the chat, the empty hint goes away, the field clears
{
  const w = page([{ status: 201, body: { id: 'm1', html: '<div class="msg mine" data-id="m1">Привет</div>' } }]);
  const text = w.document.querySelector('textarea');
  text.value = 'Привет';
  text.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }));
  await tick();
  assert.equal(w.lastRequest.url, 'http://go24.test/t/GO-1/comments');
  assert.equal(w.lastRequest.accept, 'application/json');
  assert.equal(w.lastRequest.body, 'body=%D0%9F%D1%80%D0%B8%D0%B2%D0%B5%D1%82');
  assert.ok(w.document.querySelector('.msg[data-id="m1"]'));
  assert.equal(w.document.querySelector('.chat-empty'), null);
  assert.equal(text.value, '');
}

// 3. Shift+Enter is a new line, not a send; an error keeps the text and shows the message
{
  const w = page([{ status: 422, body: { error: 'Пустой комментарий' } }]);
  const text = w.document.querySelector('textarea');
  text.value = 'x';
  text.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Enter', shiftKey: true, bubbles: true, cancelable: true }));
  await tick();
  assert.equal(w.lastRequest, undefined);
  w.document.querySelector('form[data-chat]').requestSubmit();
  await tick();
  assert.equal(w.document.querySelector('.chat-error').textContent, 'Пустой комментарий');
  assert.equal(text.value, 'x');
}

// 4. live: a message pushed by the server appears once; our own post is not duplicated
{
  const w = page([{ status: 201, body: { id: 'm7', html: '<div class="msg mine" data-id="m7">моё</div>' } }]);
  let es;
  w.EventSource = class { constructor(url) { this.url = url; this.h = {}; es = this; } addEventListener(t, f) { this.h[t] = f; } };
  w.CSS = { escape: (s) => s };
  w.document.querySelector('.tcard').dataset.task = 'GO-1';
  w.eval(script); // re-run with EventSource available
  assert.equal(es.url, '/t/GO-1/live');
  es.h.message({ data: JSON.stringify({ id: 'm9', html: '<div class="msg" data-id="m9">из Element</div>' }) });
  assert.equal(w.document.querySelectorAll('.msg[data-id="m9"]').length, 1);
  es.h.message({ data: JSON.stringify({ id: 'm9', html: '<div class="msg" data-id="m9">из Element</div>' }) });
  assert.equal(w.document.querySelectorAll('.msg[data-id="m9"]').length, 1, 'no duplicate');
  assert.equal(w.document.querySelector('.chat-empty'), null);
  // a message seen in the open chat reads the task's notifications and updates the bell
  w.document.body.insertAdjacentHTML('beforeend', '<span data-counter="notify">2</span>');
  w.fetch = async (url, opts) => { w.lastRequest = { url, body: String(opts.body) }; return { ok: true, json: async () => ({ unread: 0 }) }; };
  await new Promise(r => setTimeout(r, 1600));
  assert.equal(w.lastRequest.url, '/notifications/read');
  assert.equal(w.lastRequest.body, 'task=GO-1');
  assert.equal(w.document.querySelector('[data-counter="notify"]').hidden, true);
}

// 5. a background form: an error keeps everything typed and shows the message in the form
{
  const w = page([{ status: 422, body: { error: 'Укажите имя или фамилию' } }]);
  const tc = w.document.querySelector('.tcard');
  tc.insertAdjacentHTML('beforeend', '<form method="post" action="/u/1" data-ajax data-failed="x"><div data-error hidden></div><input name="position" value="Инженер"></form>');
  w.eval(script);
  const f = tc.querySelector('form[data-ajax]');
  f.dispatchEvent(new w.Event('submit', { bubbles: true, cancelable: true }));
  await tick();
  assert.equal(w.lastRequest.url, 'http://go24.test/u/1');
  assert.equal(f.querySelector('[data-error]').hidden, false);
  assert.equal(f.querySelector('[data-error]').textContent, 'Укажите имя или фамилию');
  assert.equal(f.querySelector('input[name=position]').value, 'Инженер', 'typed data kept');
}

// 6. checklists: «Скрыть выполненные» toggles and is remembered, Enter adds (Shift+Enter is a new line), a double
// click edits an item (Esc gives up), a dragged item is moved on the server to its new place
{
  const w = page([{ status: 200, body: { redirect: '/t/GO-1#checklist' } }]);
  const tc = w.document.querySelector('.tcard');
  tc.insertAdjacentHTML('beforeend', `<div class="card tblock" id="cl2" data-checklist>
    <button type="button" data-ck-hide data-show-text="Показать" data-hide-text="Скрыть"><span>Скрыть</span></button>
    <div class="ck-list" data-list="L1"><div data-ck-items>
      <div class="ck-item done" data-item="i1" draggable="true"><form method="post" action="/t/GO-1/checklist/i1"></form>
        <span class="t" data-ck-edit>Первый</span><form class="ck-edit" hidden><input name="title" value="Первый"></form></div>
      <div class="ck-item" data-item="i2" draggable="true"><form method="post" action="/t/GO-1/checklist/i2"></form><span class="t">Второй</span></div>
    </div><form class="ck-add"><textarea name="title" data-ck-new></textarea></form></div></div>`);
  w.eval(script);
  const cl = tc.querySelector('#cl2');
  const hide = cl.querySelector('[data-ck-hide]');
  hide.click();
  assert.ok(cl.classList.contains('hide-done'));
  assert.equal(hide.textContent, 'Показать');
  assert.equal(w.localStorage.getItem('go24.checklist.hide'), '1');
  hide.click();
  assert.ok(!cl.classList.contains('hide-done'));

  const add = cl.querySelector('textarea[data-ck-new]');
  let sent = 0;
  add.form.addEventListener('submit', e => { e.preventDefault(); sent++; });
  add.value = 'Пункт';
  add.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Enter', shiftKey: true, bubbles: true, cancelable: true }));
  assert.equal(sent, 0);
  add.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }));
  assert.equal(sent, 1);

  const span = cl.querySelector('[data-ck-edit]');
  span.dispatchEvent(new w.MouseEvent('dblclick', { bubbles: true }));
  const edit = cl.querySelector('.ck-edit');
  assert.equal(edit.hidden, false);
  assert.equal(span.hidden, true);
  edit.elements.title.value = 'Другое';
  edit.elements.title.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Escape' }));
  assert.equal(edit.hidden, true);
  assert.equal(edit.elements.title.value, 'Первый');

  const first = cl.querySelector('[data-item="i1"]');
  const drag = (type, target, extra = {}) => {
    const e = new w.Event(type, { bubbles: true, cancelable: true });
    Object.assign(e, { dataTransfer: { setData() {}, effectAllowed: '' }, clientY: 100 }, extra);
    target.dispatchEvent(e);
  };
  drag('dragstart', first);
  drag('dragover', cl.querySelector('[data-ck-items]'));
  assert.equal(cl.querySelector('[data-ck-items]').lastElementChild, first, 'dropped below the second');
  drag('dragend', first);
  await tick();
  assert.equal(w.lastRequest.url, '/t/GO-1/checklist/i1');
  assert.equal(w.lastRequest.body, 'op=move&list=L1&before=');
  assert.ok(!first.classList.contains('dragging'));
}

// 7. the message's menu: reply sends reply_to, edit sends the new text to …/edit, delete removes the message (GO24DEV-771)
{
  const dom = new JSDOM(`<!doctype html><body><main class="content">
    <div class="tcard" data-task="GO-1">
      <aside class="tchat"><div class="msgs" data-msgs>
        <div class="msg" data-id="m1" data-author="Анна"><div class="bubble"><button data-msg-menu>…</button><div class="text">Вопрос</div></div></div>
        <div class="msg mine" data-id="m2" data-author="Я"><div class="bubble"><button data-msg-menu>…</button><div class="text">Мой ответ</div></div></div></div>
        <div class="pop msgpop" data-msgpop hidden>
          <button data-do="reply">r</button><button data-do="copy">c</button>
          <a data-do="subtask" href="/tasks/new?parent=GO-1&comment=">s</a>
          <button data-do="edit" data-own>e</button><button data-do="delete" data-own data-confirm="?">d</button></div>
        <form method="post" action="/t/GO-1/comments" data-chat data-failed="Не удалось">
          <div data-chatctx hidden><b data-ctx-title data-reply="Ответ" data-edit="Изменение"></b><span data-ctx-body></span><button type="button" data-ctx-cancel>x</button></div>
          <input type="hidden" name="reply_to" value=""><textarea name="body"></textarea><button>send</button></form></aside>
    </div></main></body>`, { runScripts: 'outside-only', url: 'http://go24.test/t/GO-1', pretendToBeVisual: true });
  const w = dom.window, d = w.document;
  w.HTMLElement.prototype.scrollIntoView = () => {};
  w.confirm = () => true;
  const sent = [];
  w.fetch = async (url, opts) => {
    sent.push({ url, body: String(opts.body) });
    if (url.endsWith('/delete')) return { ok: true, json: async () => ({ id: 'm2', deleted: true }) };
    const id = url.endsWith('/edit') ? 'm2' : 'm3';
    return { ok: true, json: async () => ({ id, html: `<div class="msg mine" data-id="${id}" data-author="Я"><div class="bubble"><button data-msg-menu>…</button><div class="text">new ${id}</div></div></div>` }) };
  };
  w.eval(script);
  const pop = d.querySelector('[data-msgpop]');
  const menu = id => d.querySelector(`.msg[data-id="${id}"] [data-msg-menu]`).click();
  // someone else's message: no «Изменить»/«Удалить»; the subtask link carries the message
  menu('m1');
  assert.equal(pop.hidden, false);
  assert.equal(pop.querySelector('[data-do=edit]').hidden, true);
  assert.equal(pop.querySelector('[data-do=delete]').hidden, true);
  assert.equal(pop.querySelector('[data-do=subtask]').getAttribute('href'), '/tasks/new?parent=GO-1&comment=m1');
  pop.querySelector('[data-do=reply]').click();
  assert.equal(pop.hidden, true);
  assert.equal(d.querySelector('[data-chatctx]').hidden, false);
  assert.equal(d.querySelector('[data-ctx-title]').textContent, 'Ответ: Анна');
  const text = d.querySelector('textarea');
  text.value = 'Отвечаю';
  d.querySelector('form[data-chat]').requestSubmit();
  await tick();
  assert.equal(sent[0].url, 'http://go24.test/t/GO-1/comments');
  assert.match(sent[0].body, /reply_to=m1/);
  assert.equal(d.querySelector('[data-chatctx]').hidden, true);
  assert.equal(d.querySelector('input[name=reply_to]').value, '');
  // one's own: ↑ in the empty field edits the last own message
  text.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true }));
  assert.equal(text.value, 'new m3');
  text.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
  assert.equal(text.value, '');
  menu('m2');
  assert.equal(pop.querySelector('[data-do=edit]').hidden, false);
  pop.querySelector('[data-do=edit]').click();
  assert.equal(text.value, 'Мой ответ');
  text.value = 'Исправлено';
  d.querySelector('form[data-chat]').requestSubmit();
  await tick();
  assert.equal(sent[1].url, '/t/GO-1/comments/m2/edit');
  assert.match(sent[1].body, /body=%D0%98/);
  assert.equal(d.querySelector('.msg[data-id="m2"] .text').textContent, 'new m2');
  assert.equal(d.querySelectorAll('.msg').length, 3, 'edited in place, nothing added');
  menu('m2');
  pop.querySelector('[data-do=delete]').click();
  await tick();
  assert.equal(sent[2].url, '/t/GO-1/comments/m2/delete');
  assert.equal(d.querySelector('.msg[data-id="m2"]'), null);
}

console.log('task.js: 7 scenarios passed');
