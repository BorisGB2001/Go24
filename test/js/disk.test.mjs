// «Диск» in jsdom: checkboxes show the selection bar with its count; «Скачать» of the selection is one ZIP address;
// «Переместить» fills the dialog's form with the selection and the folder picked (a moved folder is not offered as the
// place); files dropped on a folder row upload into it one by one; a row dropped on a folder of the path moves there,
// on «Корзина» — goes to the trash; a dropped folder and «Загрузить папку» upload each file with its path; a thumbnail
// the server has none of gives way to the file's icon.
// node test/js/disk.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const js = readFileSync(new URL('../../internal/web/static/js/disk.js', import.meta.url), 'utf8');
const dom = new JSDOM(`<!doctype html><body>
  <div class="crumbs"><a href="/disk/my" data-drop-folder="">Мой диск</a> › <a href="/disk/my?folder=d1" data-drop-folder="d1">Документы</a></div>
  <a href="/disk/my?trash" data-drop-trash>Корзина</a>
  <form data-disk-upload><input type="file" name="file" multiple><select name="storage"><option value="">x</option></select>
    <label data-disk-dir hidden><input type="file" webkitdirectory multiple hidden></label></form>
  <div data-disk="/disk/my" data-folder="d1" data-write="true" data-l-move-title="Переместить в папку" data-l-move="Переместить" data-l-top="Корень">
    <div data-disk-bulk hidden><b data-disk-count></b><button type="button" data-disk-act="zip">zip</button><button type="button" data-disk-act="move">move</button>
      <form method="post" action="/disk/my/delete" data-disk-del><button>del</button></form></div>
    <table><thead><tr><th><input type="checkbox" data-disk-all></th></tr></thead><tbody>
      <tr data-item="folder" data-id="f1" data-name="Сканы" draggable="true"><td><input type="checkbox" data-sel></td></tr>
      <tr data-item="folder" data-id="f2" data-name="Архив" draggable="true"><td><input type="checkbox" data-sel></td></tr>
      <tr data-item="file" data-id="x1" data-name="a.txt" draggable="true"><td><input type="checkbox" data-sel></td></tr>
    </tbody></table>
    <div data-disk-drop hidden></div><div data-disk-progress hidden></div>
  </div>
  <dialog data-disk-dlg><form method="post" action="/disk/my/move" data-disk-move><input type="hidden" name="folder" value="d1">
    <input type="hidden" name="copy"><input type="hidden" name="to"><input type="hidden" name="to_folder">
    <h3 data-disk-dlg-title></h3><select data-disk-drives></select><div data-disk-path></div><div data-disk-folders></div><button data-disk-here></button></form></dialog>
</body>`, { runScripts: 'outside-only', url: 'http://go24.test/disk/my?folder=d1' });
const w = dom.window;
const doc = w.document;
const calls = [];
let reloaded = 0;
w.fetch = async (u, o = {}) => {
  calls.push([String(u), o.body ? String(o.body) : '']);
  const json = d => ({ ok: true, json: async () => d });
  if (String(u).startsWith('/mail/drive')) {
    return json({ drives: [{ which: 'my', name: 'Мой диск' }, { which: 'company', name: 'Общий диск' }], which: 'my', write: true,
      path: [{ id: 'd1', name: 'Документы' }], folders: [{ id: 'f1', name: 'Сканы' }, { id: 'f2', name: 'Архив' }] });
  }
  return json({ redirect: '/disk/my?folder=d1' });
};
const sent = [], paths = [];
w.XMLHttpRequest = class {
  constructor() { this.upload = {}; this.headers = {}; }
  open(m, u) { this.url = u; }
  setRequestHeader(k, v) { this.headers[k] = v; }
  send(body) { sent.push([this.url, body.get('folder'), body.get('file').name, this.headers.Accept]); paths.push(body.get('path') + '|' + body.get('file').name); this.status = 200; this.responseText = '{}'; setTimeout(() => this.onload(), 0); }
};
w.eval('location.reload = () => {}');
w.HTMLInputElement.prototype.webkitdirectory = false; // jsdom has no folder picker
w.eval(js);
const tick = () => new Promise(r => setTimeout(r, 5));
const rows = [...doc.querySelectorAll('tr[data-item]')];
const check = (row, on) => { const c = row.querySelector('[data-sel]'); c.checked = on; c.dispatchEvent(new w.Event('change', { bubbles: true })); };

// the selection bar
const bar = doc.querySelector('[data-disk-bulk]');
assert.equal(bar.hidden, true);
check(rows[0], true);
check(rows[2], true);
assert.equal(bar.hidden, false);
assert.equal(doc.querySelector('[data-disk-count]').textContent, '2');
assert.equal(doc.querySelector('[data-disk-all]').indeterminate, true);

// «Удалить» sends the selection
const del = doc.querySelector('[data-disk-del]');
del.dispatchEvent(new w.Event('submit', { bubbles: true, cancelable: true }));
assert.deepEqual([...new w.FormData(del)].map(([k, v]) => k + '=' + v), ['folder_id=f1', 'file=x1']);

// «Переместить»: the dialog's form gets the selection; the folder moved is not offered; a picked folder is the target
doc.querySelector('[data-disk-bulk] [data-disk-act=move]').click();
await tick();
const form = doc.querySelector('[data-disk-move]');
assert.equal(doc.querySelector('[data-disk-dlg-title]').textContent, 'Переместить в папку');
assert.deepEqual([...form.querySelectorAll('[data-picked]')].map(i => i.name + '=' + i.value), ['folder_id=f1', 'file=x1']);
const offered = [...doc.querySelectorAll('[data-disk-folders] [data-go]')].map(a => a.dataset.go);
assert.deepEqual(offered, ['f2']);
assert.equal(form.elements.namedItem('to').value, 'my');
assert.equal(form.elements.namedItem('to_folder').value, 'd1');
doc.querySelector('[data-disk-folders] [data-go="f2"]').click();
await tick();
assert.equal(form.elements.namedItem('to_folder').value, 'f2');
assert.ok(calls.some(([u]) => u === '/mail/drive?which=my&folder=f2'));

// files dropped on a folder row upload into that folder
const drop = (el, init) => {
  const e = new w.Event('drop', { bubbles: true, cancelable: true });
  e.dataTransfer = init;
  el.dispatchEvent(e);
  return e;
};
const f = new w.File(['abc'], 'план.docx');
const e1 = drop(rows[1].querySelector('td'), { types: ['Files'], files: [f] });
assert.equal(e1.defaultPrevented, true);
await tick(); await tick();
assert.deepEqual(sent, [['/disk/my/upload', 'f2', 'план.docx', 'application/json']]);

// a row dragged onto the drive's top in the path moves there
const ds = new w.Event('dragstart', { bubbles: true });
ds.dataTransfer = { setData() {}, effectAllowed: '' };
check(rows[0], false); check(rows[2], false);
rows[2].dispatchEvent(ds);
drop(doc.querySelector('[data-drop-folder=""]'), { types: ['text/plain'], files: [] });
await tick();
const mv = calls.find(([u]) => u === '/disk/my/move');
assert.ok(mv, 'moved by dragging');
assert.equal(mv[1], 'folder=d1&to=my&to_folder=&file=x1');

// a row dropped on «Корзина» goes to the trash
rows[0].dispatchEvent(ds);
drop(doc.querySelector('[data-drop-trash]'), { types: ['text/plain'], files: [] });
await tick();
const del2 = calls.find(([u]) => u === '/disk/my/delete');
assert.ok(del2, 'deleted by dragging');
assert.equal(del2[1], 'folder=d1&folder_id=f1');

// a dropped folder: every file of it and of its folders, with the path from the folder dropped
{
  const file = (name, text) => ({ isFile: true, name, file: ok => ok(new w.File([text], name)) });
  const dir = (name, kids) => ({ isDirectory: true, name, createReader() {
    const parts = [kids.slice(0, 1), kids.slice(1), []]; // given by parts, as browsers do
    return { readEntries: ok => ok(parts.shift()) };
  } });
  const tree = dir('Отчёт', [file('итог.docx', '1'), dir('Фото', [file('a.jpg', '2'), file('b.jpg', '3')])]);
  const loose = file('readme.txt', '4');
  paths.length = 0;
  const e2 = drop(doc.querySelector('[data-disk-drop]'), { types: ['Files'], files: [], items: [{ webkitGetAsEntry: () => tree }, { webkitGetAsEntry: () => loose }] });
  assert.equal(e2.defaultPrevented, true);
  for (let i = 0; i < 6; i++) await tick();
  assert.deepEqual(paths, ['Отчёт|итог.docx', 'Отчёт/Фото|a.jpg', 'Отчёт/Фото|b.jpg', '|readme.txt']);
  assert.ok(doc.querySelector('[data-disk-progress]').textContent.includes('Отчёт/Фото/a.jpg'));

  // «Загрузить папку» is offered where the browser picks folders; the files keep their paths
  const pick = doc.querySelector('[data-disk-dir]');
  assert.equal(pick.hidden, false);
  const f1 = new w.File(['5'], 'c.txt'), f2 = new w.File(['6'], 'd.txt');
  Object.defineProperty(f1, 'webkitRelativePath', { value: 'Архив/c.txt' });
  Object.defineProperty(f2, 'webkitRelativePath', { value: 'Архив/2025/d.txt' });
  const input = pick.querySelector('input');
  Object.defineProperty(input, 'files', { value: [f1, f2] });
  paths.length = 0;
  input.dispatchEvent(new w.Event('change', { bubbles: true }));
  for (let i = 0; i < 4; i++) await tick();
  assert.deepEqual(paths, ['Архив|c.txt', 'Архив/2025|d.txt']);
}

// the viewer and the tiles: a click on a file opens it, › goes to the next file of the folder, Esc closes; the tiles
// are remembered
{
  const store = new Map();
  const d2 = new JSDOM(`<!doctype html><body><div class="seg" data-disk-view><button data-view-mode="list"></button><button data-view-mode="tiles"></button><button data-view-mode="big"></button></div>
    <div data-disk="/disk/company" data-folder="" data-write="false" data-l-none="Не открывается" data-base="https://go24.example"><table><tbody>
    <tr data-item="folder" data-id="f1"><td><input type="checkbox" data-sel></td></tr>
    <tr data-item="file" data-id="p"><td><input type="checkbox" data-sel></td><td class="thumb"><a href="/files/p" data-view="p" data-kind="image" data-name="p.png"></a></td><td><a href="/files/p" data-view="p" data-kind="image" data-name="p.png">p.png</a></td></tr>
    <tr data-item="file" data-id="v"><td><input type="checkbox" data-sel></td><td><a href="/files/v" data-view="v" data-kind="video" data-name="v.mp4">v.mp4</a></td></tr>
    <tr data-item="file" data-id="z"><td><input type="checkbox" data-sel></td><td><a href="/files/z" data-view="z" data-kind="" data-name="z.exe">z.exe</a></td></tr>
    <tr data-item="file" data-id="x"><td><input type="checkbox" data-sel></td><td class="thumb"><a href="/files/x" data-view="x" data-kind="html" data-name="план.docx" data-edit="/disk/company/edit/x?folder=" data-edit-label="Редактировать"></a></td><td><a href="/disk/company/edit/x?folder=">план.docx</a></td></tr>
    </tbody></table><div data-disk-bulk hidden><b data-disk-count></b></div><div data-disk-drop hidden></div><div data-disk-progress hidden></div></div></body>`,
    { runScripts: 'outside-only', url: 'http://go24.test/disk/company' });
  const w2 = d2.window;
  Object.defineProperty(w2, 'localStorage', { value: { getItem: k => store.get(k) ?? null, setItem: (k, v) => store.set(k, String(v)) } });
  w2.eval(js);
  const d = w2.document;
  d.querySelector('.thumb a').dispatchEvent(new w2.MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }));
  let v = d.querySelector('.mviewer');
  assert.ok(v, 'the viewer opened');
  assert.equal(v.querySelector('img').getAttribute('src'), '/files/p/raw');
  v.querySelector('.mv-next').click();
  v = d.querySelector('.mviewer');
  assert.equal(v.querySelector('video').getAttribute('src'), '/files/v/raw');
  v.querySelector('.mv-next').click();
  assert.equal(d.querySelector('.mviewer-none').textContent, 'Не открывается');
  d.dispatchEvent(new w2.KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }));
  d.dispatchEvent(new w2.KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }));
  assert.ok(d.querySelector('.mviewer img'), 'the last file wraps to the first');
  // a picture zooms by the buttons and the keys, turns by 90°; «Копировать внутреннюю ссылку» is the file's card
  v = d.querySelector('.mviewer');
  v.querySelector('.mv-zoom-in').click();
  assert.equal(v.dataset.zoom, '1.25');
  d.dispatchEvent(new w2.KeyboardEvent('keydown', { key: '+', bubbles: true }));
  assert.equal(Number(v.dataset.zoom).toFixed(4), '1.5625');
  d.dispatchEvent(new w2.KeyboardEvent('keydown', { key: '0', bubbles: true }));
  assert.equal(v.dataset.zoom, '1');
  v.querySelector('.mv-zoom-out').click();
  assert.equal(v.dataset.zoom, '0.8');
  v.querySelector('.mv-rotate').click();
  v.querySelector('.mv-rotate').click();
  assert.equal(v.dataset.turn, '180');
  assert.equal(v.querySelector('.mv-link').dataset.copy, 'https://go24.example/files/p/about');
  assert.ok(v.querySelector('.mv-print') && v.querySelector('.mv-full'));
  assert.equal(v.querySelector('a.button[href^="/disk"]'), null, 'a picture has no editor');
  d.dispatchEvent(new w2.KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
  assert.equal(d.querySelector('.mviewer'), null);
  // a document opens from its thumbnail as itself, with «Редактировать»
  d.querySelector('tr[data-id=x] .thumb a').dispatchEvent(new w2.MouseEvent('click', { bubbles: true, cancelable: true, button: 0 }));
  v = d.querySelector('.mviewer');
  assert.equal(v.querySelector('.mviewer-name').textContent, 'план.docx');
  const ed = v.querySelector('a.button[href^="/disk"]');
  assert.equal(ed.getAttribute('href'), '/disk/company/edit/x?folder=');
  assert.equal(ed.textContent, 'Редактировать');
  assert.equal(v.querySelector('.mv-zoom-in'), null, 'only pictures zoom');
  d.dispatchEvent(new w2.KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
  d.querySelector('[data-view-mode=tiles]').click();
  assert.ok(d.querySelector('[data-disk]').classList.contains('tiles'));
  assert.equal(store.get('go24.disk.view'), 'tiles');
  d.querySelector('[data-view-mode=big]').click(); // «Крупная плитка»
  assert.ok(d.querySelector('[data-disk]').classList.contains('tiles') && d.querySelector('[data-disk]').classList.contains('big'));
  assert.equal(store.get('go24.disk.view'), 'big');
}

// «История версий»: the list comes from the server; a former version downloads, «Восстановить» sends the form
{
  const dom3 = new JSDOM(`<!doctype html><body><form method="get" data-disk-filter><input name="q" value="план"><select name="period" data-disk-period><option value="">любая</option><option value="today">сегодня</option><option value="range">период</option></select><div data-disk-range><input type="date" name="from"></div></form><div data-disk="/disk/my" data-folder="" data-write="true" data-l-download="Скачать">
    <table><tbody><tr data-item="file" data-id="x1"><td><button data-disk-versions="x1" data-mine>v</button></td></tr></tbody></table></div>
    <dialog data-disk-ver data-l-current="текущая" data-l-restore="Восстановить"><span data-disk-ver-name></span><table><tbody data-disk-ver-list></tbody></table>
    <form method="post" action="/disk/my/version-restore" data-disk-ver-form><input type="hidden" name="file"><input type="hidden" name="version"></form></dialog>
  </body>`, { runScripts: 'outside-only', url: 'http://go24.test/disk/my' });
  const w3 = dom3.window, d3 = w3.document;
  w3.fetch = async u => ({ ok: true, json: async () => ({ name: 'план.docx', versions: [
    { id: '', no: 2, date: '21.10.2026 10:00', author: 'Анна', size_text: '2 Б' },
    { id: 'v1', no: 1, date: '20.10.2026 09:00', author: 'Иван', size_text: '1 Б' }] }), url: u });
  let submitted = null;
  w3.HTMLFormElement.prototype.requestSubmit = function () { submitted = [...new w3.FormData(this)].map(([k, v]) => k + '=' + v); };
  w3.eval(js);
  d3.querySelector('[data-disk-versions]').click();
  await tick();
  assert.equal(d3.querySelector('[data-disk-ver-name]').textContent, 'план.docx');
  const trs = [...d3.querySelectorAll('[data-disk-ver-list] tr')];
  assert.equal(trs.length, 2);
  assert.match(trs[0].textContent, /2 \(текущая\)/);
  assert.equal(trs[0].querySelector('a, button'), null, 'the current version has no actions');
  assert.equal(trs[1].querySelector('a').getAttribute('href'), '/files/x1/versions/v1');
  trs[1].querySelector('button').click();
  assert.deepEqual(submitted, ['file=x1', 'version=v1']);
  // the filter: the dates «с — по» only for «Период»
  const sel = d3.querySelector('[data-disk-period]'), range = d3.querySelector('[data-disk-range]');
  assert.ok(range.hidden);
  sel.value = 'range';
  sel.dispatchEvent(new w3.Event('change', { bubbles: true }));
  assert.ok(!range.hidden);
  sel.value = 'today';
  sel.dispatchEvent(new w3.Event('change', { bubbles: true }));
  assert.ok(range.hidden);
}
// a file the folder already has: the server names the clashes; each is asked about — a new version, another name or
// skip (the version is not offered for a file the user may not change), «Для всех таких файлов» answers the rest
{
  const dom4 = new JSDOM(`<!doctype html><body><div data-disk="/disk/company" data-folder="d1" data-write="true" data-l-skipped="Пропущен">
    <div data-disk-progress hidden></div></div>
    <dialog data-disk-clash data-l-text="В папке уже есть файл «{name}».">
      <p data-disk-clash-text></p><p data-disk-clash-ro hidden></p><label data-disk-clash-many><input type="checkbox" data-disk-clash-all></label>
      <button value="version" data-disk-clash-version></button></dialog></body>`, { runScripts: 'outside-only', url: 'http://go24.test/disk/company' });
  const w4 = dom4.window, d4 = w4.document;
  let asked = [];
  w4.HTMLDialogElement.prototype.showModal = function () { this.open = true; asked.push(this); };
  w4.HTMLDialogElement.prototype.close = function (v) { this.open = false; this.returnValue = v ?? ''; this.dispatchEvent(new w4.Event('close')); };
  let probe = null;
  w4.fetch = async (u, o) => { probe = [u, String(o.body)]; return { ok: true, json: async () => ({ clashes: [{ i: 0, version: true }, { i: 1, version: false }, { i: 3, version: true }] }) }; };
  const up = [];
  w4.XMLHttpRequest = class {
    constructor() { this.upload = {}; }
    open() {} setRequestHeader() {}
    send(body) { up.push(body.get('file').name + ':' + body.get('conflict')); this.status = 200; setTimeout(() => this.onload(), 0); }
  };
  w4.eval('location.reload = () => {}');
  w4.eval(js);
  const dlg = d4.querySelector('[data-disk-clash]');
  const answer = async (v, all) => {
    for (let i = 0; i < 5 && !dlg.open; i++) await tick();
    assert.equal(dlg.open, true, 'asked');
    d4.querySelector('[data-disk-clash-all]').checked = !!all;
    dlg.close(v);
  };
  const files = ['a.docx', 'b.xlsx', 'c.txt', 'd.pdf'].map(n => new w4.File(['1'], n));
  const done = w4.go24DiskUpload(files, 'd1');
  await answer('version');
  assert.equal(probe[0], '/disk/company/clash');
  assert.equal(probe[1], 'folder=d1&name=a.docx&path=&name=b.xlsx&path=&name=c.txt&path=&name=d.pdf&path=');
  assert.equal(d4.querySelector('[data-disk-clash-text]').textContent, 'В папке уже есть файл «a.docx».');
  await answer('rename', true);
  assert.equal(d4.querySelector('[data-disk-clash-version]').hidden, true, 'no version of a file the user may not change');
  assert.equal(d4.querySelector('[data-disk-clash-ro]').hidden, false);
  await done;
  assert.equal(asked.length, 2, 'd.pdf took the answer for all');
  assert.deepEqual(up, ['a.docx:version', 'b.xlsx:rename', 'c.txt:', 'd.pdf:rename']);

  // «Пропустить» does not upload; «Отменить загрузку» (Esc) stops the rest
  up.length = 0; asked = [];
  const again = w4.go24DiskUpload(files, 'd1');
  await answer('skip');
  await answer('');
  await again;
  assert.deepEqual(up, []);
  assert.match(d4.querySelector('[data-disk-progress]').textContent, /Пропущен/);
}
// a thumbnail the server has none of (404): the icon next to it is shown instead
{
  const d5 = new JSDOM(`<!doctype html><body><table class="disk-list"><tr><td class="thumb"><a>
    <img src="/files/x1/thumb" data-thumb><span class="ticon" hidden>DOCX</span></a></td></tr></table></body>`, { runScripts: 'outside-only' });
  d5.window.eval(js);
  const img = d5.window.document.querySelector('img[data-thumb]');
  img.dispatchEvent(new d5.window.Event('error'));
  assert.equal(d5.window.document.querySelector('img[data-thumb]'), null, 'the broken thumbnail stays');
  assert.equal(d5.window.document.querySelector('.ticon').hidden, false, 'the icon is not shown');
}

console.log('disk.test.mjs ok');
