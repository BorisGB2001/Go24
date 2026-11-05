// The softphone window (static/js/softphone.js) in jsdom with a stand-in JsSIP: «Перевести» shows while talking;
// a transfer at once sends REFER to the number and the window says where the call went; a transfer after talking to
// the colleague holds the client, calls the colleague, «Завершить перевод» sends REFER with Replaces of that call;
// the colleague gone — back to choosing, «Вернуться к клиенту» takes the client off hold. «Конференция»: the client off
// hold, each call is sent the microphone with the other call's sound, «Выйти — оставить их вдвоём» transfers, the
// colleague gone — the microphone back to the client; the microphone off stays off in the mix and after it.
// node test/js/softphone.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const code = readFileSync(new URL('../../internal/web/static/js/softphone.js', import.meta.url), 'utf8');
const dom = new JSDOM(`<!doctype html><body><div data-softphone data-id="0123456789abcdef" data-login="w101" data-password="pw" data-host="pbx"
  data-ready="Готов" data-ended="Звонок завершён" data-busy="Занято" data-failed="Ошибка: %s" data-ringing="Вызов" data-dialing="Набор"
  data-on-hold="На удержании" data-bad-number="Неверный номер" data-hold="Удержать" data-unhold="Снять" data-transferred="Переведён на %s"
  data-xfer-failed="Не удалось перевести" data-xfer-calling="Вызов: %s" data-xfer-talking="Разговор: %s" data-xfer-ended="Коллега не ответил"
  data-conf-talking="Конференция: %s" data-conf-failed="Не объединить" data-conf-left="Коллега вышел">
  <span data-sp-dot></span><span data-sp-status></span>
  <div data-sp-who hidden><img data-sp-photo hidden><a data-sp-link><b data-sp-name></b></a><span data-sp-num></span><span data-sp-timer></span></div>
  <input data-sp-input><button data-sp-back></button><div data-sp-keys></div>
  <button data-sp-mute hidden></button><button data-sp-call><span data-sp-call-text data-call="Позвонить" data-answer="Ответить"></span></button>
  <button data-sp-hang hidden><span data-sp-hang-text data-hangup="Завершить" data-decline="Отклонить"></span></button>
  <button data-sp-hold hidden></button><button data-sp-xfer hidden></button>
  <div data-sp-xfer-panel hidden><div data-sp-xfer-ask><input data-sp-xfer-to><button data-sp-blind></button><button data-sp-consult></button>
    <button data-sp-xfer-cancel></button></div>
    <div data-sp-xfer-talk hidden><b data-sp-xfer-who></b><button data-sp-complete disabled data-xfer="Завершить перевод" data-conf="Выйти">Завершить перевод</button>
    <button data-sp-conf disabled></button><button data-sp-return data-xfer="Вернуться" data-conf="Отключить коллегу">Вернуться</button></div></div>
  <div data-sp-card hidden data-saved="Сохранено" data-pending="Сохранится" data-company="Компания" data-stage="Стадия" data-resp="Ответственный">
    <button data-sp-card-close></button><div data-sp-client hidden><span data-sp-client-kind></span><b data-sp-client-name></b><div data-sp-client-extra></div></div>
    <div data-sp-deals hidden><ul data-sp-deal-list></ul></div><div data-sp-create hidden></div>
    <textarea data-sp-note></textarea><button data-sp-note-save></button><span data-sp-note-state></span>
    <div data-sp-calls hidden><ul data-sp-call-list></ul></div></div>
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
    const mic = { kind: 'audio', enabled: true, id: 'mic-' + n };
    this.sender = { track: mic, replaceTrack(x) { this.track = x; return Promise.resolve(); } };
    this.mic = mic;
    this.connection = { getSenders: () => [this.sender], addEventListener() {} };
    this.remote_identity = { uri: { user: n, toString: () => uri }, display_name: '' };
  }
  isOnHold() { return { local: this.held }; }
  hold() { this.held = true; }
  unhold() { this.held = false; }
  isMuted() { return { audio: !!this.muted }; }
  mute() { this.muted = true; this.connection.getSenders()[0].track.enabled = false; }
  unmute() { this.muted = false; this.connection.getSenders()[0].track.enabled = true; }
  refer(to, opts) { this.refers.push([String(to), opts]); }
  terminate() { this.terminated = true; this.emit('ended', { originator: 'local' }); }
}
const calls = [];
w.JsSIP = {
  WebSocketInterface: class {},
  UA: class extends Em { start() { this.emit('registered'); } call(uri) { const s = new Session(uri); calls.push(s); return s; } },
  C: { causes: { BUSY: 'Busy', REJECTED: 'Rejected', UNAVAILABLE: 'Unavailable', NOT_FOUND: 'Not Found', NO_ANSWER: 'No Answer', BYE: 'Terminated', CANCELED: 'Canceled', ANSWERED_ELSEWHERE: 'Elsewhere' } },
};
// go24: the call card of a client, of an unknown number, of a colleague; the comments sent
const notes = [];
w.fetch = async (url, opts) => {
  let out = null;
  if (url.startsWith('/telephony/card?')) {
    const n = new URLSearchParams(url.split('?')[1]).get('number');
    out = n === '+79001234567' ? { at: 1761400000, comment: '', client: { kind: 'Контакт', name: 'ООО Ромашка', url: '/crm/contacts/1', company: 'Ромашка',
      company_url: '/crm/companies/2', responsible: 'Анна', deals: [{ title: 'Поставка', url: '/crm/deals/3', stage: 'Новая', color: '#39f', amount: '1 000 RUB' }] },
      calls: [{ at: '25.10.2026 10:00', dir: 'missed', kind: 'Пропущенный звонок', status: 'Не ответили', time: '0:00', who: 'Анна', comment: 'Перезвонить' }] }
      : n === '+79007654321' ? { at: 1761400100, comment: 'Начато', calls: [], create: [{ what: 'lead', title: 'Создать лид' }] }
      : { at: 1761400200, colleague: true };
  } else if (url === '/telephony/note') {
    notes.push(Object.fromEntries(opts.body));
    out = { saved: true, call: '' };
  }
  return { ok: true, json: async () => out };
};
// WebAudio: the nodes remember what goes into them
const nodes = [];
class Node { constructor(kind, from) { this.kind = kind; this.from = from; this.to = []; nodes.push(this); } connect(n) { this.to.push(n); n.ins = [...(n.ins || []), this]; return n; } }
w.MediaStream = class { constructor(tracks) { this.tracks = tracks; } };
let contexts = 0, closed = 0;
w.AudioContext = class {
  constructor() { contexts++; }
  createGain() { const g = new Node('gain'); g.gain = { value: 1 }; return g; }
  createMediaStreamSource(st) { return new Node('source', st); }
  createMediaStreamDestination() { const d = new Node('dest'); d.stream = { getAudioTracks: () => [{ kind: 'audio', mixOf: d }] }; return d; }
  resume() { return Promise.resolve(); }
  close() { closed++; }
};
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

// the call card: the client with the company, responsible and an open deal, the calls before; the comment goes to go24
const first = await talk('+79001234567');
await wait(10);
assert.equal($('[data-sp-card]').hidden, false, 'no card of a client');
assert.equal($('[data-sp-client-name] a').getAttribute('href'), '/crm/contacts/1');
assert.match($('[data-sp-client-extra]').textContent, /Компания: Ромашка.*Ответственный: Анна/);
assert.equal($('[data-sp-deal-list] a').textContent, 'Поставка');
assert.match($('[data-sp-call-list]').textContent, /Пропущенный звонок.*Перезвонить/);
assert.equal($('[data-sp-create]').hidden, true);
$('[data-sp-note]').value = ' Договорились о встрече ';
click('[data-sp-note-save]');
await wait(10);
assert.deepEqual(notes.at(-1), { number: '+79001234567', at: '1761400000', comment: 'Договорились о встрече' });
assert.equal($('[data-sp-note-state]').textContent, 'Сохранится');
click('[data-sp-note-save]');
await wait(10);
assert.equal(notes.length, 1, 'the same comment sent twice');

// talking: «Перевести» is there; a transfer at once — REFER to the colleague, the window says where the call went
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

assert.equal($('[data-sp-card]').hidden, false, 'the card closes with the call');

// an unknown number: «Создать лид», the comment begun before; a colleague has no card
// after talking to the colleague: the client held, the colleague called; «Завершить перевод» — REFER with Replaces
const second = await talk('+79007654321');
await wait(10);
assert.equal($('[data-sp-client]').hidden, true);
assert.equal($('[data-sp-create]').textContent, '+ Создать лид');
assert.equal($('[data-sp-note]').value, 'Начато');
assert.equal($('[data-sp-deals]').hidden, true);
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
await talk('102');
await wait(10);
assert.equal($('[data-sp-card]').hidden, true, 'a card of a colleague');
calls.at(-1).terminate();

// «Конференция»: the client and the colleague hear each other and the employee
const client = await talk('+79001234567');
w.document.querySelector('[data-sp-audio]').srcObject = { id: 'from-client' };
click('[data-sp-mute]'); // the microphone off before the conference
assert.equal(client.mic.enabled, false);
click('[data-sp-xfer]');
$('[data-sp-xfer-to]').value = '104';
click('[data-sp-consult]');
const coll = calls.at(-1);
assert.equal($('[data-sp-conf]').disabled, true, '«Конференция» before the colleague answered');
coll.emit('confirmed');
w.document.querySelector('[data-sp-audio2]').srcObject = { id: 'from-colleague' };
assert.equal($('[data-sp-conf]').disabled, false);
click('[data-sp-conf]');
assert.equal(contexts, 1, 'no mix');
assert.equal(client.held, false, 'the client stays on hold in the conference');
assert.equal($('[data-sp-xfer-who]').textContent, 'Конференция: 104');
assert.equal($('[data-sp-conf]').hidden, true);
assert.equal($('[data-sp-complete]').textContent, 'Выйти');
assert.equal($('[data-sp-return]').textContent, 'Отключить коллегу');
assert.equal($('[data-sp-hold]').hidden, true, '«Удержать» in the conference');
const heard = sender => sender.track.mixOf.ins.map(n => n.kind === 'gain' ? 'mic:' + n.ins[0].from.tracks[0].id : n.from.id).sort();
assert.deepEqual(heard(client.sender), ['from-colleague', 'mic:mic-+79001234567'], 'the client does not hear the colleague and the employee');
assert.deepEqual(heard(coll.sender), ['from-client', 'mic:mic-+79001234567'], 'the colleague does not hear the client and the employee');
const gain = nodes.find(n => n.kind === 'gain');
assert.equal(gain.gain.value, 0, 'the microphone off before the conference is on in the mix');
assert.equal(client.mic.enabled, true, 'the microphone track off feeds the mix silence');
click('[data-sp-mute]');
assert.equal(gain.gain.value, 1);
assert.equal(client.muted, false, 'the mix muted by JsSIP');
click('[data-sp-mute]');
assert.equal(gain.gain.value, 0);
// the colleague hangs up: the client gets the microphone back (still off), the window says so
coll.emit('ended', { originator: 'remote', cause: 'Terminated' });
assert.equal(client.sender.track, client.mic, 'the client is sent the mix after the conference');
assert.equal(client.mic.enabled, false, 'the microphone came on after the conference');
assert.equal(client.muted, true);
assert.equal(closed, 1, 'the mix left open');
assert.equal($('[data-sp-status]').textContent, 'Коллега вышел');
assert.equal($('[data-sp-complete]').textContent, 'Завершить перевод');
assert.equal($('[data-sp-hold]').hidden, false);
// again, and «Выйти — оставить их вдвоём» transfers the call to the colleague's call
click('[data-sp-mute]');
click('[data-sp-xfer]');
click('[data-sp-consult]');
const coll2 = calls.at(-1);
coll2.emit('confirmed');
click('[data-sp-conf]');
assert.equal($('[data-sp-status]').textContent, 'Не объединить', 'joined with no sound of the colleague');
assert.equal(client.held, true, 'the client off hold after a failed conference');
w.document.querySelector('[data-sp-audio2]').srcObject = { id: 'from-colleague' };
click('[data-sp-conf]');
assert.equal(contexts, 2);
click('[data-sp-complete]');
assert.equal(client.refers.at(-1)[1].replaces, coll2, 'leaving the conference does not transfer');
client.emit('ended', { originator: 'remote', cause: 'Terminated' });
assert.equal(closed, 2);
assert.equal(coll2.terminated, true);

console.log('softphone: ok');
