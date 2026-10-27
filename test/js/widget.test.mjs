// «Виджет на сайт» in jsdom, as on a stranger's page: the button opens the chat, a message starts the dialogue
// (token kept in the site's storage), the operator's answer shows, the visitor rates it and gets the conversation by
// e-mail, the contact form sends; files both ways, the operator's name and photo, «печатает…» and «прочитано»; the chat
// bot's menu as buttons.
// node test/js/widget.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const src = readFileSync(new URL('../../internal/web/widget/widget.js', import.meta.url), 'utf8');
const cfg = { id: 'w1', base: 'https://go24.test', color: '2FC6F6', name: 'Магазин', greeting: 'Здравствуйте!',
  t: { open: 'Напишите', chat: 'Чат', contact: 'Контакты', placeholder: '…', contact_note: 'note', name: 'Имя', phone: 'Тел', email: 'Почта', question: 'Вопрос', send: 'Отправить', thanks: 'Спасибо!',
  vote: 'Оцените', vote_comment: 'Комментарий', vote_thanks: 'Спасибо за оценку', history: 'На почту', history_note: 'Пришлём', history_sent: 'Отправлено',
  attach: 'Файл', typing: 'печатает…', read: 'Прочитано', file_big: 'Большой',
  form_fill: 'Заполнить форму', form_filled: 'Форма заполнена' }, max_file: 1000 };
const dom = new JSDOM('<!doctype html><body><h1>Магазин</h1></body>', { runScripts: 'outside-only', url: 'https://shop.example/catalog', pretendToBeVisual: true });
const w = dom.window;
const calls = [];
let next = null; // the next answer of «messages»
w.fetch = async (url, opts) => {
  const op = url.split('/api/')[1].split('?')[0];
  const form = opts.body instanceof w.FormData;
  calls.push({ op, url, headers: opts.headers, body: opts.body && !form ? JSON.parse(opts.body) : null, form: form ? opts.body : null });
  if (op === 'messages' && next) { const n = next; next = null; return { ok: true, json: async () => n }; }
  const answers = { file: {}, typing: {}, visit: { token: 'tok123' }, say: {}, contact: {}, vote: {}, history: {},
    messages: { messages: [{ id: 1, operator: '', body: 'Есть доставка?' }, { id: 2, operator: 'Анна', body: 'Да' }],
      state: { closed: true, vote: 0, can_vote: true, vote_text: 'Оцените ответ', history: true, email: 'ivan@shop.example' } } };
  return { ok: true, json: async () => answers[op] };
};
w.eval(src.replace('/*CONFIG*/{}', JSON.stringify(cfg)));
const tick = () => new Promise(r => setTimeout(r, 20));
const host = w.document.body.lastElementChild;
const root = host.shadowRoot;
assert.ok(root, 'the widget lives in a shadow root');
assert.equal(root.querySelector('.box').hidden, true);
assert.equal(root.querySelector('.head .tg'), null, 'no Telegram without a bot');
assert.match(root.querySelector('.msgs').textContent, /Здравствуйте!/, 'the greeting');
root.querySelector('.btn').click();
assert.equal(root.querySelector('.box').hidden, false);

const ta = root.querySelector('.say textarea');
ta.value = 'Есть доставка?';
root.querySelector('.say').dispatchEvent(new w.Event('submit', { bubbles: true, cancelable: true }));
await tick();
assert.deepEqual(calls.slice(0, 3).map(c => c.op), ['visit', 'say', 'messages']);
assert.equal(calls[0].body.page, 'https://shop.example/catalog');
assert.equal(calls[1].headers['X-Go24-Visitor'], 'tok123');
assert.equal(w.localStorage.getItem('go24.widget.w1'), 'tok123', 'the token stays on the site');
const shown = [...root.querySelectorAll('.m')].map(m => m.className + ':' + m.textContent);
assert.ok(shown.includes('m o:АннаДа'), 'the operator answers: ' + shown.join(' | '));

// the quality of service: the answer is rated, with a comment
const rate = root.querySelector('.rate');
assert.equal(rate.hidden, false, 'the visitor is asked to rate the answer');
assert.match(rate.textContent, /Оцените ответ/);
rate.querySelector('[data-vote="1"]').click();
await tick();
assert.deepEqual(calls.filter(c => c.op === 'vote').at(-1).body, { vote: 1 });
assert.ok(rate.querySelector('[data-vote="1"]').classList.contains('on'), 'the rating is marked');
assert.equal(rate.querySelector('.vc').hidden, false, 'a comment may follow');
rate.querySelector('.vc textarea').value = 'Быстро';
rate.querySelector('.vc').dispatchEvent(new w.Event('submit', { bubbles: true, cancelable: true }));
await tick();
assert.deepEqual(calls.filter(c => c.op === 'vote').at(-1).body, { vote: 1, text: 'Быстро' });

// the conversation by e-mail: the address the visitor left is filled in
const mailBtn = root.querySelector('[data-history]');
assert.equal(mailBtn.hidden, false);
mailBtn.click();
const hist = root.querySelector('.hist');
assert.equal(hist.hidden, false);
assert.equal(root.querySelector('.msgs').hidden, true);
assert.equal(hist.querySelector('[name=email]').value, 'ivan@shop.example');
hist.dispatchEvent(new w.Event('submit', { bubbles: true, cancelable: true }));
await tick();
assert.deepEqual(calls.filter(c => c.op === 'history').at(-1).body, { email: 'ivan@shop.example' });
assert.match(hist.textContent, /Отправлено/);
root.querySelector('[data-tab="chat"]').click();
assert.equal(hist.hidden, true);
assert.equal(root.querySelector('.msgs').hidden, false);

root.querySelector('[data-tab="contact"]').click();
assert.equal(rate.hidden, true, 'no rating on the contact form');
const f = root.querySelector('.contact');
f.querySelector('[name=name]').value = 'Иван';
f.querySelector('[name=phone]').value = '+7 900';
f.dispatchEvent(new w.Event('submit', { bubbles: true, cancelable: true }));
await tick();
assert.deepEqual(calls.filter(c => c.op === 'contact').at(-1).body, { name: 'Иван', phone: '+7 900', email: '', text: '' });
assert.match(root.querySelector('.contact').textContent, /Спасибо!/);
// files, the operator, «печатает…», «прочитано»
root.querySelector('[data-tab="chat"]').click();
next = { messages: [{ id: 3, operator: '', body: '', file: { name: 'a.png', url: 'https://go24.test/w/w1/file/3/s3', image: true } },
  { id: 4, operator: 'Анна', body: 'Счёт', file: { name: 'bill.pdf', url: 'https://go24.test/w/w1/file/4/s4', image: false } }],
  operator: { name: 'Анна', photo: 'https://go24.test/w/w1/photo/u1', typing: true, read: 3 } };
const att = root.querySelector('.att input');
Object.defineProperty(att, 'files', { value: [new w.File(['x'], 'a.png', { type: 'image/png' })], configurable: true });
ta.value = 'подпись';
att.dispatchEvent(new w.Event('change'));
await tick();
const sent = calls.filter(c => c.op === 'file').at(-1);
assert.equal(sent.form.get('file').name, 'a.png');
assert.equal(sent.form.get('body'), 'подпись', 'the text goes with the file');
assert.equal(sent.headers['Content-Type'], undefined, 'the browser sets the multipart boundary');
assert.equal(ta.value, '');
assert.match(calls.filter(c => c.op === 'messages').at(-1).url, /seen=1/, 'the open widget tells the visitor has seen it');
const pic = root.querySelector('.m.v[data-id="3"] img');
assert.equal(pic?.src, 'https://go24.test/w/w1/file/3/s3', 'the visitor\'s picture is shown');
const pdf = root.querySelector('.m.o[data-id="4"] a');
assert.equal(pdf.textContent, '📎 bill.pdf');
assert.equal(pdf.href, 'https://go24.test/w/w1/file/4/s4');
const head = root.querySelector('.op');
assert.equal(head.hidden, false);
assert.equal(head.textContent, 'Анна');
assert.equal(head.querySelector('img').src, 'https://go24.test/w/w1/photo/u1', 'the operator\'s photo');
assert.equal(root.querySelector('.typing').hidden, false);
assert.equal(root.querySelector('.typing').textContent, 'Анна печатает…');
assert.equal(root.querySelector('.m.v[data-id="3"]').nextElementSibling.className, 'read', '«прочитано» under the message read');
assert.equal(root.querySelectorAll('.read').length, 1);
att.value = '';
Object.defineProperty(att, 'files', { value: [new w.File(['x'.repeat(2000)], 'big.zip')], configurable: true });
let warned = '';
w.alert = m => { warned = m; };
const before = calls.length;
att.dispatchEvent(new w.Event('change'));
await tick();
assert.equal(warned, 'Большой', 'a file over the limit is not sent');
assert.equal(calls.length, before);
ta.value = 'пишу';
ta.dispatchEvent(new w.Event('input'));
ta.dispatchEvent(new w.Event('input'));
await tick();
assert.equal(calls.filter(c => c.op === 'typing').length, 1, '«печатает…» once in a while');
assert.match(root.innerHTML, /@media \(max-width:480px\)/, 'the whole screen on a phone');

// a CRM form the operator sent: its card, the form inside the chat, «заполнена» when the client has sent it
const say = async text => { ta.value = text; root.querySelector('.say').dispatchEvent(new w.Event('submit', { bubbles: true, cancelable: true })); await tick(); };
next = { messages: [{ id: 5, operator: 'Анна', body: '📋 Заполните форму «Заявка»', form: { id: 'f1', url: 'https://go24.test/cf/T?embed=1&ol=k1', done: false } }] };
await say('Хочу заказ');
const fb = root.querySelector('.m.o[data-id="5"] .fb'), fw = root.querySelector('.fw');
assert.equal(fb.textContent, 'Заполнить форму');
assert.equal(root.querySelector('.m.o[data-id="5"]').textContent.includes('ol=k1'), false, 'the link line is not shown');
assert.equal(fw.hidden, true);
fb.click();
assert.equal(fw.hidden, false, 'the form opens inside the chat');
assert.equal(fw.querySelector('iframe').src, 'https://go24.test/cf/T?embed=1&ol=k1');
next = { messages: [{ id: 6, operator: 'Магазин', body: '✓ Форма «Заявка» заполнена', form_done: 'f1' }] };
await say('Готово');
assert.equal(fb.disabled, true, 'the filled form is not filled again');
assert.equal(fb.textContent, '✓ Форма заполнена');
await new Promise(r => setTimeout(r, 1600));
assert.equal(fw.hidden, true, 'the form closes after sending');
fb.disabled = false; fw.querySelector('.x').click();
assert.equal(fw.hidden, true);

// the line's chat bot: its menu as buttons, a button sends its item; only the last menu is live
next = { messages: [{ id: 7, operator: 'Магазин', body: 'Выберите:', buttons: ['1. Оплата', '2. Оператор'] }] };
await say('Привет');
const bm = root.querySelector('.m.o[data-id="7"]');
assert.equal(bm.childNodes[1].textContent, 'Выберите:', 'the text without the menu\'s lines');
assert.deepEqual([...bm.querySelectorAll('.bb')].map(b => b.textContent), ['1. Оплата', '2. Оператор']);
next = { messages: [{ id: 8, operator: '', body: '1. Оплата' }, { id: 9, operator: 'Магазин', body: 'Картой', buttons: ['1. Оплата', '2. Оператор'] }] };
bm.querySelector('.bb').click();
await tick();
assert.deepEqual(calls.filter(c => c.op === 'say').at(-1).body, { body: '1. Оплата' }, 'the button sends its item');
assert.equal(bm.querySelector('.bb').disabled, true, 'the earlier menu is off');
assert.equal(root.querySelector('.m.o[data-id="9"] .bb').disabled, false);

// «Согласие на обработку персональных данных»: the first message waits for «Принимаю», then goes; asked once
{
  const d2 = new JSDOM('<!doctype html><body></body>', { runScripts: 'outside-only', url: 'https://shop.example/', pretendToBeVisual: true });
  const w2 = d2.window, ops = [];
  w2.fetch = async (url, opts) => {
    const op = url.split('/api/')[1].split('?')[0];
    ops.push(op);
    return { ok: true, json: async () => (op === 'visit' ? { token: 't2' } : op === 'messages' ? { messages: [], consent: true } : {}) };
  };
  w2.eval(src.replace('/*CONFIG*/{}', JSON.stringify({ ...cfg, consent: 'Даю согласие на обработку данных', telegram: 'shop_bot' })));
  const r2 = w2.document.body.lastElementChild.shadowRoot, agree = r2.querySelector('.agree');
  assert.equal(agree.hidden, true);
  assert.match(agree.textContent, /Даю согласие/);
  assert.equal(r2.querySelector('.head .tg').href, 'https://t.me/shop_bot', 'the line\'s Telegram bot as another way in');
  r2.querySelector('.say textarea').value = 'Здравствуйте';
  r2.querySelector('.say').dispatchEvent(new w2.Event('submit', { bubbles: true, cancelable: true }));
  await tick();
  assert.equal(agree.hidden, false, 'the agreement is asked first');
  assert.deepEqual(ops, [], 'nothing goes before the consent');
  agree.querySelector('.send').click();
  await tick();
  assert.equal(agree.hidden, true);
  assert.deepEqual(ops.slice(0, 3), ['visit', 'consent', 'say']);
  r2.querySelector('.say textarea').value = 'Ещё';
  r2.querySelector('.say').dispatchEvent(new w2.Event('submit', { bubbles: true, cancelable: true }));
  await tick();
  assert.equal(agree.hidden, true, 'asked once');
  assert.equal(ops.filter(o => o === 'consent').length, 1);
  assert.equal(ops.filter(o => o === 'say').length, 2);
  assert.match(r2.innerHTML, /\[hidden\]\{display:none!important\}/, 'hidden blocks stay hidden whatever their display');
}

// «Правила показа»: the corner, the pages (only / except, «*»), the delay, «Автоприглашение», no button off hours
{
  const page = (url, extra, token) => {
    const d = new JSDOM('<!doctype html><body></body>', { runScripts: 'outside-only', url, pretendToBeVisual: true });
    if (token) d.window.localStorage.setItem('go24.widget.w1', token);
    d.window.fetch = async () => ({ ok: true, json: async () => ({ messages: [] }) });
    d.window.eval(src.replace('/*CONFIG*/{}', JSON.stringify({ ...cfg, t: { ...cfg.t, close: 'Закрыть' }, ...extra })));
    const h = d.window.document.body.lastElementChild;
    return h && h.shadowRoot ? h : null;
  };
  const only = { pages_mode: 'only', pages: ['https://shop.example/catalog/*', 'https://shop.example/about'] };
  assert.ok(page('https://shop.example/catalog/shoes?x=1', only), 'a page by the mask');
  assert.ok(page('https://shop.example/About/#team', only), 'as is, ignoring the case, the «/» and the anchor');
  assert.equal(page('https://shop.example/', only), null, 'not on the other pages');
  assert.equal(page('https://shop.example/cart', { pages_mode: 'except', pages: ['*/cart*'] }), null, 'not on the excluded');
  assert.ok(page('https://shop.example/', { pages_mode: 'except', pages: ['*/cart*'] }));
  assert.equal(page('https://shop.example/', only), null);
  assert.ok(page('https://shop.example/', { ...only, offline: true }, 'tok'), 'a started dialogue shows anywhere');
  assert.equal(page('https://shop.example/', { offline: true }), null, 'no button out of working hours');
  const tl = page('https://shop.example/', { position: 'top-left' });
  assert.match(tl.style.cssText, /left: 20px/);
  assert.match(tl.style.cssText, /top: 20px/);
  assert.ok(tl.shadowRoot.querySelector('.box').classList.contains('top'), 'the chat opens below the top button');
  const late = page('https://shop.example/', { show_delay: 0.05, hello: 'Подсказать?', hello_delay: 0.05, hello_photo: 'https://go24.test/w/w1/photo/u1' });
  assert.equal(late.style.display, 'none', 'the button waits');
  const hello = late.shadowRoot.querySelector('.hello');
  assert.equal(hello.hidden, true);
  await new Promise(r => setTimeout(r, 70));
  assert.equal(late.style.display, '', 'and shows');
  assert.equal(hello.hidden, true, 'the invitation waits for its own delay');
  await new Promise(r => setTimeout(r, 60));
  assert.equal(hello.hidden, false, 'the operator invites');
  assert.match(hello.textContent, /МагазинПодсказать\?/);
  assert.equal(hello.querySelector('img').src, 'https://go24.test/w/w1/photo/u1');
  hello.click();
  assert.equal(hello.hidden, true);
  assert.equal(late.shadowRoot.querySelector('.box').hidden, false, 'a click opens the chat');
  assert.equal(late.ownerDocument.defaultView.sessionStorage.getItem('go24.widget.w1.hello'), '1', 'once a visit');
}
console.log('widget.js: display rules, chat, rating, conversation by e-mail, contact form, files, typing, read, CRM form, chat bot and consent passed');
process.exit(0);
