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

// «Мой день» (mailday.js): the button opens the pane from the server and it stays open for the next visit, on its last
// tab; ‹ › loads another day; the box of a task completes it; a letter dropped on a tab opens a meeting or a task of it
{
  const mailday = readFileSync(new URL('../../internal/web/static/js/mailday.js', import.meta.url), 'utf8');
  const store = new Map();
  const asked = [], went = [];
  const paneHTML = d => `<div class="mmyday-head"><button data-myday-tab="cal">Календарь</button><button data-myday-tab="tasks">Задачи</button><button data-myday-close>x</button></div>
    <section data-myday-pane="cal" data-myday-drop="/calendar/new?date=${d}&mail="><button data-myday-day="2026-10-30">›</button><b>${d}</b></section>
    <section data-myday-pane="tasks" data-myday-drop="/tasks/new?mail=" hidden><div class="mmyday-task"><input type="checkbox" data-myday-done="GO-1"><a href="/t/GO-1">Отчёт</a></div>
      <div class="mmyday-task"><input type="checkbox" data-myday-done="GO-2"><a href="/t/GO-2">Счёт</a></div></section>`;
  const open = () => {
    const dom = new JSDOM(`<!doctype html><body><div class="pagehead"><button data-myday-toggle aria-expanded="false">Мой день</button></div>
      <div class="mail3" data-mail><section class="maillist"><div class="mailrow" data-letter="l7" draggable="true">Заказ</div></section></div>
      <aside class="card mmyday" data-myday hidden></aside></body>`, { runScripts: 'outside-only', url: 'http://go24.test/mail' });
    const w = dom.window;
    Object.defineProperty(w, 'localStorage', { value: { getItem: k => store.get(k) ?? null, setItem: (k, v) => store.set(k, String(v)) } });
    w.alert = () => {};
    w.__assign = u => went.push(u);
    w.fetch = async (u, o = {}) => {
      asked.push([String(u), o.body ? String(o.body) : '']);
      if (String(u).startsWith('/mail/myday')) return { ok: true, text: async () => paneHTML(new URL(u, 'http://x').searchParams.get('day') || 'today') };
      if (String(u) === '/t/GO-2/action') return { ok: false, statusText: 'x', json: async () => ({ error: 'Нельзя' }) };
      return { ok: true, json: async () => ({ status: 'completed' }) };
    };
    w.eval(mailday.replace('location.assign(', 'window.__assign('));
    return w;
  };
  const tick = () => new Promise(r => setTimeout(r, 10));
  let w = open();
  let d = w.document, pane = d.querySelector('[data-myday]'), toggle = d.querySelector('[data-myday-toggle]');
  assert.ok(pane.hidden && !asked.length, 'closed at first, nothing asked');
  toggle.click();
  await tick();
  assert.ok(!pane.hidden && toggle.classList.contains('on') && d.body.classList.contains('with-myday'), 'the button opens the pane, the mail makes room');
  assert.equal(asked.at(-1)[0], '/mail/myday');
  assert.ok(pane.querySelector('[data-myday-tab="cal"]').classList.contains('on') && pane.querySelector('[data-myday-pane="tasks"]').hidden, 'the calendar first');
  pane.querySelector('[data-myday-day]').click();
  await tick();
  assert.equal(asked.at(-1)[0], '/mail/myday?day=2026-10-30', '› loads the next day');
  pane.querySelector('[data-myday-tab="tasks"]').click();
  assert.ok(!pane.querySelector('[data-myday-pane="tasks"]').hidden && pane.querySelector('[data-myday-pane="cal"]').hidden, 'the tab «Задачи»');
  const box = pane.querySelector('[data-myday-done="GO-1"]');
  box.checked = true;
  box.dispatchEvent(new w.Event('change', { bubbles: true }));
  await tick();
  assert.deepEqual(asked.at(-1), ['/t/GO-1/action', 'action=complete']);
  assert.ok(box.closest('.mmyday-task').classList.contains('done'), 'the task is done');
  const refused = pane.querySelector('[data-myday-done="GO-2"]');
  refused.checked = true;
  refused.dispatchEvent(new w.Event('change', { bubbles: true }));
  await tick();
  assert.ok(!refused.checked && !refused.disabled && !refused.closest('.mmyday-task').classList.contains('done'), 'a refused one is unticked');
  // a letter dragged over the tab «Календарь» opens it and is dropped there
  const drag = (type, el) => {
    const e = new w.Event(type, { bubbles: true, cancelable: true });
    e.dataTransfer = { dropEffect: '' };
    el.dispatchEvent(e);
    return e;
  };
  drag('dragstart', d.querySelector('.mailrow'));
  const calTab = pane.querySelector('[data-myday-tab="cal"]');
  assert.ok(drag('dragover', calTab).defaultPrevented, 'a tab takes the letter');
  assert.ok(!pane.querySelector('[data-myday-pane="cal"]').hidden && pane.querySelector('[data-myday-pane="cal"]').classList.contains('drop'), 'held over the tab, it opens');
  drag('drop', pane.querySelector('[data-myday-pane="cal"] b'));
  assert.deepEqual(went, ['/calendar/new?date=2026-10-30&mail=l7'], 'a meeting of the letter on the day shown');
  drag('dragend', d.querySelector('.mailrow'));
  assert.ok(!drag('dragover', pane.querySelector('[data-myday-pane="cal"]')).defaultPrevented, 'no letter dragged: nothing taken');
  w.close();
  // the next visit: open on its last tab (the drag opened «Календарь»); a letter onto the tab «Задачи»; × closes it
  w = open();
  await tick();
  d = w.document; pane = d.querySelector('[data-myday]');
  assert.ok(!pane.hidden && !pane.querySelector('[data-myday-pane="cal"]').hidden, 'open again, on its last tab');
  drag('dragstart', d.querySelector('.mailrow'));
  drag('dragover', pane.querySelector('[data-myday-tab="tasks"]'));
  assert.ok(!pane.querySelector('[data-myday-pane="tasks"]').hidden, 'held over «Задачи», it opens');
  drag('drop', pane.querySelector('[data-myday-tab="tasks"]'));
  assert.equal(went.at(-1), '/tasks/new?mail=l7', 'a task of the letter');
  pane.querySelector('[data-myday-close]').click();
  assert.ok(pane.hidden && store.get('mail.myday') === '0', '× closes it');
  w.close();
}

// CoPilot (mailai.js): the button opens the pane on its last conversation and closes «Мой день»; a quick question goes
// with the open letter; «Черновик ответа» waits in the box; Enter sends; «Подтвердить» of a change of letters reloads
// the page, «Отклонить» does not; another conversation from «Разговоры»; «Мой день» closes CoPilot
{
  const mailday = readFileSync(new URL('../../internal/web/static/js/mailday.js', import.meta.url), 'utf8');
  const mailai = readFileSync(new URL('../../internal/web/static/js/mailai.js', import.meta.url), 'utf8');
  const store = new Map([['mail.myday', '1'], ['mail.ai.run', 'r1']]);
  const asked = [];
  let reloads = 0;
  const paneHTML = run => `<div class="mmyday-head"><button data-ai-run="r0">Прошлый</button><button data-ai-close>x</button></div>
    <div class="mai-log" data-ai-log data-run="${run}"><div class="mai-me">Привет</div>
      <div class="mai-act write pending"><button data-ai-decide="a1" data-yes="1" data-reload>Подтвердить</button><button data-ai-decide="a1" data-yes="0">Отклонить</button></div>
      <p data-ai-wait hidden>…</p></div>
    <button data-ai-say="Кратко перескажи" data-ai-letter>Кратко</button><button data-ai-draft="Напиши черновик ответа: ">Черновик</button>
    <form data-ai-form><textarea name="text"></textarea><button>→</button></form>`;
  const dom = new JSDOM(`<!doctype html><body><div class="pagehead"><button data-ai-toggle aria-expanded="false">CoPilot</button><button data-myday-toggle aria-expanded="false">Мой день</button></div>
    <section data-mail-read><article class="mailletter" data-letter="l7">Заказ</article></section>
    <aside class="card mmyday" data-myday hidden></aside><aside class="card mmyday mai" data-ai hidden></aside></body>`, { runScripts: 'outside-only', url: 'http://go24.test/mail' });
  const w = dom.window;
  Object.defineProperty(w, 'localStorage', { value: { getItem: k => store.get(k) ?? null, setItem: (k, v) => store.set(k, String(v)) } });
  w.alert = () => {};
  w.__reload = () => reloads++;
  w.fetch = async (u, o = {}) => {
    u = String(u);
    asked.push([u, o.body ? String(o.body) : '']);
    if (u.startsWith('/mail/myday')) return { ok: true, text: async () => '<button data-myday-close>x</button>' };
    const run = u.startsWith('/mail/copilot?run=') ? u.slice(18) : 'r1';
    return { ok: true, text: async () => paneHTML(run) };
  };
  w.eval(mailday);
  w.eval(mailai.replace('location.reload()', 'window.__reload()'));
  const tick = () => new Promise(r => setTimeout(r, 10));
  const d = w.document, pane = d.querySelector('[data-ai]'), toggle = d.querySelector('[data-ai-toggle]'), day = d.querySelector('[data-myday]');
  await tick();
  assert.ok(!day.hidden && pane.hidden, '«Мой день» was open, CoPilot closed');
  toggle.click();
  await tick();
  assert.ok(!pane.hidden && day.hidden && toggle.classList.contains('on') && d.body.classList.contains('with-myday'), 'CoPilot opens, «Мой день» closes');
  assert.equal(asked.at(-1)[0], '/mail/copilot?run=r1', 'on its last conversation');
  pane.querySelector('[data-ai-say]').click();
  await tick();
  const sent = new URLSearchParams(asked.at(-1)[1]);
  assert.equal(asked.at(-1)[0], '/mail/copilot');
  assert.deepEqual([sent.get('letter'), sent.get('run'), sent.get('text')], ['l7', 'r1', 'Кратко перескажи'], 'a quick question with the open letter');
  const n = asked.length;
  pane.querySelector('[data-ai-draft]').click();
  const box = pane.querySelector('textarea');
  assert.ok(asked.length === n && box.value === 'Напиши черновик ответа: ' && d.activeElement === box, '«Черновик ответа» waits in the box');
  box.value += 'привезём в пятницу';
  box.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Enter', shiftKey: true, bubbles: true }));
  assert.equal(asked.length, n, 'Shift+Enter is a new line');
  w.HTMLFormElement.prototype.requestSubmit ??= function () { this.dispatchEvent(new w.Event('submit', { bubbles: true, cancelable: true })); };
  box.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
  await tick();
  assert.equal(new URLSearchParams(asked.at(-1)[1]).get('text'), 'Напиши черновик ответа: привезём в пятницу', 'Enter sends');
  pane.querySelector('[data-ai-decide][data-yes="0"]').click();
  await tick();
  assert.ok(asked.at(-1)[0] === '/mail/copilot/r1/a1' && new URLSearchParams(asked.at(-1)[1]).get('yes') === '0' && reloads === 0, '«Отклонить»: nothing to reload');
  pane.querySelector('[data-ai-decide][data-yes="1"]').click();
  await tick();
  assert.ok(new URLSearchParams(asked.at(-1)[1]).get('yes') === '1' && reloads === 1, '«Подтвердить» a change of letters reloads the list');
  pane.querySelector('[data-ai-run="r0"]').click();
  await tick();
  assert.ok(asked.at(-1)[0] === '/mail/copilot?run=r0' && store.get('mail.ai.run') === 'r0', 'another conversation, kept for the next visit');
  d.querySelector('[data-myday-toggle]').click();
  await tick();
  assert.ok(pane.hidden && !day.hidden && store.get('mail.ai') === '0', '«Мой день» closes CoPilot');
  w.close();
}

// «Перевести»: the banner asks the server for the translation and shows the letter again translated; an error is told
// and the buttons work again; «Никогда не переводить» and «Показать оригинал» — the letter as it is (tr=0); with
// «Всегда переводить» (data-auto) the banner asks by itself, once
{
  const banner = auto => `<form method="post" action="/mail/letters/l7/translate" class="row mc-banner mtr" data-mtr${auto ? ' data-auto' : ''}>
    <input type="hidden" name="folder" value="in"><input type="hidden" name="lang" value="ru"><input type="hidden" name="from" value="en">
    <button class="small">Перевести на русский</button><button class="ghost small" name="do" value="never" data-mtr-never>Никогда</button>
    <span data-mtr-wait hidden>Переводим…</span></form>`;
  const dom = new JSDOM(`<!doctype html><body><div data-mail data-folder="in"><section data-mail-read><article data-letter="l7">${banner(false)}</article></section></div></body>`,
    { runScripts: 'outside-only', url: 'http://go24.test/mail?folder=in&letter=l7' });
  const w = dom.window, d = w.document;
  w.HTMLFormElement.prototype.requestSubmit ??= function () { this.dispatchEvent(new w.Event('submit', { bubbles: true, cancelable: true })); };
  const asked = [];
  let fail = false;
  w.fetch = async (u, o = {}) => {
    asked.push([String(u), o.body ? new URLSearchParams([...o.body]).toString() : '']);
    if (fail) return { ok: false, statusText: 'x', json: async () => ({ error: 'Нет подключения ИИ' }) };
    if (String(u).startsWith('/mail/letters/l7?')) return { ok: true, text: async () => `<article data-letter="l7"><h2>Заказ коробок</h2><a href="/mail?tr=0" data-mtr-show="0">Показать оригинал</a></article>` };
    return { ok: true, json: async () => ({ lang: 'ru', subject: 'Заказ коробок' }) };
  };
  const shown = [];
  w.go24MailShow = html => { d.querySelector('[data-mail-read]').innerHTML = html; shown.push(html); return true; };
  const tick = () => new Promise(r => setTimeout(r, 0));
  w.eval(mailx);
  const pane = d.querySelector('[data-mail-read]');
  fail = true;
  pane.querySelector('[data-mtr] button').click();
  await tick(); await tick();
  assert.ok(asked[0][0].endsWith('/mail/letters/l7/translate') && /lang=ru/.test(asked[0][1]) && !/do=never/.test(asked[0][1]), 'the banner asks for the translation');
  assert.ok(d.querySelector('.toast')?.textContent.includes('Нет подключения ИИ') && !pane.querySelector('[data-mtr] button').disabled && pane.querySelector('[data-mtr-wait]').hidden, 'an error is told');
  fail = false;
  pane.querySelector('[data-mtr] button').click();
  assert.ok(pane.querySelector('[data-mtr] button').disabled && !pane.querySelector('[data-mtr-wait]').hidden, '«Переводим…» while the AI thinks');
  await tick(); await tick(); await tick();
  assert.equal(asked.at(-1)[0], '/mail/letters/l7?folder=in&letter=l7&tr=ru', 'the letter again, translated');
  assert.ok(pane.innerHTML.includes('Заказ коробок'), 'the translation is shown');
  pane.querySelector('[data-mtr-show]').click();
  await tick(); await tick();
  assert.equal(asked.at(-1)[0], '/mail/letters/l7?folder=in&letter=l7&tr=0', '«Показать оригинал»');
  pane.innerHTML = `<article data-letter="l7">${banner(false)}</article>`;
  pane.querySelector('[data-mtr-never]').click();
  await tick(); await tick(); await tick();
  assert.ok(/do=never/.test(asked.at(-2)[1]) && /from=en/.test(asked.at(-2)[1]) && asked.at(-1)[0].endsWith('&tr=0'), '«Никогда не переводить»: the letter as it is');
  const n = asked.length;
  pane.innerHTML = `<article data-letter="l7">${banner(true)}</article>`;
  await tick(); await tick(); await tick();
  assert.ok(asked[n]?.[0].endsWith('/mail/letters/l7/translate') && asked.at(-1)[0].endsWith('&tr=ru'), '«Всегда переводить» asks by itself');
  assert.equal(asked.filter(a => a[0].endsWith('/mail/letters/l7/translate')).length, 4, 'and only once');
  w.close();
}

// reactions: a chosen one goes to the server and the reactions under the letter are drawn again from its answer (the
// user's own marked, who — in the hint, the picker closed, «Реакция» hidden); an error is told
{
  const dom = new JSDOM(`<!doctype html><body><div data-mail data-folder="in"><form method="post" action="/mail/letters/l7/react" class="row mreacts" data-mreact>
    <input type="hidden" name="folder" value="in"><input type="hidden" name="open" value="l7">
    <details class="mbox-menu" open><summary>+<span data-mreact-label> Реакция</span></summary><div class="mbox-pop">
      <button class="mreact-one" name="kind" value="like">👍</button><button class="mreact-one" name="kind" value="heart">❤️</button></div></details></form></div></body>`,
    { runScripts: 'outside-only', url: 'http://go24.test/mail?folder=in&letter=l7' });
  const w = dom.window, d = w.document;
  const asked = [];
  let answer = { open: true, mine: 'like', list: [{ kind: 'like', emoji: '👍', count: 2, who: ['Анна', 'Ева'] }] };
  w.fetch = async (u, o = {}) => {
    asked.push([String(u), new URLSearchParams([...o.body]).toString()]);
    if (!answer) return { ok: false, statusText: 'x', json: async () => ({ error: 'Только письма коллег' }) };
    return { ok: true, json: async () => answer };
  };
  const tick = () => new Promise(r => setTimeout(r, 0));
  w.eval(mailx);
  const f = d.querySelector('[data-mreact]');
  f.querySelector('.mreact-one[value=like]').click();
  await tick(); await tick();
  assert.ok(asked[0][0].endsWith('/mail/letters/l7/react') && /kind=like/.test(asked[0][1]) && /open=l7/.test(asked[0][1]), 'the reaction goes to the server');
  const b = f.querySelector('.mreact');
  assert.ok(b && b.classList.contains('on') && b.textContent === '👍 2' && b.title === 'Анна, Ева' && b.value === 'like', 'the reaction is drawn with its count and who');
  assert.ok(f.querySelector('.mreact-one[value=like]').classList.contains('on') && !f.querySelector('details').open && f.querySelector('[data-mreact-label]').hidden, 'the picker marks it and closes');
  answer = { open: true, mine: '', list: [] };
  b.click();
  await tick(); await tick();
  assert.ok(/kind=like/.test(asked[1][1]) && !f.querySelector('.mreact') && !f.querySelector('[data-mreact-label]').hidden, 'the own one again takes it away');
  answer = null;
  f.querySelector('.mreact-one[value=heart]').click();
  await tick(); await tick();
  assert.ok(d.querySelector('.toast')?.textContent.includes('Только письма коллег') && !f.querySelector('.mreact-one').disabled, 'an error is told');
  w.close();
}

// «Предлагаемые ответы»: a letter with none kept asks for them once and draws each as a reply link with its text; none
// or an error — the row goes; kept ones are not asked for
{
  const row = (id, ask) => `<div class="row msreply" data-sreply="/mail/letters/${id}/suggest" data-href="/mail?new=1&letter=${id}&mode=reply"${ask ? ' data-ask' : ''}>✨${ask ? '' : '<a class="msreply-a" data-reply="reply" href="#">Ок</a>'}</div>`;
  const dom = new JSDOM(`<!doctype html><body><div data-mail data-folder="in"><article data-letter="a1">${row('a1', true)}</article><article data-letter="a2">${row('a2', false)}</article><div data-more></div></div></body>`,
    { runScripts: 'outside-only', url: 'http://go24.test/mail?folder=in&letter=a1' });
  const w = dom.window, d = w.document;
  const asked = [];
  w.fetch = async u => {
    asked.push(String(u));
    if (String(u).includes('/a3/')) return { ok: true, json: async () => ({ replies: [] }) };
    if (String(u).includes('/a4/')) return { ok: false, statusText: 'x', json: async () => ({ error: 'Нет подключения ИИ' }) };
    return { ok: true, json: async () => ({ replies: ['Да, в четверг', 'Пришлите детали & план'] }) };
  };
  const tick = () => new Promise(r => setTimeout(r, 0));
  w.eval(mailx);
  await tick(); await tick();
  assert.deepEqual(asked, ['/mail/letters/a1/suggest'], 'only the letter with none kept asks, once');
  const r1 = d.querySelector('[data-sreply*=a1]');
  const links = [...r1.querySelectorAll('a.msreply-a')];
  assert.deepEqual(links.map(a => a.textContent), ['Да, в четверг', 'Пришлите детали & план']);
  assert.ok(!r1.hasAttribute('data-ask') && links.every(a => a.dataset.reply === 'reply'), 'shown, each a reply');
  assert.equal(new URL(links[1].href).searchParams.get('say'), 'Пришлите детали & план');
  assert.equal(new URL(links[1].href).searchParams.get('mode'), 'reply');
  // a letter shown later (the pane drawn again): asked by itself; none or an error — no row
  d.querySelector('[data-more]').innerHTML = `<article data-letter="a3">${row('a3', true)}</article><article data-letter="a4">${row('a4', true)}</article>`;
  await tick(); await tick(); await tick();
  assert.deepEqual(asked.slice(1).sort(), ['/mail/letters/a3/suggest', '/mail/letters/a4/suggest']);
  assert.ok(!d.querySelector('[data-sreply*=a3]') && !d.querySelector('[data-sreply*=a4]'), 'no answers — no row');
  assert.ok(!d.querySelector('.toast'), 'quietly');
  w.close();
}

page.close();
other.close();
console.log('mailx.js: new letters, the viewer, the drive picker, the menus in the window passed; mailday.js: «Мой день» passed; mailai.js: CoPilot passed; «Перевести», reactions, «Предлагаемые ответы» passed');
process.exit(0);
