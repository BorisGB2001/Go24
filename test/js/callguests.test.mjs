// Guests of a call (static/js/call.js) in jsdom: «Пригласить гостя» makes a link and copies it; the links in force are
// listed with «Отозвать»; a guest waiting in the lobby is shown above the call with «Впустить / Отклонить».
// node test/js/callguests.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const call = readFileSync(new URL('../../internal/web/static/js/call.js', import.meta.url), 'utf8');
const dom = new JSDOM(`<!doctype html><body><div class="callpage" data-call data-lk="wss://lk" data-token="t" data-failed="Не удалось" data-you="вы"
  data-video="false" data-ring="" data-chat="" data-guests="task=GO-1" data-lobby-text="%s ждёт в прихожей" data-until="до %s" data-copied="Скопировано">
  <a class="button" href="/t/GO-1">назад</a>
  <details data-call-invite><summary>Пригласить</summary><ul data-call-links data-copy="Копировать" data-revoke="Отозвать" hidden></ul>
    <form data-call-invite-form><select name="ttl"><option value="1">час</option><option value="24" selected>сутки</option></select>
      <input type="text" name="password" value="секрет"><input type="checkbox" name="lobby" value="1" checked><button>Создать</button></form></details>
  <div data-call-lobby data-admit="Впустить" data-deny="Отклонить" hidden></div>
  <div data-call-status>…</div><div class="callgrid" data-call-grid></div>
  <button data-call-mic><span data-text-on="-" data-text-off="+"></span></button><button data-call-cam><span data-text-on="-" data-text-off="+"></span></button>
  <button data-call-screen><span data-text-on="-" data-text-off="+"></span></button><button data-call-layout><span data-text-on="-" data-text-off="+"></span></button>
  <details data-call-people-pop><summary><span data-call-count>1</span></summary><ul data-call-people></ul></details>
  <details data-call-devices><summary>⚙</summary></details>
  <button data-call-leave>выйти</button></div></body>`, { runScripts: 'outside-only', url: 'https://go24.test/call?task=GO-1', pretendToBeVisual: true });
const w = dom.window, d = w.document;
w.CSS = { escape: s => s };
const tick = () => new Promise(r => setTimeout(r, 20));
const me = { identity: 'me', name: 'Я', isLocal: true, isMicrophoneEnabled: false, isCameraEnabled: false, isScreenShareEnabled: false,
  async setMicrophoneEnabled(on) { this.isMicrophoneEnabled = on; }, async setCameraEnabled(on) { this.isCameraEnabled = on; },
  async setScreenShareEnabled(on) { this.isScreenShareEnabled = on; } };
const ev = ['TrackSubscribed', 'TrackUnsubscribed', 'LocalTrackPublished', 'LocalTrackUnpublished', 'TrackMuted', 'TrackUnmuted', 'ParticipantConnected',
  'ParticipantDisconnected', 'ActiveSpeakersChanged', 'Disconnected'];
w.LivekitClient = {
  RoomEvent: Object.fromEntries(ev.map(e => [e, e])),
  Track: { Source: { ScreenShare: 'screen_share', Camera: 'camera' } },
  Room: class {
    constructor() { this.h = {}; this.localParticipant = me; this.remoteParticipants = new Map(); }
    on(e, f) { (this.h[e] ??= []).push(f); return this; }
    async connect() {}
    async disconnect() {}
    static async getLocalDevices() { return []; }
  },
};
let state = { links: [], waiting: [{ id: 'g1', name: 'Пётр' }] };
const calls = [], copied = [];
Object.defineProperty(w.navigator, 'clipboard', { value: { async writeText(s) { copied.push(s); } } });
w.fetch = async (url, opts = {}) => {
  calls.push([opts.method, url, opts.body ? Object.fromEntries(opts.body) : null]);
  if (url.startsWith('/call/guests?') && opts.method === 'POST') {
    const l = { id: 'l1', url: 'https://go24.test/call/g/tok', expires: '2026-10-26T12:00:00Z', password: true, lobby: true };
    state.links = [l];
    return { ok: true, json: async () => l };
  }
  if (url.startsWith('/call/lobby/')) state.waiting = [];
  if (url.startsWith('/call/guests/l1/revoke')) state.links = [];
  return { ok: true, json: async () => state };
};
w.eval(call);
await tick();

// the guest in the lobby, above the call
const lobby = d.querySelector('[data-call-lobby]');
assert.equal(lobby.hidden, false);
assert.match(lobby.textContent, /Пётр ждёт в прихожей/);
assert.equal(calls[0][1], '/call/guests?task=GO-1');
[...lobby.querySelectorAll('button')].find(b => b.textContent === 'Впустить').click();
await tick();
assert.ok(calls.some(c => c[0] === 'POST' && c[1] === '/call/lobby/g1/admit?task=GO-1'));
assert.equal(lobby.hidden, true);

// «Пригласить гостя»: a link with the password and the lobby, copied; the password field is emptied
const f = d.querySelector('[data-call-invite-form]');
f.dispatchEvent(new w.Event('submit', { cancelable: true }));
await tick();
const made = calls.find(c => c[0] === 'POST' && c[1] === '/call/guests?task=GO-1');
assert.deepEqual(made[2], { ttl: '24', password: 'секрет', lobby: '1' });
assert.deepEqual(copied, ['https://go24.test/call/g/tok']);
assert.equal(f.elements.password.value, '');
const links = d.querySelector('[data-call-links]');
assert.equal(links.hidden, false);
assert.match(links.textContent, /🔒 до/);

// «Отозвать»
[...links.querySelectorAll('button')].find(b => b.textContent === 'Отозвать').click();
await tick();
assert.ok(calls.some(c => c[0] === 'POST' && c[1] === '/call/guests/l1/revoke?task=GO-1'));
assert.equal(links.hidden, true);
console.log('callguests.test.mjs: ok');
process.exit(0);
