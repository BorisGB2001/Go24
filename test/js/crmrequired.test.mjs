// CRM «Обязательные поля»: the stage bar asks for the fields required at a stage, in jsdom. node test/js/crmrequired.test.mjs
import { JSDOM, VirtualConsole } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/crmrequired.js', import.meta.url), 'utf8');
// jsdom cannot reload a page: it reports that, which is counted as the reload
let reloaded = 0;
const virtualConsole = new VirtualConsole();
virtualConsole.on('jsdomError', e => { if (/navigation|reload/i.test(e.message)) reloaded++; else console.error(e); });
const dom = new JSDOM(`<!doctype html><body><div class="content">
  <span hidden data-crmreq data-title="Заполните обязательные поля" data-save="Сохранить" data-cancel="Отмена"></span>
  <form method="post" action="/crm/deals/d1/stage" data-native data-crmstage><input type="hidden" name="stage" value="s3"><button>Предоплата</button></form>
  </div></body>`, { runScripts: 'outside-only', url: 'http://go24.test/crm/deals/d1', virtualConsole });
const w = dom.window;
// jsdom has no modal dialogs: open shows it, close fires «close»
w.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
w.HTMLDialogElement.prototype.close = function () { this.open = false; this.dispatchEvent(new w.Event('close')); };
const sent = [];
let answers = [];
w.fetch = async (url, opts) => {
  sent.push(Object.fromEntries(new URLSearchParams(opts.body)));
  const a = answers.shift();
  return { ok: a.status < 300, status: a.status, json: async () => a.body };
};
w.eval(script);
const tick = () => new Promise(r => setTimeout(r, 0));
const form = w.document.querySelector('form[data-crmstage]');
const need = { status: 422, body: { error: 'Заполните обязательные поля: Договор', fields: [
  { id: 'f1', name: 'Договор', kind: 'string', options: [], value: '' },
  { id: 'f2', name: 'Регион', kind: 'list', options: ['Москва', 'Тверь'], value: '' }] } };

// refused: the dialog asks for both fields; filled and saved — the move is sent again with them
answers = [need, { status: 204, body: {} }];
form.requestSubmit();
await tick(); await tick();
const dlg = w.document.querySelector('dialog.crm-dlg');
assert.ok(dlg?.open, 'the dialog is open');
assert.equal(dlg.querySelector('h3').textContent, 'Заполните обязательные поля');
const f1 = dlg.querySelector('[name=f_f1]'), f2 = dlg.querySelector('[name=f_f2]');
assert.ok(f1.required && f2.required);
assert.deepEqual([...f2.options].map(o => o.value), ['', 'Москва', 'Тверь']);
f1.value = '№ 7'; f2.value = 'Тверь';
dlg.querySelector('form').dispatchEvent(new w.Event('submit', { cancelable: true })); // method=dialog: then the dialog closes
dlg.close();
await tick(); await tick();
assert.deepEqual(sent, [{ stage: 's3' }, { stage: 's3', f_f1: '№ 7', f_f2: 'Тверь' }]);
assert.equal(reloaded, 1);
assert.equal(w.document.querySelector('dialog'), null, 'the dialog is gone');

// cancelled: nothing more is sent, the page stays
sent.length = 0; answers = [need];
form.requestSubmit();
await tick(); await tick();
w.document.querySelector('dialog.crm-dlg button[type=button]').click();
await tick();
assert.deepEqual(sent, [{ stage: 's3' }]);
assert.equal(reloaded, 1);

// another refusal shows its message
sent.length = 0; answers = [{ status: 403, body: { error: 'Нет прав' } }];
form.requestSubmit();
await tick(); await tick();
assert.equal(w.document.querySelector('.content .flash').textContent, 'Нет прав');
console.log('crmrequired.test.mjs: ok');
