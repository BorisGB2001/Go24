// Writing a letter (static/js/compose.js) in jsdom: the editor takes the server's HTML and the toolbar's commands
// (a size made the size chosen), pasted HTML is cleaned; addresses become chips (a wrong one marked), suggestions
// come as typed and are picked with the keys; the draft is kept a few seconds after a change; Outlook's questions
// before sending (no subject, a file mentioned); «Отправить» leaves «Отменить» for its seconds (kept in
// localStorage), «Отменить» takes the letter back; the signature follows «От»; a reply opens under the letter; the address book (a tab per source, personal contacts kept, changed, deleted); «Подсказки по почте» (MailTips); «Слияние» — the fields and the preview of each recipient's letter.
// node test/js/compose.test.mjs
import { JSDOM, VirtualConsole } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/compose.js', import.meta.url), 'utf8');
const labels = `<template data-compose-labels><span data-l="saved">Сохранён в {t}</span><span data-l="saving">…</span><span data-l="no_rcpt">Нет получателей</span>
  <span data-l="no_subject">Без темы?</span><span data-l="forgot">Забыли вложение?</span><span data-l="words">вложен|attach</span><span data-l="discard">Удалить?</span>
  <span data-l="link">Адрес</span><span data-l="tpl_name">Название</span><span data-l="employee">Сотрудник</span><span data-l="crm">CRM</span><span data-l="history">Переписка</span>
  <span data-l="list">Список</span><span data-l="dept">Отдел</span><span data-l="list_name">Название списка</span><span data-l="list_saved">Список «%s» сохранён</span><span data-l="mention">%s в «Кому»</span>
  <span data-l="contact">Личный контакт</span><span data-l="crm_company">CRM: компания</span><span data-l="book_kept">«%s» — в личных контактах</span><span data-l="book_imported">Добавлено: %d, обновлено: %d, пропущено: %d</span><span data-l="cc">Копия</span>
  <span data-l="book_delete_ask">Удалить «%s»?</span><span data-i="employee"><i class="i i-user"></i></span><span data-i="contact"><i class="i i-notebook"></i></span>
  <span data-i="crm_company"><i class="i i-building-warehouse"></i></span><span data-l="tip_auto">Автоматический ответ от %s</span><span data-l="tip_until">(до %s)</span>
  <span data-l="tip_ext">Получателей вне вашей организации: %s.</span><span data-l="tip_ext_one">вне организации</span><span data-l="tip_large">Письмо получат %s адресатов.</span>
  <span data-l="tip_invalid">Это не адреса:</span><span data-l="tip_nofwd">«Не пересылать» действует только в go24</span></template>`;
const form = (id = '') => `<form class="mcompose inline" method="post" action="/mail/drafts/send" data-compose data-back="/mail?folder=f1"
    data-sigs='{"b1":{"New":"s1","Reply":""},"b2":{"New":"s2","Reply":"s2"}}'>
  <input type="hidden" name="draft" value="${id}"><input type="hidden" name="mode" value=""><input type="hidden" name="reply_to" value=""><input type="hidden" name="importance" value="0">
  <span class="mc-send"><button class="primary">Отправить</button></span>
  <button type="button" data-sig="s1" data-html="&lt;p&gt;Анна&lt;/p&gt;">Основная</button><button type="button" data-sig="s2" data-html="&lt;p&gt;Отдел продаж&lt;/p&gt;">Отдел</button>
  <button type="button" data-imp="1">!</button><button type="button" data-imp="-1">↓</button>
  <span data-status></span>
  <div class="mc-tips" data-tips hidden></div><input type="hidden" name="from" value="a@x.ru" data-from><input type="checkbox" name="no_forward" value="1" data-nofwd>
  <select name="box" data-box><option value="b1" data-from="a@x.ru" selected>a@x.ru</option><option value="b2" data-from="sales@x.ru">sales@x.ru</option></select>
  <div class="mc-rcpt" data-rcpt><input type="text" id="to1" name="to" value='"Клиент" &lt;c@x.ru&gt;, bad'></div>
  <button type="button" data-show="cc">Копия</button><button type="button" data-list-save>Список</button>
  <div class="mc-field" data-field="cc" hidden><button type="button" class="mc-label" data-book="cc">Копия</button><div class="mc-rcpt" data-rcpt><input type="text" name="cc" value=""></div></div>
  <input type="text" name="subject" value="">
  <div class="mc-files" data-files></div>
  <div class="mc-tools" data-tools><select data-cmd="fontSize"><option value=""></option><option value="14pt">14pt</option></select>
    <button type="button" data-cmd="bold">B</button><button type="button" data-cmd="insertOrderedList">1</button>
    <button type="button" data-cmd="superscript">x²</button><button type="button" data-painter aria-pressed="false">кисть</button>
    <details><summary>инт</summary><div><button type="button" data-spacing="1.5">1.5</button></div></details>
    <details><summary>эмодзи</summary><div data-chars="emoji" data-recent="Недавние" data-smileys="Смайлы"></div></details>
    <details><summary>символ</summary><div data-chars="symbols"></div></details>
    <details><summary>дата</summary><div data-dates></div></details></div>
  <div class="mc-editor" contenteditable="true" data-editor></div>
  <textarea name="html" hidden data-html>&lt;p&gt;Добрый день&lt;/p&gt;&lt;div data-signature="s1"&gt;&lt;p&gt;Анна&lt;/p&gt;&lt;/div&gt;&lt;hr&gt;&lt;div data-quote="1"&gt;От: Клиент&lt;/div&gt;&lt;div&gt;см. вложение&lt;/div&gt;</textarea>
  ${labels}
</form>`;
const page = `<!doctype html><body><div data-mail-read><article data-letter="m1"><a data-reply="reply" href="/mail?new=1&letter=m1&folder=f1&mode=reply">r</a>
  <div data-reply-box>${form()}</div></article></div>
  <div data-undo-toast hidden><span data-undo-text></span><button type="button" data-undo-cancel>Отменить</button>
  <template data-undo-labels><span data-l="sending">Отправка…</span><span data-l="sent">Отправлено</span></template></div></body>`;
const wait = ms => new Promise(r => setTimeout(r, ms));

const vc = new VirtualConsole(); // jsdom does not navigate: «not implemented» is expected
vc.on("jsdomError", e => { if (!/navigation/i.test(e.message)) console.error(e); });
const dom = new JSDOM(page, { runScripts: 'outside-only', url: 'http://go24.test/mail?folder=f1', virtualConsole: vc });
const w = dom.window, d = w.document;
w.CSS = { escape: s => s };
const cmds = [];
d.execCommand = (cmd, ui, v) => {
  cmds.push([cmd, v]);
  if (cmd === 'fontSize') { const p = d.querySelector('[data-editor] p'); p.innerHTML = `<font size="7">${p.innerHTML}</font>`; }
  if (cmd === 'insertHTML') d.querySelector('[data-editor]').insertAdjacentHTML('beforeend', v);
  return true;
};
d.queryCommandState = () => false;
w.asked = [];
w.confirm = text => { w.asked.push(text); return w.answer ?? true; };
const asked = [], tipsAsked = [];
w.fetch = async (url, opts = {}) => {
  const u = String(url);
  const json = data => ({ ok: true, status: 200, statusText: 'OK', json: async () => data, text: async () => '' });
  if (u === '/mail/tips') { // apart: it comes 400 ms after the addresses change
    const b = opts.body;
    tipsAsked.push(b);
    const addrs = b.getAll('addr');
    return json({ recipients: addrs.length, large: false, external: addrs.filter(a => a.includes('@y.ru')).map(a => a.replace(/.*</, '').replace('>', '')),
      invalid: [], auto_replies: addrs.some(a => a.includes('anna@go24.test')) ? [{ email: 'anna@go24.test', name: 'Анна Иванова', text: 'В отпуске' }] : [] });
  }
  asked.push({ url: u, method: opts.method || 'GET', body: opts.body instanceof w.URLSearchParams ? opts.body.toString() : opts.body });
  if (u.startsWith('/mail/rcpt?q=sales')) return json([{ name: 'Отдел продаж', email: '', kind: 'dept', members: [{ name: 'Пётр', email: 'p@go24.test' },
    { name: '', email: 'q@go24.test' }] }]);
  if (u.startsWith('/mail/rcpt?people=1')) return json([{ name: 'Борис Б', email: 'boris@go24.test', kind: 'employee' }]);
  if (u === '/mail/lists') return json({ id: 'l1' });
  if (u.startsWith('/mail/book')) return json([{ name: 'Анна Иванова', email: 'anna@go24.test', kind: 'employee' },
    { id: 'k1', name: 'Сидоров', email: 'sid@ip.ru', kind: 'contact', company: 'ИП Сидоров' }, { name: 'ООО Ромашка', email: 'info@romashka.ru', kind: 'crm_company' }]);
  if (u === '/mail/contacts') return json({ id: 'k2', name: opts.body.get('name'), email: opts.body.get('email') });
  if (u === '/mail/contacts/import') return json({ added: 2, updated: 1, skipped: 0 });
  if (u === '/mail/contacts/phone') return json({ url: 'https://go24.test/carddav/' });
  if (u.startsWith('/mail/rcpt')) return json([{ name: 'Анна Иванова', email: 'anna@go24.test', kind: 'employee' }, { name: '', email: 'ann@c.ru', kind: 'history' }]);
  if (u === '/mail/merge/preview') {
    const i = Number(opts.body.get('i'));
    return json({ i, n: 2, of: `${i + 1} из 2`, to: ['Анна <anna@go24.test>', 'Пётр <p@go24.test>'][i], subject: ['Привет, Анна', 'Привет, Пётр'][i], html: '<p>ООО Ромашка</p>' });
  }
  if (u === '/mail/drafts/save') return json({ id: 'd1', saved: '12:03' });
  if (u === '/mail/drafts/send') return json({ id: 'd1', undo: 1 });
  if (u.endsWith('/cancel')) return json({ result: true, href: '/mail?new=1&draft=d1' });
  if (u.startsWith('/mail/compose')) return { ok: true, text: async () => form('d9') };
  return json({ result: true });
};
w.eval(script);
const f = () => d.querySelector('form[data-compose]');
const ed = () => d.querySelector('[data-editor]');
const input = (el, v) => { el.value = v; el.dispatchEvent(new w.Event('input', { bubbles: true })); };
const key = (el, k, o = {}) => { const e = new w.KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true, ...o }); el.dispatchEvent(e); return e; };

{ // the editor and its toolbar
  assert.match(ed().innerHTML, /<p>Добрый день<\/p><div data-signature="s1">/);
  d.querySelector('[data-tools] [data-cmd=bold]').click();
  assert.deepEqual(cmds.filter(c => c[0] === 'bold'), [['bold', undefined]]);
  const size = d.querySelector('[data-tools] [data-cmd=fontSize]');
  size.value = '14pt'; size.dispatchEvent(new w.Event('change', { bubbles: true }));
  assert.match(ed().innerHTML, /<span style="font-size: 14pt;">Добрый день<\/span>/, 'size 7 becomes the size chosen');
  assert.equal(size.value, '', 'the list shows its name again');
  const cleaned = w.go24Clean('<p class="button" id="x" onclick="y()" style="color: red; position: fixed">а<o:p>б</o:p></p><script>z()</script><a href="javascript:x">в</a><img src="http://t/p.png" onerror="x">');
  assert.equal(cleaned, '<p>аб</p><a>в</a><img src="http://t/p.png">', 'pasted HTML is cleaned (a style that places things goes whole)');
}

{ // a picture of the text: a click frames it, a corner resizes it in proportion, an edge one side, Delete removes it
  ed().insertAdjacentHTML('beforeend', '<img src="/files/x" width="100" height="50">');
  const img = ed().querySelector('img');
  img.getBoundingClientRect = () => ({ left: 0, top: 0, width: +img.getAttribute('width'), height: +img.getAttribute('height') });
  img.dispatchEvent(new w.MouseEvent('click', { bubbles: true }));
  const frame = d.querySelector('.mresize');
  assert.ok(frame && !frame.hidden);
  assert.equal(frame.querySelector('.mresize-size').textContent, '100 × 50');
  const drag = (h, dx, dy) => {
    const el = frame.querySelector(`[data-h=${h}]`);
    el.dispatchEvent(new w.MouseEvent('pointerdown', { bubbles: true, cancelable: true, clientX: 0, clientY: 0 }));
    el.dispatchEvent(new w.MouseEvent('pointermove', { bubbles: true, clientX: dx, clientY: dy }));
    el.dispatchEvent(new w.MouseEvent('pointerup', { bubbles: true, clientX: dx, clientY: dy }));
  };
  drag('se', 50, 0);
  assert.deepEqual([img.getAttribute('width'), img.getAttribute('height')], ['150', '75'], 'a corner keeps the proportions');
  drag('e', -30, 0);
  assert.deepEqual([img.getAttribute('width'), img.getAttribute('height')], ['120', '75'], 'an edge changes one side');
  assert.match(f().querySelector('textarea[data-html]').value, /width="120" height="75"/, 'the size goes into the letter');
  key(ed(), 'Delete');
  assert.equal(ed().querySelector('img'), null);
  assert.ok(frame.hidden);
}

{ // the addresses
  const chips = () => [...f().querySelectorAll('[data-rcpt]')[0].querySelectorAll('.mc-chip')];
  assert.deepEqual(chips().map(c => c.firstChild.textContent), ['Клиент', 'bad']);
  assert.ok(chips()[1].classList.contains('bad') && !chips()[0].classList.contains('bad'));
  const to = f().querySelector('input[name=to]');
  assert.equal(to.type, 'hidden');
  const typing = f().querySelector('.mc-rcpt-in');
  assert.equal(typing.id, 'to1', 'the label still points at the field typed in');
  key(typing, 'Backspace');
  assert.equal(to.value, '"Клиент" <c@x.ru>', 'Backspace in the empty field takes the last one off');
  input(typing, 'ann');
  await wait(200);
  assert.equal(asked.at(-1).url, '/mail/rcpt?q=ann');
  const items = f().querySelectorAll('.mc-sugg-item');
  assert.equal(items.length, 2);
  assert.match(items[0].textContent, /Анна Иванова.*anna@go24\.test.*Сотрудник/);
  assert.ok(items[0].dataset.kind === 'employee' && items[0].querySelector('.mc-kind .i-user'), 'the source is marked');
  key(typing, 'ArrowDown');
  assert.ok(items[0].classList.contains('on'));
  assert.ok(key(typing, 'Enter').defaultPrevented);
  assert.equal(to.value, '"Клиент" <c@x.ru>, "Анна Иванова" <anna@go24.test>');
  assert.equal(f().querySelector('.mc-sugg'), null);
  typing.value = 'x@y.ru; "Петров, И." <p@y.ru>';
  typing.dispatchEvent(Object.assign(new w.Event('paste', { bubbles: true, cancelable: true }), { clipboardData: { getData: () => '' } }));
  key(typing, ',');
  assert.equal(to.value, '"Клиент" <c@x.ru>, "Анна Иванова" <anna@go24.test>, x@y.ru, "Петров, И." <p@y.ru>', 'a list typed in is split right');
  f().querySelector('[data-show=cc]').click();
  assert.equal(f().querySelector('[data-field=cc]').hidden, false);
  to.focus();
  assert.ok(f().querySelector('[data-rcpt] .mc-chip[data-kind=employee] .mc-kind .i'), 'a chip keeps the source of its suggestion');
}

{ // the address book: a tab per source, the people picked go to the field; personal contacts added, changed, deleted
  f().querySelector('[data-book=cc]').click();
  const book = d.querySelector('dialog.mc-book');
  assert.ok(book, 'the caption of a field opens the address book');
  await wait(10);
  assert.equal(asked.at(-1).url, '/mail/book?src=&q=');
  const rows = () => [...book.querySelectorAll('.mc-book-row')];
  assert.deepEqual(rows().map(r => r.dataset.kind), ['employee', 'contact', 'crm_company']);
  assert.match(rows()[1].textContent, /Сидоров.*sid@ip\.ru · ИП Сидоров.*Личный контакт/);
  book.querySelector('[data-src=contact]').click();
  await wait(10);
  assert.equal(asked.at(-1).url, '/mail/book?src=contact&q=');
  assert.equal(book.querySelector('[data-src=contact]').getAttribute('aria-selected'), 'true');
  rows()[1].click();
  rows()[2].click();
  assert.equal(rows()[2].getAttribute('aria-selected'), 'true');
  book.querySelector('[data-to=cc]').click();
  const cc = f().querySelector('input[name=cc]');
  assert.equal(cc.value, '"Сидоров" <sid@ip.ru>, "ООО Ромашка" <info@romashka.ru>');
  assert.deepEqual([...f().querySelector('[data-field=cc]').querySelectorAll('.mc-chip')].map(c => c.dataset.kind), ['contact', 'crm_company']);
  assert.ok(f().querySelector('[data-field=cc] .mc-chip[data-kind=crm_company] .i-building-warehouse'));
  assert.equal(rows()[1].getAttribute('aria-selected'), 'false', 'the choice is cleared once added');
  rows()[2].querySelector('button').click(); // «В личные контакты»
  await wait(10);
  const kept = asked.at(-1);
  assert.ok(kept.url === '/mail/contacts' && kept.body.get('email') === 'info@romashka.ru' && kept.body.get('company') === 'ООО Ромашка');
  assert.match(book.querySelector('.mc-book-status').textContent, /«ООО Ромашка» — в личных контактах/);
  book.querySelector('.mc-book-head button').click(); // «Новый контакт»
  const fields = book.querySelectorAll('.mc-book-edit input');
  assert.equal(fields.length, 4);
  fields[0].value = 'Новиков';
  fields[1].value = 'nov@x.ru';
  book.querySelector('.mc-book-edit button.primary').click();
  await wait(10);
  assert.ok(asked.some(a => a.url === '/mail/contacts' && a.body.get('name') === 'Новиков' && a.body.get('email') === 'nov@x.ru'));
  assert.ok(book.querySelector('.mc-book-edit').hidden);
  rows()[1].querySelectorAll('button')[1].click(); // delete Сидоров
  await wait(10);
  assert.ok(w.asked.includes('Удалить «Сидоров»?'));
  w.asked.length = 0;
  assert.ok(asked.some(a => a.url === '/mail/contacts/k1/delete' && a.method === 'POST'));
  key(rows()[0], 'Enter'); // Enter on a row — straight into the field the book was opened for
  assert.match(cc.value, /anna@go24\.test/);
  // the personal contacts' tools, only on their tab: «Импорт» sends the file, «На телефоне» shows the CardDAV address
  const tools = book.querySelector('.mc-book-tools');
  assert.equal(tools.hidden, false);
  assert.equal(tools.querySelector('a').getAttribute('href'), '/mail/contacts/export');
  const file = tools.querySelector('input[type=file]');
  Object.defineProperty(file, 'files', { value: [new w.File(['BEGIN:VCARD'], 'c.vcf')], configurable: true });
  file.dispatchEvent(new w.Event('change'));
  await wait(10);
  assert.ok(asked.some(a => a.url === '/mail/contacts/import' && a.body.get('file').name === 'c.vcf'));
  assert.match(book.querySelector('.mc-book-status').textContent, /Добавлено: 2, обновлено: 1, пропущено: 0/);
  tools.querySelectorAll('button')[1].click(); // «На телефоне»
  await wait(10);
  assert.equal(book.querySelector('.mc-book-edit input').value, 'https://go24.test/carddav/');
  assert.equal(book.querySelector('.mc-book-edit').hidden, false);
  book.querySelector('[data-src=""]').click();
  await wait(10);
  assert.ok(tools.hidden, 'the tools are the personal contacts\' only');
  book.querySelector('.mc-book-foot button:last-child').click(); // «Готово»
  assert.equal(d.querySelector('dialog.mc-book'), null);
  assert.ok(d.activeElement.closest('[data-field=cc]'), 'back to the field');
  f().querySelector('.mc-rcpt-in').focus(); // back to «Кому»
}

{ // a contact list or a department turns into its people; «Сохранить адресатов как список»; @mentions
  const to = f().querySelector('input[name=to]');
  const typing = f().querySelector('.mc-rcpt-in');
  input(typing, 'sales');
  await wait(200);
  const item = f().querySelector('.mc-sugg-item');
  assert.match(item.textContent, /Отдел продаж.*Пётр, q@go24\.test.*Отдел/);
  item.dispatchEvent(new w.MouseEvent('mousedown', { bubbles: true, cancelable: true }));
  assert.match(to.value, /, "Пётр" <p@go24\.test>, q@go24\.test$/, 'the department is its people');
  w.prompt = () => 'Команда';
  f().querySelector('[data-list-save]').click();
  await wait(20);
  const saved = asked.findLast(a => a.url === '/mail/lists');
  assert.ok(saved && new w.URLSearchParams(saved.body).get('members').split('\n').includes('q@go24.test'), 'To and Cc become a list');
  assert.equal(new w.URLSearchParams(saved.body).get('name'), 'Команда');
  assert.equal(f().querySelector('[data-status]').textContent, 'Список «Команда» сохранён');

  const p = d.createElement('p');
  p.textContent = 'Прошу @бор';
  ed().append(p);
  const sel = w.getSelection();
  sel.removeAllRanges();
  const r = d.createRange();
  r.setStart(p.firstChild, p.firstChild.length);
  sel.addRange(r);
  ed().dispatchEvent(new w.Event('input', { bubbles: true }));
  await wait(200);
  assert.ok(asked.some(a => a.url === '/mail/rcpt?people=1&q=%D0%B1%D0%BE%D1%80'), 'only people are asked for');
  const pop = d.querySelector('.mc-mention');
  assert.ok(pop && pop.querySelector('.mc-sugg-item.on'));
  assert.ok(key(ed(), 'Enter').defaultPrevented, 'Enter picks, no new line');
  assert.equal(d.querySelector('.mc-mention'), null);
  assert.equal(p.querySelector('a[data-mention]').getAttribute('href'), 'mailto:boris@go24.test');
  assert.match(p.textContent, /^Прошу @Борис Б\u00a0$/);
  assert.match(to.value, /"Борис Б" <boris@go24\.test>$/, 'the one mentioned goes to «Кому»');
  const before = to.value;
  const p2 = d.createElement('p');
  p2.textContent = '@бор';
  ed().append(p2);
  r.setStart(p2.firstChild, 4);
  sel.removeAllRanges(); sel.addRange(r);
  ed().dispatchEvent(new w.Event('input', { bubbles: true }));
  await wait(200);
  key(ed(), 'Enter');
  assert.equal(to.value, before, 'one addressed already is not added twice');
}

{ // importance, the signature with «От»
  f().querySelector('[data-imp="1"]').click();
  assert.equal(f().querySelector('input[name=importance]').value, '1');
  f().querySelector('[data-imp="1"]').click();
  assert.equal(f().querySelector('input[name=importance]').value, '0', 'pressed again — normal');
  const box = f().querySelector('select[data-box]');
  box.value = 'b2'; box.dispatchEvent(new w.Event('change', { bubbles: true }));
  assert.match(ed().innerHTML, /<div data-signature="s2"><p>Отдел продаж<\/p><\/div><hr>/, 'the other mailbox\'s signature, above the quote');
  f().querySelector('[data-sig=""]')?.click();
}

{ // «Подсказки по почте»: asked a moment after the addresses change; a colleague's automatic reply, the ones outside marked
  await wait(500);
  const t = tipsAsked.at(-1);
  assert.ok(t && t.get('from') === 'sales@x.ru' && t.getAll('addr').includes('"Анна Иванова" <anna@go24.test>'));
  const box = f().querySelector('[data-tips]');
  assert.equal(box.hidden, false);
  assert.match(box.textContent, /Автоматический ответ от Анна Иванова: В отпуске/);
  assert.match(box.textContent, /Получателей вне вашей организации: 2\. x@y\.ru, p@y\.ru/);
  assert.deepEqual([...f().querySelectorAll('.mc-chip.ext')].map(c => c.firstChild.textContent), ['x@y.ru', 'Петров, И.']);
  const n = tipsAsked.length;
  await wait(500);
  assert.equal(tipsAsked.length, n, 'the same addresses are not asked of again');
  // «Не пересылать» with people outside: it does not hold for them
  assert.doesNotMatch(box.textContent, /действует только в go24/);
  const nofwd = f().querySelector('input[data-nofwd]');
  nofwd.checked = true; nofwd.dispatchEvent(new w.Event('change', { bubbles: true }));
  assert.match(box.textContent, /«Не пересылать» действует только в go24/);
  nofwd.checked = false; nofwd.dispatchEvent(new w.Event('change', { bubbles: true }));
  assert.doesNotMatch(box.textContent, /действует только в go24/);
  assert.equal(tipsAsked.length, n, 'the option needs no new tips');
}

{ // the draft is kept a few seconds after a change
  await wait(3200);
  const save = asked.filter(a => a.url === '/mail/drafts/save');
  assert.equal(save.length, 1);
  assert.match(save[0].body, /to=.*anna%40go24\.test/);
  assert.match(save[0].body, /html=.*%D0%9E%D1%82%D0%B4%D0%B5%D0%BB/, 'the editor\'s text goes along');
  assert.equal(f().querySelector('input[name=draft]').value, 'd1');
  assert.equal(f().querySelector('[data-status]').textContent, 'Сохранён в 12:03');
}

{ // sending: Outlook's questions, then «Отменить» for its seconds
  w.answer = false;
  f().requestSubmit();
  await wait(20);
  assert.deepEqual(w.asked, ['Без темы?'], 'no subject: asked, and not sent');
  assert.ok(!asked.some(a => a.url === '/mail/drafts/send'));
  f().querySelector('input[name=subject]').value = 'Счёт';
  ed().insertAdjacentHTML('afterbegin', '<p>Счёт во вложении</p>');
  f().requestSubmit();
  await wait(20);
  assert.equal(w.asked.at(-1), 'Забыли вложение?', 'a file mentioned in the own text, none attached');
  ed().querySelector('p').remove();
  w.answer = true;
  f().requestSubmit();
  await wait(50);
  const send = asked.find(a => a.url === '/mail/drafts/send');
  assert.ok(send, 'sent');
  assert.match(send.body, /draft=d1/);
  assert.match(send.body, /subject=%D0%A1%D1%87%D1%91%D1%82/);
  assert.equal(d.querySelector('form[data-compose]'), null, 'the reply under the letter goes away');
  const toast = d.querySelector('[data-undo-toast]');
  assert.equal(toast.hidden, false);
  assert.equal(toast.querySelector('[data-undo-text]').textContent, 'Отправка…');
  assert.equal(JSON.parse(w.localStorage.getItem('go24-mail-undo')).id, 'd1');
  toast.querySelector('[data-undo-cancel]').click();
  await wait(20);
  assert.equal(asked.at(-1).url, '/mail/drafts/d1/cancel', '«Отменить» calls the letter back');
  assert.equal(w.localStorage.getItem('go24-mail-undo'), null);
}

{ // a reply opens under the letter
  d.querySelector('a[data-reply]').dispatchEvent(new w.MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }));
  await wait(20);
  assert.equal(asked.at(-1).url, '/mail/compose?letter=m1&mode=reply&folder=f1&inline=1');
  const fresh = d.querySelector('[data-reply-box] form[data-compose]');
  assert.ok(fresh && fresh.dataset.ready, 'the editor is set up');
  assert.equal(fresh.querySelector('input[name=draft]').value, 'd9');
  // a suggested answer (web.mailRepliesData) opens the reply with its text
  const sug = d.createElement('a');
  sug.dataset.reply = 'reply';
  sug.href = '/mail?new=1&letter=m1&folder=f1&mode=reply&say=' + encodeURIComponent('Да, подходит');
  d.querySelector('article').prepend(sug);
  sug.dispatchEvent(new w.MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }));
  await wait(20);
  assert.equal(asked.at(-1).url, '/mail/compose?letter=m1&mode=reply&folder=f1&inline=1&say=%D0%94%D0%B0%2C+%D0%BF%D0%BE%D0%B4%D1%85%D0%BE%D0%B4%D0%B8%D1%82');
  sug.remove();
  // an old toast left in another tab: shown for what is left of it, then gone
  w.localStorage.setItem('go24-mail-undo', JSON.stringify({ id: 'd2', until: Date.now() + 60 }));
  w.dispatchEvent(new w.StorageEvent('storage', { key: 'go24-mail-undo' }));
  assert.equal(d.querySelector('[data-undo-toast]').hidden, false);
  await wait(120);
  assert.equal(d.querySelector('[data-undo-text]').textContent, 'Отправлено');
}
{ // «Вставка» and «Формат текста», as Outlook's: superscript, Ctrl+= / Ctrl+Shift+= / Ctrl+J, smileys (the recent first),
  // symbols, the date, the line spacing of the paragraphs selected, «Формат по образцу»
  const form = d.querySelector('[data-reply-box] form[data-compose]'), ed = form.querySelector('[data-editor]');
  const tools = form.querySelector('[data-tools]');
  const select = (node, from, to) => {
    const r = d.createRange();
    r.setStart(node, from);
    r.setEnd(node, to);
    w.getSelection().removeAllRanges();
    w.getSelection().addRange(r);
    d.dispatchEvent(new w.Event('selectionchange'));
  };
  const open = sel => { const det = tools.querySelector(sel).closest('details'); det.open = true; det.dispatchEvent(new w.Event('toggle')); return det; };
  cmds.length = 0;
  tools.querySelector('[data-cmd=superscript]').click();
  key(ed, '=', { ctrlKey: true, code: 'Equal' });
  key(ed, '+', { ctrlKey: true, shiftKey: true, code: 'Equal' });
  key(ed, 'j', { ctrlKey: true });
  assert.deepEqual(cmds.filter(c => c[0] !== 'styleWithCSS').map(c => c[0]), ['superscript', 'subscript', 'superscript', 'justifyFull']);

  ed.innerHTML = '<p>Первый абзац</p><p>Второй</p>';
  let det = open('[data-chars=emoji]');
  assert.equal(det.querySelector('b').textContent, 'Смайлы', 'nothing recent yet');
  const pick = (el, c) => [...el.querySelectorAll('[data-char]')].find(b => b.dataset.char === c).click(); // jsdom's selectors miss smileys
  pick(det, '👍');
  assert.match(ed.innerHTML, /👍/);
  assert.equal(det.open, false);
  det = open('[data-chars=emoji]');
  assert.equal(det.querySelector('b').textContent, 'Недавние');
  assert.equal(det.querySelector('.mc-chars-row [data-char]').dataset.char, '👍');
  pick(open('[data-chars=symbols]'), '©');
  assert.match(ed.innerHTML, /©/);
  det = open('[data-dates]');
  const days = [...det.querySelectorAll('[data-date]')];
  assert.equal(days.length, 6);
  assert.ok(days.some(b => b.dataset.date.includes(String(new Date().getFullYear()))));
  days[0].click();
  assert.ok(ed.innerHTML.includes(days[0].dataset.date.replace(/&/g, '&amp;')));

  ed.innerHTML = '<p>Первый абзац</p><p>Второй</p><p>Третий</p>';
  const ps = ed.querySelectorAll('p');
  const r = d.createRange();
  r.setStart(ps[0].firstChild, 2);
  r.setEnd(ps[1].firstChild, 3);
  w.getSelection().removeAllRanges();
  w.getSelection().addRange(r);
  d.dispatchEvent(new w.Event('selectionchange'));
  tools.querySelector('[data-spacing="1.5"]').click();
  assert.deepEqual([...ps].map(p => p.style.lineHeight), ['1.5', '1.5', ''], 'the paragraphs touched');

  ed.innerHTML = '<p><span style="color: red; font-weight: bold">образец</span> и <i>цель</i> дальше</p>';
  select(ed.querySelector('span').firstChild, 1, 3);
  const brush = tools.querySelector('[data-painter]');
  brush.click();
  assert.equal(brush.getAttribute('aria-pressed'), 'true');
  select(ed.querySelector('i').firstChild, 0, 4);
  ed.dispatchEvent(new w.MouseEvent('mouseup', { bubbles: true }));
  await wait(5);
  const painted = ed.querySelectorAll('span')[1];
  assert.equal(painted.textContent, 'цель');
  assert.match(painted.getAttribute('style'), /color: (red|rgb\(255, 0, 0\))/);
  assert.match(painted.getAttribute('style'), /font-weight: (bold|700)/);
  assert.equal(ed.querySelector('i')?.textContent || '', '', 'its own italics gave way');
  assert.equal(brush.getAttribute('aria-pressed'), 'false', 'the brush is put down');
  brush.click();
  assert.equal(brush.getAttribute('aria-pressed'), 'true');
  key(ed, 'Escape');
  assert.equal(brush.getAttribute('aria-pressed'), 'false', 'Esc puts it down');
}
{ // «Слияние»: the option shows the bar; a field goes where the caret was — the subject or the text; «Просмотр» asks
  // the server for each recipient's letter in turn
  const box = d.createElement('div');
  box.innerHTML = form('d7').replace('<input type="text" name="subject" value="">', `<input type="text" name="subject" value="Привет, ">
    <input type="checkbox" name="merge" value="1" data-merge-on>
    <div data-merge-bar hidden><details open><summary>Поле</summary><div><button type="button" data-merge-field="Имя">Имя</button>
      <button type="button" data-merge-field="Компания">Компания</button></div></details><button type="button" data-merge-preview>Просмотр</button></div>
    <dialog data-merge-dlg><button type="button" data-merge-step="-1">‹</button><span data-merge-of></span><button type="button" data-merge-step="1">›</button>
      <button type="button" data-merge-close>×</button><span data-merge-to></span><b data-merge-subject></b><iframe data-merge-body></iframe></dialog>`);
  d.body.append(box);
  w.go24Compose(box);
  const mf = box.querySelector('form[data-compose]');
  const bar = mf.querySelector('[data-merge-bar]'), on = mf.querySelector('[data-merge-on]');
  on.checked = true;
  on.dispatchEvent(new w.Event('change', { bubbles: true }));
  assert.equal(bar.hidden, false, 'the bar shows with the option');
  const subj = mf.querySelector('input[name=subject]');
  subj.dispatchEvent(new w.FocusEvent('focusin', { bubbles: true }));
  subj.setSelectionRange(8, 8);
  bar.querySelector('[data-merge-field="Имя"]').click();
  assert.equal(subj.value, 'Привет, {Имя}', 'into the subject at the caret');
  assert.equal(bar.querySelector('details').open, false, 'the menu closes');
  const med = mf.querySelector('[data-editor]');
  med.dispatchEvent(new w.FocusEvent('focusin', { bubbles: true }));
  bar.querySelector('[data-merge-field="Компания"]').click();
  assert.deepEqual(cmds.at(-1), ['insertHTML', '{Компания}'], 'into the text');
  const dlg = mf.querySelector('[data-merge-dlg]');
  dlg.showModal = function () { this.open = true; };
  dlg.close = function () { this.open = false; };
  const n = asked.length;
  mf.querySelector('[data-merge-preview]').click();
  await wait(20);
  const pv = asked.slice(n).find(a => a.url === '/mail/merge/preview');
  assert.ok(pv, 'asked for the preview');
  assert.match(pv.body, /i=0/);
  assert.match(pv.body, /subject=%D0%9F%D1%80%D0%B8%D0%B2%D0%B5%D1%82%2C\+%7B%D0%98%D0%BC%D1%8F%7D/);
  assert.equal(dlg.open, true);
  assert.equal(dlg.querySelector('[data-merge-of]').textContent, '1 из 2');
  assert.equal(dlg.querySelector('[data-merge-subject]').textContent, 'Привет, Анна');
  assert.match(dlg.querySelector('[data-merge-body]').srcdoc, /<p>ООО Ромашка<\/p>/);
  dlg.querySelector('[data-merge-step="1"]').click();
  await wait(20);
  assert.match(asked.at(-1).body, /i=1/, 'the next recipient');
  assert.equal(dlg.querySelector('[data-merge-of]').textContent, '2 из 2');
  dlg.querySelector('[data-merge-close]').click();
  assert.equal(dlg.open, false);
  box.remove();
}
console.log('compose.test.mjs: ok');
process.exit(0);
