// «Иммерсивное средство чтения» of a letter (static/js/mailread.js) in jsdom: the menu item opens the letter over the
// page — the subject, the sender, paragraphs, a list, a quote, a picture (not a tracking pixel), each word clickable;
// the text preferences change the reader and stay for the next letter; the line focus shows its masks; reading
// aloud goes paragraph by paragraph, the word the voice says is highlighted, a click on a word reads from it, the
// space pauses; Esc closes the open menu first, then the reader, and stops the voice.
// node test/js/mailread.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/mailread.js', import.meta.url), 'utf8');
const wait = ms => new Promise(r => setTimeout(r, ms));

const ui = `<template data-immersive-ui><div class="mimm" role="dialog">
  <div class="mimm-bar"><span class="mimm-voice"><button data-imm="play">▶</button>
    <select data-imm-rate><option value="0.5">0.5</option><option value="1">1</option><option value="2">2</option></select><select data-imm-voice hidden></select></span>
    <details class="mimm-menu" id="tm"><summary>Текст</summary><div><input type="range" min="16" max="56" step="2" data-imm-set="size">
      <input type="checkbox" data-imm-set="spacing"><select data-imm-set="font"><option value="sans">sans</option><option value="serif">serif</option></select>
      <select data-imm-set="width"><option value="narrow">n</option><option value="medium">m</option><option value="wide">w</option></select>
      <button data-imm-theme="auto">Aa</button><button data-imm-theme="sepia">Aa</button></div></details>
    <details class="mimm-menu"><summary>Чтение</summary><div>${['0', '1', '3', '5'].map(v => `<input type="radio" name="f" value="${v}" data-imm-set="focus">`).join('')}</div></details>
    <button data-imm="close">×</button></div>
  <div class="mimm-scroll" data-imm-scroll tabindex="-1"><article class="mimm-text" data-imm-text></article></div>
  <div class="mimm-mask" data-imm-mask="top" hidden></div><div class="mimm-mask" data-imm-mask="bottom" hidden></div>
  <span hidden data-i="play">PLAY</span><span hidden data-i="pause">PAUSE</span><span hidden data-l="pause">Пауза</span><span hidden data-l="play">Читать</span></div></template>`;

const page = `<!doctype html><html lang="ru"><body>
<article class="mailletter" data-letter="m1"><h2>Отчёт  за октябрь</h2>
  <div class="row mread-actions"><details open id="more"><div><button type="button" data-immersive>Иммерсивное средство чтения</button></div></details></div>
  <details class="mmsg" open data-msg="m1"><summary><div class="mmsg-who"><b>Анна Петрова</b></div></summary>
    <div class="mailbody">Коллеги, привет.<br>Отчёт <b>во вложении</b>.<div><h1>Итоги</h1><ul><li>продажи растут</li><li>расходы <i>падают</i></li></ul></div>
      <blockquote>Старое письмо</blockquote><img src="/files/f1" alt="график"><img src="https://t.example/p.gif" width="1" height="1"><style>p{}</style></div></details></article>
${ui}</body></html>`;

const dom = new JSDOM(page, { runScripts: 'outside-only', url: 'http://go24.test/mail', pretendToBeVisual: true });
const w = dom.window, d = w.document;
const store = new Map();
Object.defineProperty(w, 'localStorage', { value: { getItem: k => store.get(k) ?? null, setItem: (k, v) => store.set(k, String(v)) } });
w.HTMLElement.prototype.scrollIntoView = function () {};

// the voices of the browser: each utterance tells its words, then ends
const spoken = [];
let cancels = 0;
w.SpeechSynthesisUtterance = class { constructor(t) { this.text = t; } };
w.speechSynthesis = { getVoices: () => [], cancel() { cancels++; }, pause() {}, resume() {},
  speak(u) {
    spoken.push(u);
    if (w.__hold) return;
    const sp = u.text.indexOf(' ');
    setTimeout(() => u.onboundary?.({ name: 'word', charIndex: 0 }), 1);
    if (sp > 0) setTimeout(() => u.onboundary?.({ name: 'word', charIndex: sp + 1 }), 3);
    setTimeout(() => u.onend?.(), 5);
  } };

w.eval(script);

const click = el => el.dispatchEvent(new w.MouseEvent('click', { bubbles: true }));
const key = (k, el = d.activeElement || d.body) => el.dispatchEvent(new w.KeyboardEvent('keydown', { key: k, bubbles: true }));
const box = () => d.querySelector('body > .mimm');

// ---------- opening ----------
click(d.querySelector('[data-immersive]'));
assert.ok(box(), 'the reader is open');
assert.equal(d.getElementById('more').hasAttribute('open'), false, 'the menu closed');
assert.ok(d.documentElement.classList.contains('mimm-open'), 'the page under it does not scroll');
const text = box().querySelector('[data-imm-text]');
assert.equal(text.querySelector('h1').textContent, 'Отчёт за октябрь');
assert.equal(text.querySelector('[data-kind="who"]').textContent, 'Анна Петрова');
const ps = [...text.querySelectorAll('.mimm-p')].map(p => p.tagName + ':' + p.dataset.text);
assert.deepEqual(ps, ['H1:Отчёт за октябрь', 'P:Анна Петрова', 'P:Коллеги, привет.', 'P:Отчёт во вложении.', 'H3:Итоги',
  'LI:продажи растут', 'LI:расходы падают', 'BLOCKQUOTE:Старое письмо']);
assert.equal(text.querySelectorAll('ul > li').length, 2, 'the list items are one list');
const imgs = text.querySelectorAll('img');
assert.equal(imgs.length, 1, 'the picture is kept, the tracking pixel is not');
assert.match(imgs[0].src, /\/files\/f1$/);
const words = [...text.querySelectorAll('p:nth-of-type(2) .mimm-w')].map(x => x.textContent + '@' + x.dataset.o);
assert.deepEqual(words, ['Коллеги,@0', 'привет.@9'], 'each word knows where it starts');
assert.equal(box().dataset.theme, 'auto');
assert.equal(box().style.getPropertyValue('--imm-size'), '24px');
assert.equal(box().querySelector('[data-imm-set="size"]').value, '24');

// ---------- text and reading preferences ----------
const size = box().querySelector('[data-imm-set="size"]');
size.value = '36';
size.dispatchEvent(new w.Event('input', { bubbles: true }));
assert.equal(box().style.getPropertyValue('--imm-size'), '36px');
const spacing = box().querySelector('[data-imm-set="spacing"]');
spacing.checked = true;
spacing.dispatchEvent(new w.Event('change', { bubbles: true }));
assert.ok(box().classList.contains('mimm-wide'));
const font = box().querySelector('[data-imm-set="font"]');
font.value = 'serif';
font.dispatchEvent(new w.Event('change', { bubbles: true }));
assert.equal(box().dataset.font, 'serif');
click(box().querySelector('[data-imm-theme="sepia"]'));
assert.equal(box().dataset.theme, 'sepia');
assert.equal(box().querySelector('[data-imm-theme="sepia"]').getAttribute('aria-pressed'), 'true');
const three = box().querySelector('[data-imm-set="focus"][value="3"]');
three.checked = true;
three.dispatchEvent(new w.Event('change', { bubbles: true }));
assert.equal(box().dataset.focus, '3');
assert.equal(box().querySelector('[data-imm-mask="top"]').hidden, false, 'line focus dims above');
assert.equal(box().querySelector('[data-imm-mask="bottom"]').hidden, false, 'and below');
const kept = JSON.parse(store.get('go24-immersive'));
assert.deepEqual([kept.size, kept.spacing, kept.font, kept.theme, kept.focus], [36, true, 'serif', 'sepia', 3]);

// ---------- Esc: the open menu first, then the reader ----------
d.getElementById('tm').setAttribute('open', '');
key('Escape');
assert.equal(d.getElementById('tm').hasAttribute('open'), false, 'Esc closed the menu');
assert.ok(box(), 'the reader is still open');
key('Escape');
assert.equal(box(), null, 'Esc closed the reader');
assert.equal(d.documentElement.classList.contains('mimm-open'), false);

// the preferences stay for the next time
click(d.querySelector('[data-immersive]'));
assert.equal(box().dataset.theme, 'sepia');
assert.equal(box().style.getPropertyValue('--imm-size'), '36px');
assert.equal(box().querySelector('[data-imm-set="spacing"]').checked, true);
assert.equal(box().querySelector('[data-imm-set="focus"][value="3"]').checked, true);
// line focus off
const off = box().querySelector('[data-imm-set="focus"][value="0"]');
off.checked = true;
off.dispatchEvent(new w.Event('change', { bubbles: true }));
assert.equal(box().querySelector('[data-imm-mask="top"]').hidden, true);

// ---------- reading aloud ----------
const marks = [];
const t2 = box().querySelector('[data-imm-text]');
new w.MutationObserver(() => { const on = t2.querySelector('.mimm-on'); if (on && marks.at(-1) !== on.textContent) marks.push(on.textContent); })
  .observe(t2, { subtree: true, attributes: true, attributeFilter: ['class'] });
click(box().querySelector('[data-imm="play"]'));
assert.equal(box().querySelector('[data-imm="play"]').textContent, 'PAUSE');
for (let i = 0; i < 100 && box().querySelector('[data-imm="play"]').textContent !== 'PLAY'; i++) await wait(5);
assert.deepEqual(spoken.map(u => u.text.trim()), ['Отчёт за октябрь', 'Анна Петрова', 'Коллеги, привет.', 'Отчёт во вложении.', 'Итоги',
  'продажи растут', 'расходы падают', 'Старое письмо'], 'the whole letter, paragraph by paragraph');
assert.equal(spoken[0].lang, 'ru');
assert.ok(marks.includes('за') && marks.includes('привет.') && marks.includes('растут'), 'the words said are highlighted: ' + marks);
assert.equal(t2.querySelector('.mimm-on, .mimm-reading'), null, 'the end: nothing highlighted');

// a click on a word reads from it, at the rate chosen
const rate = box().querySelector('[data-imm-rate]');
rate.value = '2';
rate.dispatchEvent(new w.Event('change', { bubbles: true }));
w.__hold = true;
spoken.length = 0;
click([...t2.querySelectorAll('.mimm-w')].find(x => x.textContent === 'во'));
assert.equal(spoken.length, 1);
assert.equal(spoken[0].text, 'во вложении.', 'from the word clicked');
assert.equal(spoken[0].rate, 2);
assert.equal(t2.querySelector('.mimm-on').textContent, 'во');
assert.ok(t2.querySelector('.mimm-reading').dataset.text.startsWith('Отчёт во'));
// the voice tells the next word: the highlight follows, counted from the word clicked
spoken[0].onboundary({ name: 'word', charIndex: 3 });
assert.equal(t2.querySelector('.mimm-on').textContent, 'вложении.');
// the space pauses; a stale end of the stopped utterance does not go on
const c0 = cancels;
key(' ', box().querySelector('[data-imm-scroll]'));
assert.equal(box().querySelector('[data-imm="play"]').textContent, 'PLAY');
assert.ok(cancels > c0, 'the voice stopped');
spoken[0].onend?.();
await wait(10);
assert.equal(spoken.length, 1, 'nothing more is said after the pause');
// the space again resumes from the word it stopped at
key(' ', box().querySelector('[data-imm-scroll]'));
assert.equal(spoken.at(-1).text, 'вложении.');
// closing stops the voice
const c1 = cancels;
click(box().querySelector('[data-imm="close"]'));
assert.equal(box(), null);
assert.ok(cancels > c1);

console.log('mailread: ok');
