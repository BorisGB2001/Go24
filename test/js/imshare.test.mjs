// The message's «…» in jsdom (static/js/im.js): a quick reaction and the whole choice react to the message;
// «Переслать по почте» makes the letter and opens it; «Поделиться картинкой» draws the message into a PNG — on a
// computer a window with «Скачать» / «Копировать», on a phone the system «Поделиться»; a long press opens the menu.
// node test/js/imshare.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const im = readFileSync(new URL('../../internal/web/static/js/im.js', import.meta.url), 'utf8');
const tick = (ms = 20) => new Promise(r => setTimeout(r, ms));
const menu = `<details class="im-menu"><summary>…</summary><div class="pop"><div class="im-menu-reacts"><button type="button" data-react="👍">👍</button>
  <button type="button" data-react-more>+</button></div><button type="button" data-act="mail">m</button><button type="button" data-act="share">s</button></div></details>`;
const list = `<div class="msg" data-id="$1" data-sender="Анна Петрова" data-photo="" data-ts="1761394800000" data-when="25.10.2026 14:20"><div class="bubble">${menu}
  <div class="text" data-body="Сверить счета до пятницы">Сверить счета до пятницы</div></div></div>`;
const dom = new JSDOM(`<!doctype html><html lang="ru-RU"><body><div class="im" data-im data-room="!r" data-failed="ошибка">
  <nav data-list></nav><section class="im-chat"><div class="im-msgs" data-msgs>${list}</div>
  <form class="chatform" data-send><textarea name="body"></textarea><button>s</button></form>
  <div class="im-emoji" data-emoji-pop hidden data-recent="r" data-smileys="s" data-gestures="g" data-hearts="h" data-objects="o"></div>
  <dialog data-share-dlg data-copied="скопировано"><img data-share-img><button data-share-native hidden></button><a data-share-save></a><button type="button" data-share-copy></button><span data-share-note></span></dialog></section>
  <aside data-thread-pane hidden><div data-thread-msgs></div></aside></div></body></html>`,
{ runScripts: 'outside-only', url: 'http://go24.test/im?room=!r', pretendToBeVisual: true });
const w = dom.window;
w.CSS = { escape: s => s };
w.HTMLElement.prototype.scrollIntoView = () => {};
w.HTMLDialogElement.prototype.showModal = function () { this.setAttribute('open', ''); };
// a canvas that records what is drawn
const drawn = [];
w.HTMLCanvasElement.prototype.getContext = function () {
  const cv = this;
  return new Proxy({ canvas: cv, measureText: t => ({ width: String(t).length * 8 }), fillText: t => drawn.push(t) }, {
    get: (o, k) => (k in o ? o[k] : () => {}), set: (o, k, v) => { o[k] = v; return true; } });
};
w.HTMLCanvasElement.prototype.toBlob = function (cb) { cb(new w.Blob(['png'], { type: 'image/png' })); };
w.URL.createObjectURL = () => 'blob:pic'; w.URL.revokeObjectURL = () => {};
let copied = null;
w.ClipboardItem = class { constructor(x) { this.x = x; } };
Object.defineProperty(w.navigator, 'clipboard', { value: { write: async items => { copied = items; } } });
w.matchMedia = () => ({ matches: false });
const posts = [];
let assigned = '';
w.fetch = async (u, opts = {}) => {
  if (opts.method === 'POST') posts.push({ u, body: String(opts.body) });
  const data = u === '/im/mail' ? { url: '/mail?letter=d1' } : { html: u.startsWith('/im/messages') ? list : '' };
  return { ok: true, json: async () => data };
};
w.EventSource = class { addEventListener() {} close() {} };
w.eval(im.replace('location.assign(data.url)', 'window.__assign(data.url)'));
w.__assign = u => { assigned = u; };
await tick();
const d = w.document, $ = s => d.querySelector(s);
const msg = () => $('[data-msgs] [data-id="$1"]');
const reacts = () => posts.filter(p => p.u === '/im/react').map(p => decodeURIComponent(p.body));

// a quick reaction of the menu
msg().querySelector('details').open = true;
[...msg().querySelectorAll('[data-react]')].find(b => b.dataset.react === '👍').click(); // jsdom's selectors miss emoji
await tick();
assert.deepEqual(reacts(), ['room=!r&event=$1&emoji=👍']);
// the whole choice: beside the message, the smiley goes to it as a reaction, not into the field
posts.length = 0;
msg().querySelector('details').open = true;
msg().querySelector('[data-react-more]').click();
const pop = $('[data-emoji-pop]');
assert.equal(pop.hidden, false);
assert.ok(msg().querySelector('.bubble').contains(pop), 'the choice is beside the message');
assert.equal(msg().querySelector('details').open, false, 'the menu is closed');
[...pop.querySelectorAll('[data-emoji-pick]')].find(b => b.dataset.emojiPick === '🎉').click();
await tick();
assert.deepEqual(reacts(), ['room=!r&event=$1&emoji=🎉']);
assert.equal($('[data-send] textarea').value, '', 'nothing typed into the field');
assert.equal(pop.hidden, true);

// «Переслать по почте»
msg().querySelector('[data-act="mail"]').click();
await tick();
assert.deepEqual(posts.filter(p => p.u === '/im/mail').map(p => p.body), ['room=%21r&event=%241']);
assert.equal(assigned, '/mail?letter=d1', 'the letter opens in the mail');

// «Поделиться картинкой» on a computer: the picture in a window to download or copy
msg().querySelector('[data-act="share"]').click();
await tick(50);
assert.ok(drawn.includes('Анна Петрова') && drawn.includes('Сверить счета до пятницы') && drawn.includes('АП') && drawn.includes('25.10.2026 14:20'), 'the card: initials, name, time, text — ' + drawn.join(' | '));
const dlg = $('[data-share-dlg]');
assert.ok(dlg.hasAttribute('open'));
assert.equal($('[data-share-img]').getAttribute('src'), 'blob:pic');
assert.equal($('[data-share-save]').getAttribute('href'), 'blob:pic');
assert.equal($('[data-share-native]').hidden, true, 'no system sharing here');
assert.equal($('[data-share-copy]').hidden, false);
$('[data-share-copy]').click();
await tick();
assert.ok(copied?.[0]?.x?.['image/png'], 'the PNG is copied');
assert.equal($('[data-share-note]').textContent, 'скопировано');

// without a clipboard for pictures (not https) — no «Копировать», «Скачать» stays
dlg.removeAttribute('open');
const ci = w.ClipboardItem;
delete w.ClipboardItem;
msg().querySelector('[data-act="share"]').click();
await tick(50);
assert.equal($('[data-share-copy]').hidden, true);
w.ClipboardItem = ci;

// on a phone: the system «Поделиться» with the PNG file
dlg.removeAttribute('open');
let shared = null;
w.matchMedia = () => ({ matches: true });
w.navigator.canShare = x => !!x.files;
w.navigator.share = async x => { shared = x; };
msg().querySelector('[data-act="share"]').click();
await tick(50);
assert.equal(shared?.files?.[0]?.name, 'message.png');
assert.equal(shared.files[0].type, 'image/png');
assert.ok(!dlg.hasAttribute('open'), 'no window on a phone');

// a long press opens the menu
msg().querySelector('.text').dispatchEvent(new w.Event('touchstart', { bubbles: true }));
await tick(600);
assert.equal(msg().querySelector('details').open, true);
console.log('imshare: ok');
