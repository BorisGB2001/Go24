// Recording of a call (static/js/call.js) in jsdom with a stand-in for LiveKit: «Начать запись» shows only where the
// administrators let calls be recorded; pressed, it starts the recording and turns into «Остановить запись», with
// «Идёт запись» above the call; LiveKit telling the room it is recorded (someone else started it) shows the same;
// «Остановить запись» stops it and says the file goes to the Drive; a refusal of go24 is shown.
// node test/js/callrecord.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const call = readFileSync(new URL('../../internal/web/static/js/call.js', import.meta.url), 'utf8');
const page = enabled => {
  const dom = new JSDOM(`<!doctype html><body><div class="callpage" data-call data-lk="wss://lk" data-token="t" data-failed="Не удалось" data-you="вы"
    data-video="true" data-ring="" data-chat="" data-record="task=CR-1&amp;video=1" data-saving="Сохраняется на Диск" data-screen="экран">
    <h1>Звонок <span data-call-rec hidden>Идёт запись</span></h1>
    <a class="button" href="/t/CR-1">назад</a>
    <div data-call-status>…</div><div class="callgrid" data-call-grid></div>
    <button data-call-mic><span data-text-on="Выкл. микрофон" data-text-off="Вкл. микрофон"></span></button>
    <button data-call-cam><span data-text-on="Выкл. камеру" data-text-off="Вкл. камеру"></span></button>
    <button data-call-screen><span data-text-on="Стоп" data-text-off="Экран"></span></button>
    <button data-call-layout><span data-text-on="Докладчик" data-text-off="Сетка"></span></button>
    <button data-call-record data-start="Начать запись" data-stop="Остановить запись" hidden>Начать запись</button>
    <details data-call-people-pop><summary><span data-call-count>1</span></summary><ul data-call-people></ul></details>
    <details data-call-devices><summary>⚙</summary></details>
    <button data-call-leave>выйти</button></div></body>`, { runScripts: 'outside-only', url: 'https://go24.test/call?task=CR-1', pretendToBeVisual: true });
  const w = dom.window;
  w.CSS = { escape: s => s };
  const me = { identity: 'me', name: 'Я', isLocal: true, isMicrophoneEnabled: false, isCameraEnabled: false, isScreenShareEnabled: false,
    async setMicrophoneEnabled(on) { this.isMicrophoneEnabled = on; }, async enableCameraAndMicrophone() { this.isCameraEnabled = this.isMicrophoneEnabled = true; } };
  const t = { requests: [], answer: { on: true }, status: 200 };
  const ev = ['TrackSubscribed', 'TrackUnsubscribed', 'LocalTrackPublished', 'LocalTrackUnpublished', 'TrackMuted', 'TrackUnmuted', 'ParticipantConnected',
    'ParticipantDisconnected', 'ActiveSpeakersChanged', 'Disconnected', 'RecordingStatusChanged'];
  w.LivekitClient = {
    RoomEvent: Object.fromEntries(ev.map(e => [e, e])),
    Track: { Source: { ScreenShare: 'screen_share', Camera: 'camera' } },
    Room: class {
      constructor() { this.h = {}; this.localParticipant = me; this.remoteParticipants = new Map(); this.isRecording = false; t.room = this; }
      on(e, f) { (this.h[e] ??= []).push(f); return this; }
      emit(e, ...a) { (this.h[e] || []).forEach(f => f(...a)); }
      async connect() {}
      async disconnect() {}
      static async getLocalDevices() { return []; }
    },
  };
  w.fetch = async (url, opts = {}) => {
    t.requests.push([opts.method || 'GET', url, opts.body ? String(opts.body) : '']);
    if (url.startsWith('/call/record') && (opts.method || 'GET') === 'GET') return { ok: true, json: async () => ({ enabled, on: false }) };
    return { ok: t.status === 200, status: t.status, json: async () => t.answer };
  };
  w.eval(call);
  t.$ = s => w.document.querySelector(s);
  return t;
};
const tick = () => new Promise(r => setTimeout(r, 20));

// not allowed: no button, nothing recorded
const off = page(false);
await tick();
assert.equal(off.$('[data-call-record]').hidden, true);
assert.deepEqual(off.requests.filter(r => r[1].startsWith('/call/record')), [['GET', '/call/record?task=CR-1&video=1', '']]);

// allowed: the button; pressed — recording, the mark above the call
const t = page(true);
await tick();
const btn = t.$('[data-call-record]'), mark = t.$('[data-call-rec]');
assert.equal(btn.hidden, false);
assert.equal(mark.hidden, true);
btn.click();
await tick();
assert.deepEqual(t.requests.at(-1), ['POST', '/call/record?task=CR-1&video=1', 'action=start']);
assert.equal(btn.textContent, 'Остановить запись');
assert.ok(btn.classList.contains('danger'));
assert.equal(mark.hidden, false);

// stopped: the file goes to the Drive
t.answer = { on: false };
btn.click();
await tick();
assert.equal(t.requests.at(-1)[2], 'action=stop');
assert.equal(btn.textContent, 'Начать запись');
assert.equal(mark.hidden, true);
assert.equal(t.$('[data-call-status]').textContent, 'Сохраняется на Диск');

// someone else started recording: LiveKit tells the room
t.room.emit('RecordingStatusChanged', true);
assert.equal(mark.hidden, false);
assert.equal(btn.textContent, 'Остановить запись');
t.room.emit('RecordingStatusChanged', false);
assert.equal(mark.hidden, true);

// go24 refuses (the administrator turned recording off meanwhile)
t.status = 422;
t.answer = { error: 'Запись звонков выключена администратором' };
btn.click();
await tick();
assert.equal(t.$('[data-call-status]').textContent, 'Запись звонков выключена администратором');
assert.equal(mark.hidden, true);
console.log('callrecord: ok');
