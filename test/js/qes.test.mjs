// «Подпись» with КЭП: the certificates of the CryptoPro plug-in in the list, the PDF signed on submitting, in jsdom
// with a fake plug-in. node test/js/qes.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/qes.js', import.meta.url), 'utf8');
const dom = new JSDOM(`<!doctype html><body><form method="post" action="/sign/d1/sign">
  <div data-qes data-pdf="/sign/d1/file/pdf" data-no-plugin="нет плагина" data-no-certs="нет сертификатов" data-pick="выберите"
    data-signing="подписываем" data-until="до" data-failed="не удалось">
    <select data-qes-cert disabled><option value="">…</option></select><p data-qes-msg hidden></p><input type="hidden" name="cms"></div>
  <input type="checkbox" name="agree" value="1" checked><button>Подписать</button></form></body>`,
{ runScripts: 'outside-only', url: 'http://go24.test/sign/d1' });
const w = dom.window, doc = w.document;
const day = 864e5, now = Date.now();
const cert = (subject, from, to, key = true) => ({ SubjectName: subject, IssuerName: 'CN="Тестовый УЦ", O=УЦ',
  ValidFromDate: new Date(from).toISOString(), ValidToDate: new Date(to).toISOString(), HasPrivateKey: async () => key });
const certs = [cert('CN=Старый, O=X', now - 9 * day, now - day), cert('CN="ООО ""Ромашка""", SN=Иванов', now - day, now + 9 * day),
  cert('CN=Без ключа', now - day, now + day, false)];
let signed = null;
const objects = {
  'CAdESCOM.Store': () => ({ Open: async () => {}, Close: async () => {}, Certificates: { Count: certs.length, Item: async (i) => certs[i - 1] } }),
  'CAdESCOM.CPSigner': () => { const s = { AuthenticatedAttributes2: { Add: async () => {} } }; s.propset_Certificate = async (c) => { s.cert = c; }; return s; },
  'CADESCOM.CPAttribute': () => ({ propset_Name: async () => {}, propset_Value: async () => {} }),
  'CAdESCOM.CadesSignedData': () => { const d = {}; d.propset_ContentEncoding = async () => {}; d.propset_Content = async (c) => { d.content = c; };
    d.SignCades = async (s, kind, detached) => { signed = { content: d.content, cert: s.cert, kind, detached }; return 'MIIBsig=='; }; return d; },
};
const plugin = Promise.resolve();
Object.assign(plugin, { CAPICOM_CURRENT_USER_STORE: 2, CAPICOM_MY_STORE: 'My', CAPICOM_STORE_OPEN_MAXIMUM_ALLOWED: 2,
  CADESCOM_BASE64_TO_BINARY: 1, CADESCOM_CADES_BES: 1, CAPICOM_AUTHENTICATED_ATTRIBUTE_SIGNING_TIME: 0,
  CreateObjectAsync: async (name) => objects[name]() });
w.cadesplugin = plugin;
w.fetch = async (url) => { assert.equal(url, '/sign/d1/file/pdf'); return { ok: true, arrayBuffer: async () => new TextEncoder().encode('%PDF').buffer }; };
let submitted = 0;
w.HTMLFormElement.prototype.submit = function () { submitted++; };
w.eval(script);
const tick = () => new Promise((r) => setTimeout(r, 0));
for (let i = 0; i < 20; i++) await tick();

const select = doc.querySelector('[data-qes-cert]');
assert.equal(select.disabled, false, 'the certificates are loaded');
assert.equal(select.options.length, 1, 'only the valid one with a key');
assert.match(select.options[0].textContent, /^ООО "Ромашка" — до .+ \(Тестовый УЦ\)$/);
const form = doc.querySelector('form');
form.dispatchEvent(new w.Event('submit', { cancelable: true }));
for (let i = 0; i < 20; i++) await tick();
assert.ok(signed, 'signed by the plug-in');
assert.equal(signed.content, 'JVBERg==', 'the PDF in base64');
assert.equal(signed.cert, certs[1]);
assert.equal(signed.detached, true, 'a detached signature');
assert.equal(doc.querySelector('[name=cms]').value, 'MIIBsig==');
assert.equal(submitted, 1, 'the form is sent with the signature');
console.log('qes: ok');
