// «Открытые линии → Виджеты», the chat bot's menu editor (static/js/olbot.js) in jsdom: the saved tree is shown, items
// and submenu items are added and removed, a submenu's block shows only for «Вложенное меню»; on saving the tree goes to
// bot_menu as JSON (empty items are dropped).
// node test/js/olbot.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const src = readFileSync(new URL('../../internal/web/static/js/olbot.js', import.meta.url), 'utf8');
const menu = [{ title: 'Оплата', action: 'menu', items: [{ title: 'Картой', action: 'text', text: 'На сайте' }] }, { title: 'Оператор', action: 'queue' }];
const dom = new JSDOM(`<!doctype html><body><form><details data-olbot>
  <input type="hidden" name="bot_menu" value='${JSON.stringify(menu)}'>
  <div data-olbot-list></div><button type="button" data-olbot-add>+</button>
  <template data-olbot-item><div class="olbot-item">
    <div class="row"><input type="text" data-k="title"><select data-k="action"><option value="text">t</option><option value="menu">m</option>
      <option value="queue">q</option><option value="close">c</option></select><button type="button" data-olbot-del>x</button></div>
    <textarea data-k="text" data-ph-text="Текст" data-ph-menu="Заголовок" data-ph-queue="Соединяю" data-ph-close="Пока"></textarea>
    <div class="olbot-sub"><div data-olbot-list></div><button type="button" data-olbot-add>+ sub</button></div>
  </div></template></details></form></body>`, { runScripts: 'outside-only' });
const w = dom.window, d = w.document;
w.eval(src);
const box = d.querySelector('[data-olbot]'), top = box.querySelector(':scope > [data-olbot-list]');
const items = top.querySelectorAll(':scope > .olbot-item');
assert.equal(items.length, 2, 'the saved items');
assert.equal(items[0].querySelector('[data-k="title"]').value, 'Оплата');
assert.equal(items[0].querySelector('.olbot-sub').hidden, false, 'a submenu shows its items');
assert.equal(items[0].querySelector('.olbot-sub .olbot-item [data-k="text"]').value, 'На сайте');
assert.equal(items[1].querySelector('.olbot-sub').hidden, true, 'no submenu for «Соединить с оператором»');
assert.equal(items[1].querySelector('[data-k="text"]').placeholder, 'Соединяю');

// a new item turned into a submenu gets its first sub-item; an item is removed
box.querySelector(':scope > [data-olbot-add]').click();
const added = top.lastElementChild;
added.querySelector('[data-k="title"]').value = 'Доставка';
const act = added.querySelector('[data-k="action"]');
act.value = 'menu';
act.dispatchEvent(new w.Event('change'));
assert.equal(added.querySelectorAll('.olbot-sub .olbot-item').length, 1, 'a submenu starts with an item');
added.querySelector('.olbot-sub .olbot-item [data-k="title"]').value = 'Курьером';
added.querySelector('.olbot-sub .olbot-item [data-k="text"]').value = 'Завтра';
added.querySelector('.olbot-sub > [data-olbot-add]').click();
assert.equal(added.querySelectorAll('.olbot-sub .olbot-item').length, 2);
items[1].querySelector('[data-olbot-del]').click();

d.querySelector('form').dispatchEvent(new w.Event('submit'));
assert.deepEqual(JSON.parse(box.querySelector('[name="bot_menu"]').value), [
  { title: 'Оплата', action: 'menu', text: '', items: [{ title: 'Картой', action: 'text', text: 'На сайте' }] },
  { title: 'Доставка', action: 'menu', text: '', items: [{ title: 'Курьером', action: 'text', text: 'Завтра' }] },
], 'the tree as JSON, the empty sub-item dropped');
console.log('olbot.js: the chat bot menu editor passed');
process.exit(0);
