// The softphone window (static/js/softphone.js) in jsdom with a stand-in JsSIP: «Перевести» shows while talking;
// a transfer at once sends REFER to the number and the window says where the call went; a transfer after talking to
// the colleague holds the client, calls the colleague, «Завершить перевод» sends REFER with Replaces of that call;
// the colleague gone — back to choosing, «Вернуться к клиенту» takes the client off hold.
// node test/js/softphone.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const code = readFileSync(new URL('../../internal/web/static/js/softphone.js', import.meta.url), 'utf8');
const dom = new JSDOM(`<!doctype html><body><div data-softphone data-id="0123456789abcdef" data-login="w101" data-password="pw" data-host="pbx"
  data-ready="Готов" data-ended="Звонок завершён" data-busy="Занято" data-failed="Ошибка: %s" data-ringing="Вызов" data-dialing="Набор"
  data-on-hold="На удержании" data-bad-number="Неверный номер" data-hold="Удержать" data-unhold="Снять" data-transferred="Переведён на %s"
  data-xfer-failed="Не удалось перевести" data-xfer-calling="Вызов: %s" data-xfer-talking="Разговор: %s" data-xfer-ended="Коллега не ответил">
  <span data-sp-dot></span><span data-sp-status></span>
  <div data-sp-who hidden><img data-sp-photo hidden><a data-sp-link><b data-sp-name></b></a><span data-sp-num></span><span data-sp-timer></span></div>
  <input data-sp-input><button data-sp-back></button><div data-sp-keys></div>
  <button data-sp-mute hidden></button><button data-sp-call><span data-sp-call-text data-call="Позвонить" data-answer="Ответить"></span></button>
  <button data-sp-hang hidden><span data-sp-hang-text data-hangup="Завершить" data-decline="Отклонить"></span></button>
  <button data-sp-hold hidden></button><button data-sp-xfer hidden></button>
  <div data-sp-xfer-panel hidden><div data-sp-xfer-ask><input data-sp-xfer-to><button data-sp-blind></button><button data-sp-consult></button>
    <button data-sp-xfer-cancel></button></div>
    <div data-sp-xfer-talk hidden><b data-sp-xfer-who></b><button data-sp-complete disabled></button><button data-sp-return></button></div></div>
  <audio data-sp-audio></audio><audio data-sp-audio2></audio></div></body>`, { runScripts: 'outside-only', url: 'https://go24.test/phone', pretendToBeVisual: true });
const w = dom.window, d = w.document;
const $ = s => d.querySelector(s);
const wait = ms => new Promise(r => setTimeout(r, ms));
const click = s => $(s).dispatchEvent(new w.MouseEvent('click', { bubbles: true }));

class Em {
  constructor() { this.h = {}; }
  on(e, f) { (this.h[e] ??= []).push(f); }
  emit(e, a) { (this.h[e] || []).forEach(f => f(a)); }
}
class Session extends Em {
  constructor(uri) {
    super();
    const n = uri.replace(/^sip:/, '').replace(/@.*/, '');
    this.held = false; this.refers = []; this.terminated = false;
    this.remote_identity = { uri: { user: n, toString: () => uri }, display_name: '' };
  }
  isOnHold() { return { local: this.held }; }
  hold() { this.held = true; }
  unhold() { this.held = false; }
  isMuted() { return { audio: false }; }
  refer(to, opts) { this.refers.push([String(to), opts]); }
  terminate() { this.terminated = true; this.emit('ended', { originator: 'local' }); }
}
const calls = [];
w.JsSIP = {
  WebSocketInterface: class {},
  UA: class extends Em { start() { this.emit('registered'); } call(uri) { const s = new Session(uri); calls.push(s); return s; } },
  C: { causes: { BUSY: 'Busy', REJECTED: 'Rejected', UNAVAILABLE: 'Unavailable', NOT_FOUND: 'Not Found', NO_ANSWER: 'No Answer', BYE: 'Terminated', CANCELED: 'Canceled', ANSWERED_ELSEWHERE: 'Elsewhere' } },
};
w.fetch = async () => ({ ok: true, json: async () => null });
w.EventSource = class { addEventListener() {} };
Object.defineProperty(w.navigator, 'mediaDevices', { value: { getUserMedia: async () => ({ getTracks: () => [] }) } });
delete w.BroadcastChannel;
w.eval(code);
await wait(20);

const talk = async number => {
  $('[data-sp-input]').value = number;
  click('[data-sp-call]');
  const s = calls.at(-1);
  s.emit('confirmed');
  await wait(0);
  return s;
};

// talking: «Перевести» is there; a transfer at once — REFER to the colleague, the window says where the call went
const first = await talk('+79001234567');
assert.equal($('[data-sp-xfer]').hidden, false, 'no «Перевести» while talking');
click('[data-sp-xfer]');
assert.equal($('[data-sp-xfer-panel]').hidden, false);
$('[data-sp-xfer-to]').value = 'abc';
click('[data-sp-blind]');
assert.equal(first.refers.length, 0, 'transferred to nonsense');
assert.equal($('[data-sp-status]').textContent, 'Неверный номер');
$('[data-sp-xfer-to]').value = '102';
click('[data-sp-blind]');
assert.deepEqual(first.refers.map(r => r[0]), ['sip:102@pbx']);
first.refers[0][1].eventHandlers.accepted();
first.emit('ended', { originator: 'remote', cause: 'Terminated' });
assert.equal($('[data-sp-status]').textContent, 'Переведён на 102');
assert.equal($('[data-sp-xfer-panel]').hidden, true, 'the panel stays after the call');
assert.equal($('[data-sp-xfer]').hidden, true);

// after talking to the colleague: the client held, the colleague called; «Завершить перевод» — REFER with Replaces
const second = await talk('+79007654321');
click('[data-sp-xfer]');
$('[data-sp-xfer-to]').value = '103';
click('[data-sp-consult]');
const colleague = calls.at(-1);
assert.equal(second.held, true, 'the client is not on hold');
assert.equal(colleague.remote_identity.uri.user, '103');
assert.equal($('[data-sp-xfer-talk]').hidden, false);
assert.equal($('[data-sp-xfer-who]').textContent, 'Вызов: 103');
assert.equal($('[data-sp-complete]').disabled, true, 'completed before the colleague answered');
assert.equal($('[data-sp-xfer]').hidden, true);
// the colleague did not answer: back to choosing
colleague.emit('failed', { originator: 'remote', cause: 'No Answer' });
assert.equal($('[data-sp-xfer-ask]').hidden, false);
assert.equal($('[data-sp-status]').textContent, 'Коллега не ответил');
assert.equal(second.held, true);
// again, answered: «Завершить перевод»
click('[data-sp-consult]');
const colleague2 = calls.at(-1);
colleague2.emit('confirmed');
assert.equal($('[data-sp-xfer-who]').textContent, 'Разговор: 103');
assert.equal($('[data-sp-complete]').disabled, false);
click('[data-sp-complete]');
assert.equal(second.refers.length, 1);
assert.equal(second.refers[0][0], 'sip:103@pbx');
assert.equal(second.refers[0][1].replaces, colleague2, 'REFER without Replaces of the colleague\'s call');

// «Вернуться к клиенту»: the colleague's call ends, the client off hold
click('[data-sp-return]');
assert.equal(colleague2.terminated, true);
assert.equal(second.held, false, 'the client stays on hold');
assert.equal($('[data-sp-xfer-panel]').hidden, true);
assert.equal($('[data-sp-xfer]').hidden, false);
// the client hangs up during a talk with a colleague: that call ends too
click('[data-sp-xfer]');
click('[data-sp-consult]');
const colleague3 = calls.at(-1);
second.emit('ended', { originator: 'remote', cause: 'Terminated' });
assert.equal(colleague3.terminated, true, 'the colleague\'s call stays after the client is gone');
assert.equal($('[data-sp-status]').textContent, 'Звонок завершён');

console.log('softphone: ok');
