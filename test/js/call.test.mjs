// The call page (static/js/call.js) in jsdom with a stand-in for LiveKit: an audio call turns on only the
// microphone; the people of the call with their microphones and cameras; a shared screen is shown large by itself,
// «Докладчик» puts whoever speaks large; the devices are chosen; «Позвонить остальным» rings the rest of the chat;
// «Поднять руку», reactions and the chat of the call go over the data channel; «Шумоподавление» restarts the microphone;
// the background of one's video (callbg.js) is set on the camera as a track processor and remembered; the host turns
// off others' microphones, cameras and screens and puts people out, the person is told.
// node test/js/call.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const call = readFileSync(new URL('../../internal/web/static/js/call.js', import.meta.url), 'utf8');
const callbg = readFileSync(new URL('../../internal/web/static/js/callbg.js', import.meta.url), 'utf8');
const dom = new JSDOM(`<!doctype html><body><div class="callpage" data-call data-lk="wss://lk" data-token="t" data-failed="Не удалось" data-you="вы"
  data-video="false" data-ring="" data-chat="!g:x" data-screen="экран" data-ringing-more="Звоним остальным" data-hand-up="%s поднимает руку"
  data-host="room=!g:x" data-host-mark="ведущий" data-mute-mic="Выключить микрофон" data-mute-cam="Выключить камеру" data-stop-screen="Остановить показ"
  data-remove="Удалить из звонка" data-remove-confirm="Удалить %s?" data-muted-all="Выключены у всех" data-muted-microphone="Ведущий выключил ваш микрофон"
  data-muted-camera="Ведущий выключил вашу камеру" data-muted-screen-share="Ведущий остановил показ вашего экрана" data-removed="Ведущий удалил вас">
  <a class="button" href="/im?room=!g:x">назад</a>
  <div data-call-status>…</div><div class="callgrid" data-call-grid></div>
  <button data-call-mic><span data-text-on="Выкл. микрофон" data-text-off="Вкл. микрофон"></span></button>
  <button data-call-cam><span data-text-on="Выкл. камеру" data-text-off="Вкл. камеру"></span></button>
  <button data-call-screen><span data-text-on="Стоп" data-text-off="Экран"></span></button>
  <button data-call-layout><span data-text-on="Докладчик" data-text-off="Сетка"></span></button>
  <details data-call-people-pop><summary><span data-call-count>1</span></summary><ul data-call-people></ul><button data-call-mute-all>всем</button><button data-call-ring-more>ещё</button></details>
  <details data-call-devices><summary>⚙</summary><label><select data-kind="audioinput"></select></label><label><select data-kind="videoinput"></select></label>
    <label><select data-kind="audiooutput"></select></label><input type="checkbox" data-call-noise checked>
    <div data-call-bg-pane><div data-call-bgs data-base="/mp/" data-preset="Фон %s" data-own="Свой" data-remove="Удалить" data-loading="Готовим" data-failed="Нет фона">
      <button data-bg="none">∅</button><button data-bg="blur">Р</button><button data-bg="blur2">Р+</button><label>+<input type="file" data-call-bg-file></label></div></div></details>
  <button data-call-hand data-up="Поднять руку" data-down="Опустить руку">Поднять руку</button>
  <details data-call-react><summary>🙂</summary><button data-call-emoji="👍">👍</button><button data-call-emoji="🎉">🎉</button></details>
  <details data-call-chat><summary><span data-call-unread hidden>0</span></summary><ul data-call-msgs></ul><form><textarea name="text"></textarea><button>→</button></form></details>
  <button data-call-leave>выйти</button></div></body>`, { runScripts: 'outside-only', url: 'https://go24.test/call?room=!g:x', pretendToBeVisual: true });
const w = dom.window, d = w.document;
w.TextEncoder = TextEncoder;
w.TextDecoder = TextDecoder;
const sent = [];
w.CSS = { escape: s => s };
const tick = () => new Promise(r => setTimeout(r, 20));
const switched = [], posts = [];
const person = (identity, name, local = false) => ({ identity, name, isLocal: local, isMicrophoneEnabled: false, isCameraEnabled: false, isScreenShareEnabled: false });
const restarts = [];
const cam = { source: 'camera', kind: 'video', p: null, attach: () => d.createElement('video'), detach: () => [],
  async setProcessor(p) { this.p = p; }, getProcessor() { return this.p; }, async stopProcessor() { this.p = null; } };
const micTrack = { async restartTrack(o) { restarts.push(JSON.parse(JSON.stringify(o))); } };
const me = Object.assign(person('me', 'Я', true), {
  getTrackPublication: src => src === 'camera' ? { track: cam } : src === 'microphone' ? { track: micTrack } : undefined,
  async setMicrophoneEnabled(on) { this.isMicrophoneEnabled = on; }, async setCameraEnabled(on) { this.isCameraEnabled = on; },
  async setScreenShareEnabled(on) { this.isScreenShareEnabled = on; }, async enableCameraAndMicrophone() { this.isCameraEnabled = this.isMicrophoneEnabled = true; },
  async publishData(data, opts) { sent.push([JSON.parse(new TextDecoder().decode(data)), opts.topic, ...(opts.destinationIdentities ? [[...opts.destinationIdentities]] : [])]); } });
let room;
const ev = ['TrackSubscribed', 'TrackUnsubscribed', 'LocalTrackPublished', 'LocalTrackUnpublished', 'TrackMuted', 'TrackUnmuted', 'ParticipantConnected',
  'ParticipantDisconnected', 'ActiveSpeakersChanged', 'Disconnected', 'DataReceived'];
w.LivekitClient = {
  RoomEvent: Object.fromEntries(ev.map(e => [e, e])),
  DisconnectReason: { PARTICIPANT_REMOVED: 4 },
  Track: { Source: { ScreenShare: 'screen_share', Camera: 'camera', Microphone: 'microphone' } },
  Room: class {
    constructor(opts) { this.opts = opts; this.h = {}; this.localParticipant = me; this.remoteParticipants = new Map(); room = this; }
    on(e, f) { (this.h[e] ??= []).push(f); return this; }
    emit(e, ...a) { (this.h[e] || []).forEach(f => f(...a)); }
    async connect() {}
    async disconnect() {}
    getActiveDevice() { return 'b'; }
    async switchActiveDevice(kind, id) { switched.push([kind, id]); return true; }
    static async getLocalDevices(kind) { return kind === 'audiooutput' ? [] : [{ deviceId: 'a', label: 'Первое' }, { deviceId: 'b', label: 'Второе' }]; }
  },
};
w.fetch = async (url, opts = {}) => { posts.push([url, Object.fromEntries(new URLSearchParams(String(opts.body)))]); return { ok: true, json: async () => ({}) }; };
w.HTMLCanvasElement.prototype.captureStream = () => ({}); // jsdom has no media: the background pane stays
w.Worker = class {};
w.WebAssembly = {};
w.eval(callbg);
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

// «Шумоподавление»: on by default, turning it off restarts the microphone without it and is remembered
assert.equal(room.opts.audioCaptureDefaults.noiseSuppression, true);
const noise = $('[data-call-noise]');
noise.checked = false;
noise.dispatchEvent(new w.Event('change'));
assert.deepEqual(restarts, [{ deviceId: 'b', noiseSuppression: false, echoCancellation: true, autoGainControl: true }]);
assert.equal(w.localStorage.getItem('go24.call.noise'), '0');

// the background: the ready ones are offered; blur sets the processor on the camera, another choice only repaints it,
// «Без фона» takes it off; the choice is remembered and set again on a camera published later
const bgs = [...d.querySelectorAll('[data-bg]')].map(b => b.dataset.bg);
assert.deepEqual(bgs, ['none', 'blur', 'blur2', 'p1', 'p2', 'p3', 'p4', 'p5', 'p6']);
assert.ok($('[data-bg="none"]').classList.contains('on'));
const pick = async k => { $(`[data-bg="${k}"]`).click(); await tick(); };
await pick('blur');
assert.equal(cam.p?.name, 'go24-background');
assert.equal(w.localStorage.getItem('go24.call.bg'), 'blur');
assert.ok($('[data-bg="blur"]').classList.contains('on'));
const first = cam.p, paint = cam.p.paint;
await pick('blur2');
assert.equal(cam.p, first, 'the same processor');
assert.notEqual(cam.p.paint, paint);
await pick('none');
assert.equal(cam.p, null);
assert.equal(w.localStorage.getItem('go24.call.bg'), null);
await pick('blur');
cam.p = null; // the camera turned on anew
room.emit('LocalTrackPublished', { source: 'camera', track: cam });
await tick();
assert.equal(cam.p?.name, 'go24-background');
await pick('none');

// «Позвонить остальным»
$('[data-call-ring-more]').click();
await tick();
assert.deepEqual(posts.at(-1), ['/call/ring-more', { room: '!g:x', video: '0' }]);
assert.equal($('[data-call-status]').textContent, 'Звоним остальным');

// «Поднять руку»: my hand up goes to the others, ✋ on my tile and in the list; hers comes with a note
const data = (p, m) => room.emit('DataReceived', new TextEncoder().encode(JSON.stringify(m)), p, 0, 'go24');
$('[data-call-hand]').click();
assert.deepEqual(sent.at(-1), [{ t: 'hand', up: true }, 'go24']);
assert.equal($('[data-call-hand]').textContent, 'Опустить руку');
assert.ok($('[data-sid="me"]').classList.contains('hand'));
assert.match($('[data-call-people]').textContent, /Я \(вы\) ✋/);
data(anna, { t: 'hand', up: true });
assert.ok($('[data-sid="u1"]').classList.contains('hand'));
assert.equal($('[data-call-status]').textContent, 'Анна поднимает руку');
data(anna, { t: 'hand', up: false });
assert.ok(!$('[data-sid="u1"]').classList.contains('hand'));
// someone new comes — they are told my hand is up
const petr = person('u2', 'Пётр');
room.remoteParticipants.set('u2', petr);
room.emit('ParticipantConnected', petr);
assert.deepEqual(sent.at(-1), [{ t: 'hand', up: true }, 'go24']);
$('[data-call-hand]').click();
assert.deepEqual(sent.at(-1), [{ t: 'hand', up: false }, 'go24']);
assert.equal($('[data-call-hand]').textContent, 'Поднять руку');
assert.ok(!$('[data-sid="me"]').classList.contains('hand'));

// reactions: mine floats over my tile and goes to the others; theirs over theirs; an unknown one is ignored
[...d.querySelectorAll('[data-call-emoji]')].find(b => b.dataset.callEmoji === '🎉').click(); // nwsapi: no emoji in selectors
assert.deepEqual(sent.at(-1), [{ t: 'react', e: '🎉' }, 'go24']);
assert.equal($('[data-sid="me"] .callreact').textContent, '🎉');
data(anna, { t: 'react', e: '👍' });
data(anna, { t: 'react', e: '<b>x</b>' });
assert.deepEqual([...d.querySelectorAll('[data-sid="u1"] .callreact')].map(r => r.textContent), ['👍']);

// the chat of the call: hers is counted on the closed chat, opening it clears the count; mine goes to the others
data(anna, { t: 'msg', text: 'Слышно?' });
data(anna, { t: 'msg', text: 'Да <i>нет</i>' });
assert.equal($('[data-call-unread]').textContent, '2');
assert.equal($('[data-call-unread]').hidden, false);
const chat = $('[data-call-chat]');
chat.open = true;
chat.dispatchEvent(new w.Event('toggle'));
assert.equal($('[data-call-unread]').hidden, true);
chat.querySelector('textarea').value = ' Да, слышно ';
chat.querySelector('form').dispatchEvent(new w.Event('submit', { cancelable: true }));
assert.deepEqual(sent.at(-1), [{ t: 'msg', text: 'Да, слышно' }, 'go24']);
assert.equal(chat.querySelector('textarea').value, '');
const lines = [...d.querySelectorAll('[data-call-msgs] li')];
assert.deepEqual(lines.map(l => l.querySelector('b').textContent + ': ' + l.querySelector('div').textContent), ['Анна: Слышно?', 'Анна: Да <i>нет</i>', 'Я (вы): Да, слышно']);
assert.equal(d.querySelector('[data-call-msgs] i'), null, 'the text is not markup');

// the host (go24 marked me in the token): «ведущий» by my name; by her — turn off her microphone, put her out
me.metadata = '{"host":true}';
anna.isMicrophoneEnabled = true;
room.emit('TrackUnmuted');
const rowOf = name => [...d.querySelectorAll('[data-call-people] li')].find(li => li.textContent.startsWith(name));
assert.match(rowOf('Я').textContent, /Я \(вы\) · ведущий/);
assert.equal(rowOf('Я').querySelector('.callhost'), null, 'no tools by oneself');
assert.deepEqual([...rowOf('Анна').querySelectorAll('.callhost button')].map(b => b.title), ['Выключить микрофон', 'Удалить из звонка']);
rowOf('Анна').querySelector('[title="Выключить микрофон"]').click();
await tick();
assert.deepEqual(posts.at(-1), ['/call/moderate?room=!g:x', { action: 'mute', who: 'u1', what: 'microphone' }]);
assert.deepEqual(sent.at(-1), [{ t: 'muted', what: 'microphone' }, 'go24', ['u1']]);
w.confirm = () => true;
rowOf('Анна').querySelector('[title="Удалить из звонка"]').click();
await tick();
assert.deepEqual(posts.at(-1), ['/call/moderate?room=!g:x', { action: 'remove', who: 'u1', what: '' }]);
$('[data-call-mute-all]').click();
await tick();
assert.deepEqual(posts.at(-1), ['/call/moderate?room=!g:x', { action: 'mute', who: '', what: 'microphone' }]);
assert.deepEqual(sent.at(-1), [{ t: 'muted', what: 'microphone' }, 'go24']);
assert.equal($('[data-call-status]').textContent, 'Выключены у всех');
// the host stopped my screen: it stops, I am told; someone not the host cannot; my buttons follow a muted microphone
me.metadata = '';
anna.metadata = '{"host":true}';
me.isScreenShareEnabled = true;
data(petr, { t: 'muted', what: 'screen_share' });
assert.equal(me.isScreenShareEnabled, true);
data(anna, { t: 'muted', what: 'screen_share' });
await tick();
assert.equal(me.isScreenShareEnabled, false);
assert.equal($('[data-call-status]').textContent, 'Ведущий остановил показ вашего экрана');
me.isMicrophoneEnabled = false;
data(anna, { t: 'muted', what: 'microphone' });
assert.equal($('[data-call-status]').textContent, 'Ведущий выключил ваш микрофон');
assert.equal($('[data-call-mic] span').textContent, 'Вкл. микрофон');
me.isMicrophoneEnabled = true;
anna.metadata = '';
room.remoteParticipants.delete('u2');
room.emit('ParticipantDisconnected', petr);

// she leaves: her tiles go
room.remoteParticipants.delete('u1');
room.emit('ParticipantDisconnected', anna);
assert.deepEqual(tiles(), ['me']);
assert.equal($('[data-call-count]').textContent, '1');
// put out by the host: told so, not «no connection»
room.emit('Disconnected', 4);
assert.equal($('[data-call-status]').textContent, 'Ведущий удалил вас');
console.log('call.js: the call page passed');
