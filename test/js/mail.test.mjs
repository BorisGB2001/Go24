// The mail page (static/js/mail.js) in jsdom: a conversation opens in the reading pane and stops being unread (the
// counters follow by its unread letters), the reading pane moves (cookie mail_pane), a conversation dropped on a
// folder of its mailbox moves there with all its letters; ticked letters show the toolbar; Outlook's hot keys
// act on the ticked letters or on the open one; with the pane off a letter opens as a page; «Избранное» folds (kept
// in the browser), its places are reordered by dragging, its folders' counters go with the folders'. node test/js/mail.test.mjs
import { JSDOM, VirtualConsole } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/mail.js', import.meta.url), 'utf8');
// the mail page runs mailx.js too (its menus in the window listen to the whole document): a letter is chosen with it
const extra = readFileSync(new URL('../../internal/web/static/js/mailx.js', import.meta.url), 'utf8');
const page = pane => `<!doctype html><body><div class="pagehead"><a class="button create" href="/mail?new=1">new</a></div>
  <button data-pane="right">r</button><button data-pane="bottom">b</button><button data-pane="off">o</button>
  <div class="mail3 pane-${pane}" data-mail data-folder="f1" style="--mail-nav: 260px; --mail-list: 380px; --mail-listh: 320px">
    <div class="msplit" data-split="nav"></div><div class="msplit" data-split="list"></div>
    <nav><details class="mbox mfavs" data-favs open><summary>Избранное</summary>
      <div class="mfolder-row" draggable="true" data-fav="f1"><a class="mfolder" href="/mail?folder=f1" data-folder-id="f1" data-box="b1"><i class="i i-inbox"></i><span class="cnt" data-unseen>3</span></a></div>
      <div class="mfolder-row" draggable="true" data-fav="f2"><a class="mfolder" href="/mail?folder=f2" data-folder-id="f2" data-box="b1"><i class="i i-folder"></i></a></div>
      <div class="mfolder-row" draggable="true" data-fav="flagged"><a class="mfolder" href="/mail?folder=flagged">С флажком</a></div></details>
      <a class="mfolder" href="/mail?folder=all"><span>Все</span><span class="cnt" data-unseen>4</span></a>
      <a class="mfolder" href="/mail?folder=f1" data-folder-id="f1" data-box="b1"><i class="i i-inbox"></i><span class="cnt" data-unseen>3</span></a>
      <a class="mfolder" href="/mail?folder=f2" data-folder-id="f2" data-box="b1"><i class="i i-folder"></i></a>
      <a class="mfolder" href="/mail?folder=f9" data-folder-id="f9" data-box="b2"><i class="i i-folder"></i></a></nav>
    <section class="maillist">
      <form method="post" action="/mail/act" id="mailact" data-mail-toolbar><input type="checkbox" class="msel-all"><span data-sel-count></span>
        <button name="do" value="delete" data-key="delete">d</button><button name="do" value="archive" data-key="archive">a</button>
        <button name="do" value="read" data-key="read" title="Прочитано (Q)">r</button>
        <details class="mact-menu"><summary title="Флажок">f</summary><div class="mbox-pop"><button name="do" value="flag:today">Сегодня</button>
          <div class="row"><input type="date" name="arg_flag"><button name="do" value="flag">ok</button></div></div></details>
        <button name="do" value="sweep" title="Очистка">s<span class="mlabel"> Очистка</span></button><button name="do" value="rule" title="Создать правило…">p</button></form>
      <div class="mailrow unread" data-letter="m1" data-ids="m0,m1" data-unread="2" data-box="b1" draggable="true">
        <input type="checkbox" class="msel" name="letter" value="m0,m1" form="mailact"><a class="mrow-link" href="/mail?folder=f1&letter=m1">Заказ</a></div>
      <div class="mailrow unread" data-letter="m2" data-ids="m2" data-unread="1" data-box="b1" draggable="true">
        <input type="checkbox" class="msel" name="letter" value="m2" form="mailact"><span class="ava">СЧ</span><a class="mrow-link" href="/mail?folder=f1&letter=m2">Счёт</a>
        <span class="mmarks"><button type="button" class="mclip" data-clip="m2">clip</button></span></div>
      <template data-clip-labels><span data-l="open">Открыть</span><span data-l="download">Скачать</span><span data-l="none">Нет</span><span data-l="del1">Удалить письмо?</span><span data-l="deln">Удалить {n}?</span><span data-l="gone1">Навсегда?</span><span data-l="gonen">Навсегда {n}?</span></template></section>
    <section data-mail-read></section>
  </div></body>`;
const wait = ms => new Promise(r => setTimeout(r, ms));
const setup = (pane, before) => {
  const dom = new JSDOM(page(pane), { runScripts: 'outside-only', url: 'http://go24.test/mail?folder=f1' });
  const w = dom.window;
  w.CSS = { escape: s => s };
  w.asked = [];
  w.confirm = text => { w.asked.push(text); return w.answer ?? true; };
  w.matchMedia = () => ({ matches: false });
  const asked = [];
  w.fetch = async (url, opts = {}) => {
    asked.push({ url: String(url), method: opts.method || 'GET', body: String(opts.body || '') });
    if (String(url).startsWith('/mail/letters/')) return { ok: true, text: async () => `<article data-letter="${String(url).slice(14, 16)}"><h2>Заказ</h2>
      <a data-key="r" href="/mail?new=1&mode=reply">r</a><a data-key="all" href="/mail?new=1&mode=all">a</a><a data-key="fwd" href="/mail?new=1&mode=fwd">f</a>
      <form method="post" action="/mail/act" data-mail-act><input type="hidden" name="letter" value="m0,m1">
        <button name="do" value="delete" data-key="delete">d</button><button name="do" value="flag:none" data-key="flag">f</button></form></article>` };
    if (String(url).startsWith('/mail/files')) return { ok: true, json: async () => [{ id: 'f1', name: 'счёт.pdf', size_text: '20 КБ', image: false }, { id: 'f2', name: 'фото.png', size_text: '1,5 МБ', image: true }] };
    return { ok: true, json: async () => ({ result: true }) };
  };
  before?.(w);
  w.eval(script);
  w.eval(extra);
  return { w, d: w.document, asked };
};
const unseen = (d, href) => d.querySelector(`.mfolder[href="${href}"] [data-unseen]`)?.textContent ?? '';

{ // the reading pane
  const { w, d, asked } = setup('right');
  const row = d.querySelector('.mailrow[data-letter=m1]');
  row.querySelector('a').dispatchEvent(new w.MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }));
  await wait(10);
  assert.equal(asked[0].url, '/mail/letters/m1?folder=f1');
  assert.match(d.querySelector('[data-mail-read]').innerHTML, /Заказ/);
  assert.ok(!row.classList.contains('unread') && row.classList.contains('on'));
  assert.equal(unseen(d, '/mail?folder=f1'), '1', 'both unread letters of the conversation are read');
  assert.equal(unseen(d, '/mail?folder=all'), '2', 'an inbox letter counts in «Все входящие» too');

  // hot keys: the open letter's reply links and actions; nothing while typing
  const submitted = [];
  w.HTMLFormElement.prototype.requestSubmit = function (b) { submitted.push([this.action.replace('http://go24.test', ''), b?.value, new w.FormData(this, b).getAll('letter').join('|')]); };
  const clicked = [];
  w.HTMLAnchorElement.prototype.click = function () { clicked.push(this.getAttribute('href')); };
  const key = (k, o = {}, el = d.body) => { const e = new w.KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true, ...o }); el.dispatchEvent(e); return e; };
  key('r'); key('R', { ctrlKey: true, shiftKey: true }); key('f'); key('n');
  assert.deepEqual(clicked, ['/mail?new=1&mode=reply', '/mail?new=1&mode=all', '/mail?new=1&mode=fwd', '/mail?new=1']);
  assert.ok(key('Delete').defaultPrevented);
  key('Insert');
  assert.deepEqual(submitted, [['/mail/act', 'delete', 'm0,m1'], ['/mail/act', 'flag:none', 'm0,m1']]);
  const search = d.createElement('input'); d.body.append(search);
  assert.equal(key('Delete', {}, search).defaultPrevented, false, 'typing is not a hot key');
  // ticked letters: the toolbar shows and the keys act on them
  const boxes = d.querySelectorAll('.msel');
  boxes[1].checked = true; boxes[1].dispatchEvent(new w.Event('change', { bubbles: true }));
  assert.ok(d.querySelector('#mailact').classList.contains('has-sel'));
  assert.equal(d.querySelector('[data-sel-count]').textContent, '1');
  assert.ok(d.querySelector('.msel-all').indeterminate);
  key('e');
  assert.deepEqual(submitted.at(-1), ['/mail/act', 'archive', 'm2']);
  const all = d.querySelector('.msel-all'); all.checked = true; all.dispatchEvent(new w.Event('change', { bubbles: true }));
  key('q');
  assert.deepEqual(submitted.at(-1), ['/mail/act', 'read', 'm0,m1|m2'], 'a conversation ticked is all its letters');
  all.checked = false; all.dispatchEvent(new w.Event('change', { bubbles: true }));
  assert.ok(!d.querySelector('#mailact').classList.contains('has-sel'));
  // Ctrl+click ticks a letter instead of opening it, Shift+click ticks the letters between
  const n0 = asked.length;
  d.querySelector('.mailrow[data-letter=m2] a').dispatchEvent(new w.MouseEvent('click', { bubbles: true, cancelable: true, button: 0, ctrlKey: true }));
  assert.equal(asked.length, n0, 'Ctrl+click opens nothing');
  assert.ok(boxes[1].checked && !boxes[0].checked);
  d.querySelector('.mailrow[data-letter=m1] a').dispatchEvent(new w.MouseEvent('click', { bubbles: true, cancelable: true, button: 0, shiftKey: true }));
  assert.ok(boxes[0].checked && boxes[1].checked, 'Shift+click ticks the range');
  assert.equal(d.querySelector('[data-sel-count]').textContent, '2');
  all.checked = false; all.dispatchEvent(new w.Event('change', { bubbles: true }));
  // the arrow opens the next letter
  key('ArrowDown');
  await wait(10);
  assert.equal(asked.at(-1).url, '/mail/letters/m2?folder=f1');
  assert.ok(d.querySelector('.mailrow[data-letter=m2]').classList.contains('on'));
  assert.equal(w.location.search, '?folder=f1&letter=m2');

  // the pane moves down: a cookie for the server and the class for now
  d.querySelector('[data-pane=bottom]').click();
  assert.match(d.cookie, /mail_pane=bottom/);
  assert.ok(d.querySelector('[data-mail]').classList.contains('pane-bottom'));
  assert.ok(d.querySelector('[data-pane=bottom]').classList.contains('on'));

  // drag and drop onto a folder of the same mailbox
  const m2 = d.querySelector('.mailrow[data-letter=m1]');
  const dt = { setData() {}, effectAllowed: '', dropEffect: '' };
  const drag = (type, el) => { const e = new w.Event(type, { bubbles: true, cancelable: true }); e.dataTransfer = dt; el.dispatchEvent(e); return e; };
  drag('dragstart', m2);
  assert.equal(drag('dragover', d.querySelector('[data-folder-id=f9]')).defaultPrevented, false, 'another mailbox takes no letters');
  assert.equal(drag('dragover', d.querySelector('[data-folder-id=f1]')).defaultPrevented, false, 'nor its own folder');
  assert.equal(drag('dragover', d.querySelector('[data-folder-id=f2]')).defaultPrevented, true);
  drag('drop', d.querySelector('[data-folder-id=f2]'));
  await wait(10);
  const move = asked.find(a => a.url === '/mail/move');
  assert.equal(move.method, 'POST');
  assert.equal(move.body, 'letter=m0%2Cm1&folder=f2', 'a conversation moves with all its letters');
  assert.equal(d.querySelector('.mailrow[data-letter=m1]'), null, 'the moved conversation leaves the list');
  assert.equal(unseen(d, '/mail?folder=f2'), '', 'it was read');
}

{ // dragging a ticked letter takes all the ticked ones along
  const { w, d, asked } = setup('right');
  d.querySelectorAll('.msel').forEach(b => { b.checked = true; });
  const dt = { setData() {}, effectAllowed: '', dropEffect: '' };
  const drag = (type, el) => { const e = new w.Event(type, { bubbles: true, cancelable: true }); e.dataTransfer = dt; el.dispatchEvent(e); };
  drag('dragstart', d.querySelector('.mailrow[data-letter=m2]'));
  drag('drop', d.querySelector('[data-folder-id=f2]'));
  await wait(10);
  assert.equal(asked.find(a => a.url === '/mail/move').body, 'letter=m0%2Cm1&letter=m2&folder=f2');
  assert.equal(d.querySelectorAll('.mailrow').length, 0);
  assert.equal(unseen(d, '/mail?folder=f2'), '3');
}

{ // the borders between the panes are dragged; the sizes are kept, within bounds; double click — the usual
  const { w, d } = setup('right');
  const root = d.querySelector('[data-mail]');
  const ev = (type, el, x) => el.dispatchEvent(new w.MouseEvent(type, { bubbles: true, cancelable: true, clientX: x }));
  const nav = d.querySelector('[data-split=nav]');
  ev('pointerdown', nav, 100); ev('pointermove', nav, 150); ev('pointerup', nav, 150);
  assert.equal(root.style.getPropertyValue('--mail-nav'), '310px');
  assert.match(d.cookie, /mail_cols=310\.380\.320/);
  const lst = d.querySelector('[data-split=list]');
  ev('pointerdown', lst, 500); ev('pointermove', lst, 5000); ev('pointerup', lst, 5000);
  assert.match(d.cookie, /mail_cols=310\.900\.320/, 'no wider than the bound');
  nav.dispatchEvent(new w.MouseEvent('dblclick', { bubbles: true }));
  assert.match(d.cookie, /mail_cols=250\.900\.320/);
}

{ // the paper clip: a click shows the files with «Открыть» (pictures) and «Скачать»; the letter does not open
  const { w, d, asked } = setup('right');
  d.querySelector('.mclip').dispatchEvent(new w.MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }));
  await wait(10);
  assert.equal(asked.length, 1);
  assert.equal(asked[0].url, '/mail/files?letter=m2');
  const pop = d.querySelector('.mclip-pop');
  assert.ok(pop);
  const links = [...pop.querySelectorAll('a')].map(a => a.textContent + ' ' + a.getAttribute('href'));
  assert.deepEqual(links, ['Скачать /files/f1', 'Открыть /files/f2?inline=1', 'Скачать /files/f2']);
  assert.match(pop.textContent, /счёт\.pdf.*20 КБ/);
  d.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Escape' }));
  assert.equal(d.querySelector('.mclip-pop'), null);
}

{ // the row's quick actions: delete takes the letter off the list; read reads it
  const { w, d, asked } = setup('right');
  const m2 = d.querySelector('.mailrow[data-letter=m2]');
  m2.insertAdjacentHTML('beforeend', '<span class="mquick"><button type="button" data-do="read">r</button><button type="button" data-do="delete">d</button></span>');
  m2.querySelector('[data-do=read]').dispatchEvent(new w.MouseEvent('click', { bubbles: true, cancelable: true }));
  await wait(10);
  assert.match(asked.at(-1).body, /letter=m2&do=read/);
  assert.ok(!m2.classList.contains('unread'));
  assert.equal(unseen(d, '/mail?folder=f1'), '2');
  w.answer = false; // «no»: nothing is deleted
  m2.querySelector('[data-do=delete]').dispatchEvent(new w.MouseEvent('click', { bubbles: true, cancelable: true }));
  await wait(10);
  assert.deepEqual(w.asked, ['Удалить письмо?']);
  assert.ok(d.querySelector('.mailrow[data-letter=m2]'));
  w.answer = true;
  m2.querySelector('[data-do=delete]').dispatchEvent(new w.MouseEvent('click', { bubbles: true, cancelable: true }));
  await wait(10);
  assert.match(asked.at(-1).body, /letter=m2&do=delete/);
  assert.equal(d.querySelector('.mailrow[data-letter=m2]'), null);
  assert.equal(asked.filter(a => a.url.startsWith('/mail/letters/')).length, 0, 'the letter is not opened');
}

{ // the toolbar's «Удалить» asks how many; in «Удалённые» — for good
  const { w, d } = setup('right');
  d.querySelectorAll('.msel').forEach(b => { b.checked = true; });
  const bar = d.querySelector('#mailact');
  const del = bar.querySelector('[value=delete]');
  w.answer = false;
  const e = new w.SubmitEvent('submit', { bubbles: true, cancelable: true, submitter: del });
  bar.dispatchEvent(e);
  assert.equal(e.defaultPrevented, true);
  assert.deepEqual(w.asked, ['Удалить 2?']);
  d.querySelector('[data-mail]').dataset.role = 'trash';
  w.answer = true;
  const e2 = new w.SubmitEvent('submit', { bubbles: true, cancelable: true, submitter: del });
  bar.dispatchEvent(e2);
  assert.equal(e2.defaultPrevented, false);
  assert.equal(w.asked[1], 'Навсегда 2?');
  const e3 = new w.SubmitEvent('submit', { bubbles: true, cancelable: true, submitter: bar.querySelector('[value=archive]') });
  bar.dispatchEvent(e3);
  assert.equal(w.asked.length, 2, 'archive asks nothing');
}

{ // the right-click menu: on the letter under the mouse (it alone is ticked), with replies and the toolbar's actions
  const { w, d } = setup('right');
  d.querySelectorAll('.msel')[1].checked = true;
  const m1 = d.querySelector('.mailrow[data-letter=m1]');
  m1.querySelector('a').dispatchEvent(new w.MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: 10, clientY: 10 }));
  const menu = d.querySelector('.mctx');
  assert.ok(menu);
  assert.deepEqual([...d.querySelectorAll('.msel')].map(b => b.checked), [true, false], 'only the right-clicked letter');
  const links = [...menu.querySelectorAll(':scope > a')].map(a => a.getAttribute('href'));
  assert.deepEqual(links.slice(0, 4), ['reply', 'all', 'fwd', 'fwdatt'].map(m => `/mail?box=b1&new=1&letter=m1&folder=f1&mode=${m}`));
  assert.match(links[4], /letter=m1&pane=off$/);
  const buttons = [...menu.querySelectorAll('button')];
  assert.ok(buttons.every(b => b.getAttribute('form') === 'mailact'));
  assert.deepEqual(buttons.map(b => b.value), ['delete', 'archive', 'read', 'flag:today', 'sweep', 'rule'], 'no own-date field in the menu');
  assert.match(buttons[2].textContent, /Прочитано$/);
  assert.match(buttons[4].textContent, /Очистка$/, '«Очистка» from the letter menu');
  assert.match(buttons[5].textContent, /Создать правило…$/, '«Создать правило» from the letter menu');
  d.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Escape' }));
  assert.equal(d.querySelector('.mctx'), null);
}

{ // the whole row opens its letter: its picture, its margins (GO24DEV-472), not its buttons
  const { w, d, asked } = setup('right');
  const click = el => { const e = new w.MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }); el.dispatchEvent(e); return e; };
  const row = d.querySelector('.mailrow[data-letter=m2]');
  assert.ok(click(row.querySelector('.ava')).defaultPrevented);
  await wait(10);
  assert.equal(asked.at(-1)?.url, '/mail/letters/m2?folder=f1', 'a click on the picture opens the letter');
  assert.ok(row.classList.contains('on'));
  click(d.querySelector('.mailrow[data-letter=m1]'));
  await wait(10);
  assert.equal(asked.at(-1).url, '/mail/letters/m1?folder=f1', 'a click beside the link opens the letter');
  const n = asked.filter(a => a.url.startsWith('/mail/letters/')).length;
  click(row.querySelector('.mclip'));
  await wait(10);
  assert.equal(asked.filter(a => a.url.startsWith('/mail/letters/')).length, n, 'a button of the row opens nothing');
  click(row.querySelector('.ava'));
  assert.ok(!row.querySelector('.msel').checked);
  row.querySelector('.ava').dispatchEvent(new w.MouseEvent('click', { bubbles: true, cancelable: true, button: 0, ctrlKey: true }));
  assert.ok(row.querySelector('.msel').checked, 'Ctrl+click on the row ticks it');
}
{ // no reading pane: a click on the row goes to the letter's page
  const went = [], vc = new VirtualConsole();
  vc.on('jsdomError', e => went.push(e.message)); // jsdom does not navigate: it says so
  const dom = new JSDOM(page('off'), { runScripts: 'outside-only', url: 'http://go24.test/mail?folder=f1', virtualConsole: vc });
  const w = dom.window;
  w.CSS = { escape: s => s };
  w.matchMedia = () => ({ matches: false });
  w.eval(script);
  w.document.querySelector('.mailrow[data-letter=m2] .ava').dispatchEvent(new w.MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }));
  assert.ok(went.some(m => /navigation/i.test(m)), 'the page of the letter opens');
}
{ // no reading pane: the letter opens as a page
  const { w, d, asked } = setup('off');
  const e = new w.MouseEvent('click', { bubbles: true, cancelable: true, button: 0 });
  d.querySelector('.mailrow[data-letter=m1] a').dispatchEvent(e);
  assert.equal(asked.length, 0);
  assert.equal(e.defaultPrevented, false);
}
{ // the search: the words found are marked; the box suggests recent searches, people and operators; Ctrl+E goes to it
  const html = `<!doctype html><body><div class="mail3 pane-right" data-mail data-folder="f1">
    <section class="maillist" data-hl='["коробок","ivan"]'>
      <form method="get" action="/mail" data-msearch><input type="hidden" name="folder" value="f1">
        <span class="msearch-box"><input type="search" name="q" value="" data-msearch-q><div data-msuggest hidden></div></span>
        <template data-suggest-labels><span data-l="recent">Недавние</span><span data-l="people">Люди</span><span data-l="ops">Уточнить</span><span data-l="clear">Очистить</span>
          <span data-op="from:">от кого</span><span data-op="has:attachment">есть вложения</span><span data-op="is:unread">непрочитанные</span></template></form>
      <div class="mailrow" data-letter="m1" data-ids="m1" data-box="b1"><a class="mrow-link" href="/mail?folder=f1&letter=m1">
        <span class="mfrom">Ivan Petrov</span><span class="msubj">Заказ Коробок &lt;b&gt;</span><span class="mprev">ничего</span></a></div></section>
    <section data-mail-read></section></div></body>`;
  const dom = new JSDOM(html, { runScripts: 'outside-only', url: 'http://go24.test/mail?folder=f1' });
  const w = dom.window, d = w.document;
  w.CSS = { escape: s => s };
  w.matchMedia = () => ({ matches: false });
  const asked = [];
  w.fetch = async (url, opts = {}) => {
    asked.push({ url: String(url), method: opts.method || 'GET' });
    if (String(url).startsWith('/mail/suggest')) return { ok: true, json: async () => ({ recent: ['счёт', 'from:anna'], people: [{ name: 'Иван', email: 'ivan@client.test' }] }) };
    return { ok: true, json: async () => ({ result: true }) };
  };
  w.eval(script);
  assert.deepEqual([...d.querySelectorAll('mark.mhl')].map(m => m.textContent), ['Ivan', 'Коробок']);
  assert.equal(d.querySelector('.msubj').textContent, 'Заказ Коробок <b>', 'the text stays as it was');
  assert.equal(d.querySelector('.mprev mark'), null);

  const box = d.querySelector('[data-msearch-q]'), pop = d.querySelector('[data-msuggest]');
  d.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'e', ctrlKey: true, bubbles: true }));
  assert.equal(d.activeElement, box, 'Ctrl+E goes to the search box');
  box.dispatchEvent(new w.Event('focus'));
  await wait(20);
  assert.equal(pop.hidden, false);
  const texts = () => [...pop.querySelectorAll('.msg-item')].map(x => x.textContent);
  assert.deepEqual(texts(), ['счёт', 'from:anna', 'from:от кого', 'has:attachmentесть вложения', 'is:unreadнепрочитанные'], 'empty box: recent and operators');
  box.value = 'Заказ iv';
  box.dispatchEvent(new w.Event('input'));
  await wait(200);
  assert.match(asked.at(-1).url, /\/mail\/suggest\?q=%D0%97%D0%B0%D0%BA%D0%B0%D0%B7%20iv$/);
  assert.deepEqual(texts().slice(2), ['from:Иван <ivan@client.test>'], 'people for the word typed; no operator starts with it');
  // ↓ ↓ ↓ Enter takes the person: the word becomes from:their address
  for (let i = 0; i < 3; i++) box.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true }));
  box.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }));
  assert.equal(box.value, 'Заказ from:ivan@client.test ');
  assert.equal(pop.hidden, true);
  // an operator being typed
  box.value = 'Заказ is:u';
  box.dispatchEvent(new w.Event('input'));
  await wait(200);
  assert.deepEqual(texts().filter(t => t.startsWith('is:')), ['is:unreadнепрочитанные']);
  // a recent search is taken and searched at once
  let sent = 0;
  d.querySelector('[data-msearch]').requestSubmit = () => { sent++; };
  pop.querySelector('.msg-item').dispatchEvent(new w.MouseEvent('mousedown', { bubbles: true, cancelable: true }));
  assert.equal(box.value, 'счёт');
  assert.equal(sent, 1);
  // «Очистить» forgets the recent searches
  box.dispatchEvent(new w.Event('input'));
  await wait(200);
  pop.querySelector('.msg-head button').dispatchEvent(new w.MouseEvent('mousedown', { bubbles: true, cancelable: true }));
  await wait(10);
  assert.ok(asked.some(a => a.url === '/mail/searches/recent/clear' && a.method === 'POST'));
  box.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
  await wait(10);
}

{ // «Избранное»: the counters of a folder there and in its mailbox go together; folded is kept; reordered by dragging
  const { w, d, asked } = setup('right');
  d.querySelector('.mailrow[data-letter=m1] a').dispatchEvent(new w.MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }));
  await wait(10);
  assert.deepEqual([...d.querySelectorAll('.mfolder[data-folder-id=f1] [data-unseen]')].map(c => c.textContent), ['1', '1'], 'both links of the inbox');
  const favs = d.querySelector('[data-favs]');
  favs.open = false;
  favs.dispatchEvent(new w.Event('toggle'));
  assert.equal(w.localStorage.getItem('mail_favs'), 'closed');
  const dt = { setData() {}, effectAllowed: '', dropEffect: '' };
  const drag = (type, el, y = 0) => { const e = new w.Event(type, { bubbles: true, cancelable: true }); e.dataTransfer = dt; e.clientY = y; el.dispatchEvent(e); return e; };
  const flagged = d.querySelector('[data-fav=flagged]');
  drag('dragstart', flagged);
  assert.equal(drag('dragover', d.querySelector('[data-fav=f1]'), -1).defaultPrevented, true);
  drag('drop', flagged);
  drag('dragend', flagged);
  await wait(10);
  assert.deepEqual([...favs.querySelectorAll('[data-fav]')].map(f => f.dataset.fav), ['flagged', 'f1', 'f2']);
  const order = asked.find(a => a.url === '/mail/favorites/move');
  assert.equal(order?.body, 'order=flagged%2Cf1%2Cf2');
}
{ // folded «Избранное» stays folded on the next page
  const { d } = setup('right', w => w.localStorage.setItem('mail_favs', 'closed'));
  assert.equal(d.querySelector('[data-favs]').open, false);
}

{ // «Обработка сообщений»: read after N seconds in the pane, or never; after the open letter leaves, the next opens
  const handling = (after, readAfter) => w => {
    w.document.querySelector('[data-mail]').dataset.afterMove = after;
    const base = w.fetch;
    w.fetch = async (url, opts) => {
      if (!String(url).startsWith('/mail/letters/')) return base(url, opts);
      await base(url, opts);
      const id = String(url).slice(14, 16);
      return { ok: true, text: async () => `<article data-letter="${id}" data-read-after="${readAfter}" data-unread="${id === 'm1' ? 'm0,m1' : id}">
        <form method="post" action="/mail/act" data-mail-act><input type="hidden" name="letter" value="${id}"><button name="do" value="delete">d</button></form></article>` };
    };
  };
  const click = (w, d, id) => d.querySelector(`.mailrow[data-letter=${id}] a`).dispatchEvent(new w.MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }));

  let { w, d, asked } = setup('right', handling('next', 1));
  click(w, d, 'm1');
  await wait(10);
  const row = d.querySelector('.mailrow[data-letter=m1]');
  assert.ok(row.classList.contains('unread'), 'not read at once');
  assert.equal(unseen(d, '/mail?folder=f1'), '3');
  await wait(1100);
  const marked = asked.find(a => a.url === '/mail/act');
  assert.match(marked?.body || '', /letter=m0%2Cm1&do=read/);
  assert.ok(!row.classList.contains('unread'));
  assert.equal(unseen(d, '/mail?folder=f1'), '1');

  // the pane's form says which letter to open after (the next one; the first letter has no previous — the next)
  const form = d.querySelector('[data-mail-read] form[data-mail-act]');
  form.dispatchEvent(new w.Event('submit', { bubbles: true, cancelable: true }));
  assert.equal(form.querySelector('input[name=next]')?.value, 'm2');

  // a letter shown elsewhere is not marked read when another one opens
  ({ w, d, asked } = setup('right', handling('prev', 1)));
  click(w, d, 'm2');
  await wait(10);
  click(w, d, 'm1');
  await wait(1100);
  assert.deepEqual(asked.filter(a => a.url === '/mail/act').map(a => a.body.match(/letter=([^&]*)/)[1]), ['m0%2Cm1']);
  assert.ok(d.querySelector('.mailrow[data-letter=m2]').classList.contains('unread'));
  let f = d.querySelector('[data-mail-read] form[data-mail-act]');
  f.dispatchEvent(new w.Event('submit', { bubbles: true, cancelable: true }));
  assert.equal(f.querySelector('input[name=next]')?.value, 'm2', 'no previous letter: the next one');

  // never read by itself; «Вернуться к папке» — no letter after
  ({ w, d, asked } = setup('right', handling('folder', -1)));
  click(w, d, 'm2');
  await wait(1100);
  assert.ok(!asked.some(a => a.url === '/mail/act'));
  assert.ok(d.querySelector('.mailrow[data-letter=m2]').classList.contains('unread'));
  f = d.querySelector('[data-mail-read] form[data-mail-act]');
  f.dispatchEvent(new w.Event('submit', { bubbles: true, cancelable: true }));
  assert.equal(f.querySelector('input[name=next]'), null);

  // the open letter dropped on a folder: the next one opens in the pane
  ({ w, d, asked } = setup('right', handling('next', 0)));
  click(w, d, 'm1');
  await wait(10);
  const dt = { setData() {}, effectAllowed: '', dropEffect: '' };
  const drag = (type, el) => { const e = new w.Event(type, { bubbles: true, cancelable: true }); e.dataTransfer = dt; el.dispatchEvent(e); };
  const m1 = d.querySelector('.mailrow[data-letter=m1]');
  drag('dragstart', m1);
  drag('dragover', d.querySelector('nav > .mfolder[data-folder-id=f2]'));
  drag('drop', d.querySelector('nav > .mfolder[data-folder-id=f2]'));
  await wait(20);
  assert.ok(!m1.isConnected);
  assert.equal(asked.at(-1).url, '/mail/letters/m2?folder=f1');
  assert.equal(d.querySelector('[data-mail-read] article')?.dataset.letter, 'm2');
}

{ // the ribbon over the mail: the open letter's or the ticked letters' actions, grey without them; «…» takes what does not fit
  const btn = (d, t) => `<button type="button" class="ghost" data-rb="${d}" title="${t}" disabled><span class="mlabel">${t}</span></button>`;
  const { w, d } = setup('right', w => w.document.body.insertAdjacentHTML('afterbegin', `<div class="pagehead"><a class="button create" href="/mail?new=1">+</a>
    <div class="row mribbon" data-mribbon>${btn('reply', 'Ответить (R)')}${btn('all', 'Всем')}${btn('fwd', 'Переслать')}<span class="mribbon-sep"></span>${btn('delete', 'Удалить')}${btn('archive', 'Архив')}${btn('read', 'Прочитано')}${btn('flag', 'Флажок')}${btn('print', 'Печать')}
      <details class="mribbon-more" hidden><summary>…</summary><div data-mribbon-more></div></details></div></div>`));
  const rb = name => d.querySelector(`[data-rb="${name}"]`);
  const state = () => ['reply', 'all', 'fwd', 'delete', 'archive', 'read', 'flag', 'print'].filter(n => !rb(n).disabled).join(',');
  assert.equal(state(), '', 'nothing open, nothing ticked: all grey');
  const submitted = [];
  w.HTMLFormElement.prototype.requestSubmit = function (b) { submitted.push([this.getAttribute('action'), b?.value]); };
  const clicked = [];
  w.HTMLAnchorElement.prototype.click = function () { clicked.push(this.getAttribute('href')); };
  // ticked letters: the toolbar's actions, no reply
  const box = d.querySelector('.mailrow[data-letter=m2] .msel');
  box.checked = true;
  box.dispatchEvent(new w.Event('change', { bubbles: true }));
  await wait(5);
  assert.equal(state(), 'delete,archive,read');
  rb('archive').click();
  rb('read').click();
  assert.deepEqual(submitted, [['/mail/act', 'archive'], ['/mail/act', 'read']], 'on the ticked letters, read as one of them is unread');
  box.checked = false;
  box.dispatchEvent(new w.Event('change', { bubbles: true }));
  await wait(5);
  assert.equal(state(), '');
  // the open letter: its reply links and its pane's buttons
  d.querySelector('.mailrow[data-letter=m1] a').dispatchEvent(new w.MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }));
  await wait(20);
  assert.equal(state(), 'reply,all,fwd,delete,flag', 'what the pane of the letter offers');
  rb('reply').click();
  rb('delete').click();
  assert.equal(clicked.at(-1), '/mail?new=1&mode=reply');
  assert.deepEqual(submitted.at(-1), ['/mail/act', 'delete']);
  // a narrow window: the last ones go into «…» and still act
  const ribbon = d.querySelector('[data-mribbon]');
  Object.defineProperty(ribbon, 'clientWidth', { get: () => 200, configurable: true });
  Object.defineProperty(ribbon, 'scrollWidth', { get: () => 40 * ribbon.querySelectorAll(':scope > [data-rb], :scope > .mribbon-sep').length + 30 });
  w.dispatchEvent(new w.Event('resize'));
  const pop = d.querySelector('[data-mribbon-more]');
  assert.ok(!d.querySelector('.mribbon-more').hidden);
  assert.deepEqual([...pop.querySelectorAll('[data-rb]')].map(b => b.dataset.rb), ['delete', 'archive', 'read', 'flag', 'print']);
  pop.querySelector('[data-rb=flag]').click();
  assert.deepEqual(submitted.at(-1), ['/mail/act', 'flag:none']);
  Object.defineProperty(ribbon, 'clientWidth', { get: () => 2000 });
  w.dispatchEvent(new w.Event('resize'));
  assert.ok(d.querySelector('.mribbon-more').hidden && !pop.children.length, 'a wide window: all back, in order');
  assert.equal([...ribbon.querySelectorAll('[data-rb]')].map(b => b.dataset.rb).join(','), 'reply,all,fwd,delete,archive,read,flag,print');
}

{ // «Импорт писем»: files .eml and .msg dropped onto a server's folder or the list, or chosen in the folder's menu
  const sent = [];
  const { w, d } = setup('right', w => {
    w.document.querySelectorAll('nav > .mfolder[data-folder-id=f1], nav > .mfolder[data-folder-id=f2]').forEach(a => a.setAttribute('data-import', ''));
    w.document.querySelector('nav').insertAdjacentHTML('beforeend', '<details open><input type="file" multiple hidden data-mail-import="f2"></details>');
    const orig = w.fetch;
    w.fetch = async (url, opts = {}) => {
      if (!String(url).endsWith('/import')) return orig(url, opts);
      sent.push([String(url), opts.body.getAll('file').map(f => f.name)]);
      return { ok: true, json: async () => ({ done: 0, msg: 'Импортировано писем: 0' }) };
    };
    w.alerts = [];
    w.alert = m => w.alerts.push(m);
  });
  const files = [new w.File(['x'], 'a.eml'), new w.File(['y'], 'b.MSG'), new w.File(['z'], 'c.txt')];
  const dt = { types: ['Files'], files, dropEffect: '' };
  const drag = (type, el) => { const e = new w.Event(type, { bubbles: true, cancelable: true }); e.dataTransfer = dt; el.dispatchEvent(e); return e; };
  const f2 = d.querySelector('nav > .mfolder[data-folder-id=f2]');
  assert.ok(drag('dragover', d.querySelector('nav > .mfolder[data-folder-id=f9]')).defaultPrevented, 'files are not opened in place of the mail');
  assert.equal(dt.dropEffect, 'none', 'a folder not on a server takes no files');
  drag('dragover', f2);
  assert.equal(dt.dropEffect, 'copy');
  assert.ok(f2.classList.contains('drop'));
  drag('drop', f2);
  await wait(10);
  assert.deepEqual(sent.at(-1), ['/mail/folders/f2/import', ['a.eml', 'b.MSG']], 'only letter files go');
  assert.ok(!f2.classList.contains('drop'));
  assert.equal(w.alerts.at(-1), 'Импортировано писем: 0');
  drag('drop', d.querySelector('.mailrow[data-letter=m2]'));
  await wait(10);
  assert.equal(sent.at(-1)[0], '/mail/folders/f1/import', 'dropped onto the list — into the folder shown');
  const input = d.querySelector('input[data-mail-import]');
  Object.defineProperty(input, 'files', { value: [new w.File(['x'], 'old.eml')], configurable: true });
  input.dispatchEvent(new w.Event('change', { bubbles: true }));
  await wait(10);
  assert.deepEqual(sent.at(-1), ['/mail/folders/f2/import', ['old.eml']], 'chosen in the menu');
  assert.ok(!input.closest('details').open, 'the menu closes');
  const n = sent.length;
  dt.files = [new w.File(['z'], 'c.txt')];
  drag('drop', f2);
  await wait(10);
  assert.equal(sent.length, n, 'no letter files — nothing sent');
}

{ // «Макет → Размер текста»: an HTML letter of the reading pane is scaled with the text
  for (const [size, zoom] of [['large', '1.15'], ['small', '0.9'], ['medium', '']]) {
    const dom = new JSDOM(`<!doctype html><body><div class="mail3 pane-right mtext-${size}" data-mail data-folder="f1" data-text-size="${size}">
      <section class="maillist"></section><section data-mail-read><article data-letter="m1"><iframe class="mailhtml"></iframe></article></section></div></body>`,
    { runScripts: 'outside-only', url: 'http://go24.test/mail?folder=f1&letter=m1' });
    const w = dom.window;
    w.CSS = { escape: s => s };
    w.matchMedia = () => ({ matches: false });
    w.fetch = async () => ({ ok: true, json: async () => ({}) });
    w.eval(script);
    assert.equal(w.document.querySelector('iframe.mailhtml').contentDocument.documentElement.style.zoom, zoom, size);
  }
}

console.log('mail.js: reading pane, message handling, pane place, hot keys, ticked letters, drag and drop, pane sizes, paper clip, quick actions, right-click menu, search marks and suggestions, favorites, ribbon, letter import, text size passed');
