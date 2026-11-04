// «Колл-трекинг» on a client's site in jsdom: a visitor of a channel (utm_source, or the site they came from) sees
// the channel's number instead of the company's — in the text, in tel: links and in .go24-phone; the channel is kept
// for the next pages; a visitor of no channel sees the page as it is; other numbers stay.
// node test/js/calltrack.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const src = readFileSync(new URL('../../internal/web/widget/calltrack.js', import.meta.url), 'utf8');
const cfg = { numbers: ['+74951234567', '+74957654321', '+74950000000'], lines: [
  { number: '+74957654321', text: '+7 495 765-43-21', channels: ['yandex', 'ya-direct'] },
  { number: '+74950000000', text: '+7 495 000-00-00', channels: ['vk'] }] };
const html = `<!doctype html><body><p id="a">Звоните: 8 (495) 123-45-67, бухгалтерия +7 812 111-22-33</p>
  <a id="b" href="tel:+74951234567">+7 495 123 45 67</a><span id="c" class="go24-phone">позвонить</span></body>`;
const page = async (url, referrer, storage) => {
  const dom = new JSDOM(html, { runScripts: 'outside-only', url, referrer });
  if (storage) dom.window.localStorage.setItem('go24ct', storage);
  dom.window.eval(src.replace('/*CONFIG*/{}', JSON.stringify(cfg)));
  await new Promise(r => setTimeout(r, 20)); // the page loaded
  return dom.window;
};
const text = (w, id) => w.document.getElementById(id).textContent;

// utm_source of the channel
let w = await page('https://shop.example/?utm_source=Yandex&utm_campaign=x');
assert.equal(text(w, 'a'), 'Звоните: +7 495 765-43-21, бухгалтерия +7 812 111-22-33', 'the company\'s number, not another one');
assert.equal(w.document.getElementById('b').getAttribute('href'), 'tel:+74957654321');
assert.equal(text(w, 'b'), '+7 495 765-43-21');
assert.equal(text(w, 'c'), '+7 495 765-43-21');
assert.equal(w.go24CallTrack.number, '+74957654321');
const kept = w.localStorage.getItem('go24ct');
assert.equal(JSON.parse(kept).ch, 'Yandex', 'the channel is kept');

// the next page of the same visitor, without tags: the kept channel
w = await page('https://shop.example/price', 'https://shop.example/', kept);
assert.equal(text(w, 'c'), '+7 495 765-43-21', 'the kept channel on the next page');

// kept long ago: no more
w = await page('https://shop.example/price', undefined, JSON.stringify({ ch: 'yandex', at: Date.now() - 31 * 864e5 }));
assert.equal(text(w, 'c'), 'позвонить', 'a channel of 31 days ago is forgotten');

// came from a site of a channel
w = await page('https://shop.example/', 'https://m.vk.com/wall-1');
assert.equal(text(w, 'c'), '+7 495 000-00-00', 'the site the visitor came from');
assert.equal(w.document.getElementById('b').getAttribute('href'), 'tel:+74950000000');

// a visitor of no channel: the page as it is, nothing kept
w = await page('https://shop.example/?utm_source=google', 'https://www.google.com/');
assert.equal(text(w, 'a'), 'Звоните: 8 (495) 123-45-67, бухгалтерия +7 812 111-22-33');
assert.equal(w.document.getElementById('b').getAttribute('href'), 'tel:+74951234567');
assert.equal(text(w, 'c'), 'позвонить');
assert.equal(w.localStorage.getItem('go24ct'), null);
assert.equal(w.go24CallTrack, undefined);

console.log('calltrack: ok');
