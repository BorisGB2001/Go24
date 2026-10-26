// The call page (static/js/call.js) in jsdom with a stand-in for LiveKit: an audio call turns on only the
// microphone; the people of the call with their microphones and cameras; a shared screen is shown large by itself,
// «Докладчик» puts whoever speaks large; the devices are chosen; «Позвонить остальным» rings the rest of the chat.
// node test/js/call.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const call = readFileSync(new URL('../../internal/web/static/js/call.js', import.meta.url), 'utf8');
const dom = new JSDOM(`<!doctype html><body><div class="callpage" data-call data-lk="wss://lk" data-token="t" data-failed="Не удалось" data-you="вы"
  data-video="false" data-ring="" data-chat="!g:x" data-screen="экран" data-ringing-more="Звоним остальным">
  <a class="button" href="/im?room=!g:x">назад</a>
  <div data-call-status>…</div><div class="callgrid" data-call-grid></div>
  <button data-call-mic><span data-text-on="Выкл. микрофон" data-text-off="Вкл. микрофон"></span></button>
  <button data-call-cam><span data-text-on="Выкл. камеру" data-text-off="Вкл. камеру"></span></button>
  <button data-call-screen><span data-text-on="Стоп" data-text-off="Экран"></span></button>
  <button data-call-layout><span data-text-on="Докладчик" data-text-off="Сетка"></span></button>
  <details data-call-people-pop><summary><span data-call-count>1</span></summary><ul data-call-people></ul><button data-call-ring-more>ещё</button></details>
  <details data-call-devices><summary>⚙</summary><label><select data-kind="audioinput"></select></label><label><select data-kind="videoinput"></select></label>
    <label><select data-kind="audiooutput"></select></label></details>
  <button data-call-leave>выйти</button></div></body>`, { runScripts: 'outside-only', url: 'https://go24.test/call?room=!g:x', pretendToBeVisual: true });
const w = dom.window, d = w.document;
w.CSS = { escape: s => s };
const tick = () => new Promise(r => setTimeout(r, 20));
const switched = [], posts = [];
const person = (identity, name, local = false) => ({ identity, name, isLocal: local, isMicrophoneEnabled: false, isCameraEnabled: false, isScreenShareEnabled: false });
const me = Object.assign(person('me', 'Я', true), {
  async setMicrophoneEnabled(on) { this.isMicrophoneEnabled = on; }, async setCameraEnabled(on) { this.isCameraEnabled = on; },
  async setScreenShareEnabled(on) { this.isScreenShareEnabled = on; }, async enableCameraAndMicrophone() { this.isCameraEnabled = this.isMicrophoneEnabled = true; } });
let room;
const ev = ['TrackSubscribed', 'TrackUnsubscribed', 'LocalTrackPublished', 'LocalTrackUnpublished', 'TrackMuted', 'TrackUnmuted', 'ParticipantConnected',
  'ParticipantDisconnected', 'ActiveSpeakersChanged', 'Disconnected'];
w.LivekitClient = {
  RoomEvent: Object.fromEntries(ev.map(e => [e, e])),
  Track: { Source: { ScreenShare: 'screen_share', Camera: 'camera' } },
  Room: class {
    constructor() { this.h = {}; this.localParticipant = me; this.remoteParticipants = new Map(); room = this; }
    on(e, f) { (this.h[e] ??= []).push(f); return this; }
    emit(e, ...a) { (this.h[e] || []).forEach(f => f(...a)); }
    async connect() {}
    async disconnect() {}
    getActiveDevice() { return 'b'; }
    async switchActiveDevice(kind, id) { switched.push([kind, id]); return true; }
    static async getLocalDevices(kind) { return kind === 'audiooutput' ? [] : [{ deviceId: 'a', label: 'Первое' }, { deviceId: 'b', label: 'Второе' }]; }
  },
};
w.fetch = async (url, opts = {}) => { posts.push([url, Object.fromEntries(new URLSearchParams(String(opts.body)))]); return { ok: true }; };
w.eval(call);
await tick();
const $ = s => d.querySelector(s);
const tiles = () => [...d.querySelectorAll('.calltile')].map(t => t.dataset.sid + (t.classList.contains('main') ? '*' : ''));
const track = (kind, source) => { const el = d.createElement(kind); return { kind, source, attach: () => el, detach: () => [el] }; };

// an audio call: only the microphone; the tile without a camera shows the name, the list has the microphone on
assert.equal(me.isMicrophoneEnabled, true);
assert.equal(me.isCameraEnabled, false);
assert.ok($('[data-sid="me"]').classList.contains('cam-off'));
assert.equal($('[data-call-people]').textContent, 'Я (вы) 🎤');

// someone comes: their tile, the count, their microphone off
const anna = person('u1', 'Анна');
room.remoteParticipants.set('u1', anna);
room.emit('ParticipantConnected', anna);
assert.deepEqual(tiles(), ['me', 'u1']);
assert.equal($('[data-call-count]').textContent, '2');
assert.equal($('[data-call-status]').hidden, true);
assert.ok($('[data-sid="u1"]').classList.contains('mic-off'));
anna.isMicrophoneEnabled = true;
room.emit('TrackUnmuted');
assert.ok(!$('[data-sid="u1"]').classList.contains('mic-off'));

// her shared screen: a tile of its own, large by itself; gone when she stops
const screen = track('video', 'screen_share');
anna.isScreenShareEnabled = true;
room.emit('TrackSubscribed', screen, {}, anna);
assert.deepEqual(tiles(), ['me', 'u1', 'u1:screen*']);
assert.ok($('[data-call-grid]').classList.contains('speaker'));
assert.match($('[data-sid="u1:screen"] .callname').textContent, /Анна · экран/);
anna.isScreenShareEnabled = false;
room.emit('TrackUnsubscribed', screen, {}, anna);
assert.deepEqual(tiles(), ['me', 'u1']);
assert.ok(!$('[data-call-grid]').classList.contains('speaker'));

// «Докладчик»: whoever speaks is large; «Сетка» back
$('[data-call-layout]').click();
room.emit('ActiveSpeakersChanged', [anna]);
assert.deepEqual(tiles(), ['me', 'u1*']);
assert.equal($('[data-call-layout] span').textContent, 'Сетка');
$('[data-call-layout]').click();
assert.deepEqual(tiles(), ['me', 'u1']);

// the devices: the lists come when opened, the speaker's is hidden when the browser offers none; a choice switches
const dev = $('[data-call-devices]');
dev.open = true;
dev.dispatchEvent(new w.Event('toggle'));
await tick();
const mic = dev.querySelector('[data-kind="audioinput"]');
assert.deepEqual([...mic.options].map(o => o.textContent), ['Первое', 'Второе']);
assert.equal(mic.value, 'b', 'the device in use is chosen');
assert.equal(dev.querySelector('[data-kind="audiooutput"]').closest('label').hidden, true);
mic.value = 'a';
mic.dispatchEvent(new w.Event('change', { bubbles: true }));
assert.deepEqual(switched, [['audioinput', 'a']]);

// «Позвонить остальным»
$('[data-call-ring-more]').click();
await tick();
assert.deepEqual(posts.at(-1), ['/call/ring-more', { room: '!g:x', video: '0' }]);
assert.equal($('[data-call-status]').textContent, 'Звоним остальным');

// she leaves: her tiles go
room.remoteParticipants.delete('u1');
room.emit('ParticipantDisconnected', anna);
assert.deepEqual(tiles(), ['me']);
assert.equal($('[data-call-count]').textContent, '1');
console.log('call.js: the call page passed');
