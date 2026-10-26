// «Виджет на сайт» in jsdom, as on a stranger's page: the button opens the chat, a message starts the dialogue
// (token kept in the site's storage), the operator's answer shows, the visitor rates it and gets the conversation by
// e-mail, the contact form sends. node test/js/widget.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const src = readFileSync(new URL('../../internal/web/widget/widget.js', import.meta.url), 'utf8');
const cfg = { id: 'w1', base: 'https://go24.test', color: '2FC6F6', name: 'Магазин', greeting: 'Здравствуйте!',
  t: { open: 'Напишите', chat: 'Чат', contact: 'Контакты', placeholder: '…', contact_note: 'note', name: 'Имя', phone: 'Тел', email: 'Почта', question: 'Вопрос', send: 'Отправить', thanks: 'Спасибо!',
  vote: 'Оцените', vote_comment: 'Комментарий', vote_thanks: 'Спасибо за оценку', history: 'На почту', history_note: 'Пришлём', history_sent: 'Отправлено' } };
const dom = new JSDOM('<!doctype html><body><h1>Магазин</h1></body>', { runScripts: 'outside-only', url: 'https://shop.example/catalog', pretendToBeVisual: true });
const w = dom.window;
const calls = [];
w.fetch = async (url, opts) => {
  const op = url.split('/api/')[1].split('?')[0];
  calls.push({ op, headers: opts.headers, body: opts.body ? JSON.parse(opts.body) : null });
  const answers = { visit: { token: 'tok123' }, say: {}, contact: {}, vote: {}, history: {},
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
console.log('widget.js: chat, rating, conversation by e-mail and contact form passed');
process.exit(0);
