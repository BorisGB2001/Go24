// Mail «Проверка читаемости», as Outlook's Accessibility Checker (static/js/compose.js): the pane lists the errors
// (a picture without alternative text or with a file name for it, a table without a header row), the warnings (merged
// cells, a link saying nothing, text blending with its background) and the tips (blank paragraphs in a row) of the
// letter's own text, not of the quote; each is fixed in the pane; «Проверять читаемость перед отправкой» asks first.
// node test/js/mailaccess.test.mjs
import { JSDOM, VirtualConsole } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/compose.js', import.meta.url), 'utf8');
const L = { title: 'Проверка читаемости', errors: 'Ошибки', warnings: 'Предупреждения', tips: 'Советы', none: 'Проблем не найдено',
  alt: 'Нет замещающего текста у рисунка', alt_file: 'Замещающий текст рисунка — имя файла', alt_why: 'Опишите рисунок', alt_ph: 'Что на рисунке',
  decorative: 'Декоративный', header: 'Нет строки заголовка у таблицы', header_why: 'Заголовки', header_fix: 'Первая строка — заголовок',
  merged: 'Объединённые ячейки', merged_why: 'Трудно', link: 'Непонятный текст ссылки', link_why: 'Куда', link_ph: 'Куда ведёт',
  link_words: 'здесь|тут|ссылк[аеиу]|click here|here', contrast: 'Плохо читаемый цвет', contrast_why: 'Контрастность %s:1', contrast_fix: 'Обычный цвет',
  blank: 'Пустые строки', blank_why: 'Пусто', blank_fix: 'Убрать лишние', apply: 'Применить', close: 'Закрыть', ask: 'Ошибки: %s. Отправить?' };
const labels = `<template data-a11y-labels>${Object.entries(L).map(([k, v]) => `<span data-l="${k}">${v}</span>`).join('')}</template>`;
const letter = `<p>Добрый день</p><p><img src="/files/a" alt="scan_0012.png"> <img src="/files/b"> <img src="/files/logo" alt=""> <img src="/t" width="1" height="1"></p>
<table><tr><td>Товар</td><td>Цена</td></tr><tr><td colspan="2">итого</td></tr></table>
<p>Отчёт <a href="https://x.test/r">здесь</a>, договор — <a href="https://x.test/d">Договор поставки</a>.</p>
<p><span style="color: #dddddd">бледно</span> <span style="color: #ffffff; background-color: #000000">белым по чёрному</span></p>
<div><br></div><div><br></div><div><br></div><p>Анна</p>
<hr><div data-quote="1"><img src="/files/q"><a href="https://y.test">здесь</a><div><br></div><div><br></div><div><br></div></div>`;
const page = `<!doctype html><body><form method="post" action="/mail/drafts/send" data-compose data-back="/mail" data-a11y-send>
  <input type="hidden" name="draft" value="d1"><input type="hidden" name="mode" value="">
  <span class="mc-send"><button class="primary">Отправить</button></span>
  <button type="button" data-a11y-check aria-pressed="false">a11y</button><span data-status></span>
  <input type="hidden" name="box" value="b1" data-box>
  <div class="mc-rcpt" data-rcpt><input type="text" name="to" value="c@x.ru"></div>
  <input type="text" name="subject" value="Отчёт">
  <div class="mc-files" data-files></div>
  <div class="mc-a11y" data-a11y hidden>${labels}</div>
  <div class="mc-editor" contenteditable="true" data-editor></div>
  <textarea name="html" hidden data-html>${letter.replace(/&/g, '&amp;').replace(/</g, '&lt;')}</textarea>
  <template data-compose-labels><span data-l="words">вложен</span></template>
</form></body>`;
const wait = ms => new Promise(r => setTimeout(r, ms));

const vc = new VirtualConsole();
vc.on('jsdomError', e => { if (!/navigation/i.test(e.message)) console.error(e); });
const dom = new JSDOM(page, { runScripts: 'outside-only', url: 'http://go24.test/mail', virtualConsole: vc });
const w = dom.window, d = w.document;
w.CSS = { escape: s => s };
d.execCommand = () => true;
d.queryCommandState = () => false;
const asked = [], sent = [];
w.confirm = text => { asked.push(text); return w.answer ?? true; };
w.fetch = async (url, opts = {}) => {
  const u = String(url);
  if (u === '/mail/drafts/send') sent.push(opts.body);
  const data = u === '/mail/drafts/send' ? { id: 'd1', undo: 0, href: '/mail' } : { id: 'd1', saved: '12:00' };
  return { ok: true, status: 200, statusText: 'OK', json: async () => data, text: async () => '' };
};
w.eval(script);

const pane = d.querySelector('[data-a11y]'), ed = d.querySelector('[data-editor]'), btn = d.querySelector('[data-a11y-check]');
const items = () => [...pane.querySelectorAll('li[data-issue]')].map(li => li.dataset.issue);
const item = kind => pane.querySelector(`li[data-issue="${kind}"]`);
const fix = (kind, text) => [...item(kind).querySelectorAll('.mc-a11y-fix button')].find(b => b.textContent === text).click();

{ // the pane: errors, warnings, tips of the letter's own text; the quote, the decorative picture and the pixel are not looked at
  assert.equal(pane.hidden, true);
  btn.click();
  assert.equal(pane.hidden, false);
  assert.equal(btn.getAttribute('aria-pressed'), 'true');
  assert.deepEqual(items(), ['alt_file', 'alt', 'header', 'merged', 'link', 'contrast', 'blank']);
  assert.deepEqual([...pane.querySelectorAll('.mc-a11y-level')].map(h => h.textContent), ['Ошибки (3)', 'Предупреждения (3)', 'Советы (1)']);
  assert.match(item('contrast').textContent, /Контрастность 1\.4:1/, 'white on black is fine, the pale grey is not');
}

{ // a click selects the place in the letter
  item('link').querySelector('button.link').click();
  assert.equal(w.getSelection().getRangeAt(0).cloneContents().textContent, 'здесь');
}

{ // the fixes: alternative text, decorative, header row, link text, colour, blank lines
  const alt = item('alt_file');
  alt.querySelector('input').value = 'Скан счёта';
  fix('alt_file', 'Применить');
  assert.equal(ed.querySelector('img[src="/files/a"]').alt, 'Скан счёта');
  fix('alt', 'Декоративный');
  assert.equal(ed.querySelector('img[src="/files/b"]').getAttribute('alt'), '', 'a decorative picture: alt=""');
  fix('header', 'Первая строка — заголовок');
  assert.deepEqual([...ed.querySelector('table').rows[0].cells].map(c => c.outerHTML), ['<th>Товар</th>', '<th>Цена</th>']);
  const link = item('link');
  link.querySelector('input').value = 'Отчёт за октябрь';
  link.querySelector('input').dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }));
  assert.equal(ed.querySelector('a[href="https://x.test/r"]').textContent, 'Отчёт за октябрь');
  fix('contrast', 'Обычный цвет');
  assert.match(ed.innerHTML, /<span>бледно<\/span>/);
  fix('blank', 'Убрать лишние');
  assert.equal(ed.querySelectorAll(':scope > div:not([data-quote])').length, 1, 'one blank line stays');
  assert.deepEqual(items(), ['merged'], 'merged cells are only shown');
}

{ // it follows the text: a new picture shows up a moment after
  ed.insertAdjacentHTML('afterbegin', '<p><img src="/files/c"></p>');
  ed.dispatchEvent(new w.Event('input', { bubbles: true }));
  await wait(350);
  assert.deepEqual(items(), ['alt', 'merged']);
}

{ // «Проверять читаемость перед отправкой»: «Нет» keeps the letter and shows the pane; «Да» sends
  pane.querySelector('[data-a11y-close]').click();
  assert.equal(pane.hidden, true);
  w.answer = false;
  d.querySelector('.mc-send button').click();
  await wait(20);
  assert.deepEqual(asked, ['Ошибки: Нет замещающего текста у рисунка. Отправить?']);
  assert.equal(sent.length, 0);
  assert.equal(pane.hidden, false, 'the pane opens on the errors');
  fix('alt', 'Декоративный');
  assert.deepEqual(items(), ['merged']);
  w.answer = undefined;
  d.querySelector('.mc-send button').click();
  await wait(50);
  assert.equal(asked.length, 1, 'no errors — no question (a warning does not stop the letter)');
  assert.equal(sent.length, 1);
}

{ // nothing to find
  if (pane.hidden) btn.click();
  ed.innerHTML = '<p>Спасибо!</p>';
  ed.dispatchEvent(new w.Event('input', { bubbles: true }));
  await wait(350);
  assert.deepEqual(items(), []);
  assert.equal(pane.querySelector('[data-a11y-none]').textContent, 'Проблем не найдено');
}
console.log('mailaccess.test.mjs: ok');
process.exit(0);
