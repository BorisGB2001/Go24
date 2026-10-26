// Files in the messenger (static/js/im.js) in jsdom: the paper clip and a pasted picture each send a file to
// /im/upload (with the chat and, in a thread, its root); the message appears; a refusal shows the error.
// node test/js/imfiles.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const im = readFileSync(new URL('../../internal/web/static/js/im.js', import.meta.url), 'utf8');
const tick = () => new Promise(r => setTimeout(r, 20));
const dom = new JSDOM(`<!doctype html><body><div class="im" data-im data-room="!r" data-failed="Не удалось" data-uploading="Отправляется…" data-secure="https://go24.test:8444" data-voice-https="https?" data-mic-denied="нет микрофона" data-voice-wait="…">
  <section class="im-chat"><div class="im-msgs" data-msgs></div>
    <form class="chatform" data-send><label><input type="file" multiple hidden data-attach></label><button type="button" data-voice><span data-voice-time></span></button><textarea name="body"></textarea><button>s</button></form></section>
  <aside class="im-thread" data-thread-pane hidden><div data-thread-msgs></div><form data-send-thread><textarea name="body"></textarea></form></aside>
  </div></body>`, { runScripts: 'outside-only', url: 'http://go24.test/im?room=!r', pretendToBeVisual: true });
const w = dom.window, d = w.document;
w.CSS = { escape: s => s };
w.HTMLElement.prototype.scrollIntoView = () => {};
const sent = [];
let refuse = false;
w.fetch = async (url, opts = {}) => {
  if (url === '/im/transcribe') {
    sent.push({ transcribe: String(opts.body) });
    return { ok: true, json: async () => ({ text: 'привет из голосового' }) };
  }
  if (url === '/im/upload') {
    const fields = Object.fromEntries([...opts.body.entries()].map(([k, v]) => [k, typeof v === 'string' ? v : v.name]));
    sent.push(fields);
    if (refuse) return { ok: false, json: async () => ({ error: 'Гостям нельзя загружать файлы' }) };
    const id = '$f' + sent.length;
    return { ok: true, json: async () => ({ id, html: `<div class="msg mine" data-id="${id}">${fields.file}</div>` }) };
  }
  return { ok: true, json: async () => ({ html: '' }) };
};
const alerts = [];
w.alert = m => alerts.push(m);
w.eval(im);
await tick();

// the paper clip: two files, two messages
const input = d.querySelector('[data-attach]');
Object.defineProperty(input, 'files', { configurable: true, value: [new w.File(['a'], 'a.txt'), new w.File(['b'], 'b.png', { type: 'image/png' })] });
input.dispatchEvent(new w.Event('change'));
await tick();
assert.deepEqual(sent.map(s => [s.room, s.file]), [['!r', 'a.txt'], ['!r', 'b.png']]);
assert.equal(d.querySelectorAll('[data-msgs] .msg').length, 2);
assert.equal(d.querySelector('.uploading'), null, 'the «sending» line goes away');

// a picture pasted into the field
const paste = new w.Event('paste', { bubbles: true, cancelable: true });
Object.defineProperty(paste, 'clipboardData', { value: { files: [new w.File(['p'], 'image.png', { type: 'image/png' })] } });
d.querySelector('[data-send] textarea').dispatchEvent(paste);
await tick();
assert.equal(sent.at(-1).file, 'image.png');
assert.ok(paste.defaultPrevented);

// refused: the error, no message
refuse = true;
Object.defineProperty(input, 'files', { configurable: true, value: [new w.File(['c'], 'c.txt')] });
input.dispatchEvent(new w.Event('change'));
await tick();
assert.deepEqual(alerts, ['Гостям нельзя загружать файлы']);
assert.equal(d.querySelectorAll('[data-msgs] .msg').length, 3);

// a voice message: on https the microphone records, the second click sends the file with its length
refuse = false;
let tracksStopped = 0;
Object.defineProperty(w, 'isSecureContext', { configurable: true, value: true });
Object.defineProperty(w.navigator, 'mediaDevices', { configurable: true, value: { getUserMedia: async () => ({ getTracks: () => [{ stop: () => tracksStopped++ }] }) } });
w.MediaRecorder = class {
  static isTypeSupported(t) { return t === 'audio/webm;codecs=opus'; }
  constructor(s, o) { this.mimeType = o.mimeType; }
  start() { this.t = Date.now(); }
  stop() { this.ondataavailable({ data: new w.Blob(['opus'], { type: 'audio/webm' }) }); this.onstop(); }
};
const mic = d.querySelector('[data-voice]');
const realNow = w.Date.now;
mic.click();
await tick();
assert.ok(mic.classList.contains('recording'));
w.Date.now = () => realNow() + 4200;
mic.click();
w.Date.now = realNow;
await tick();
const v = sent.at(-1);
assert.match(v.file, /^voice-.*\.webm$/);
assert.ok(Number(v.duration) >= 4200, 'the length goes with it');
assert.equal(tracksStopped, 1, 'the microphone is let go');
assert.ok(!mic.classList.contains('recording'));
// Esc: nothing is sent
const before = sent.length;
mic.click();
await tick();
d.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Escape' }));
await tick();
assert.equal(sent.length, before);

// «Текст» under a voice message
d.querySelector('[data-msgs]').append(Object.assign(d.createElement('div'), { className: 'msg', innerHTML: '<div data-transcript></div><button data-transcribe>Текст</button>' }));
d.querySelector('[data-msgs] .msg:last-child').dataset.id = '$voice';
d.querySelector('[data-transcribe]').click();
await tick();
assert.match(sent.at(-1).transcribe, /event=%24voice/);
assert.equal(d.querySelector('[data-transcript]').textContent, 'привет из голосового');
assert.equal(d.querySelector('[data-transcribe]'), null);

// on http: the microphone is not given — go24 offers its https page
Object.defineProperty(w, 'isSecureContext', { configurable: true, value: false });
let asked = '';
w.confirm = m => { asked = m; return false; };
mic.click();
await tick();
assert.equal(asked, 'https?');
console.log('im.js: files and voice messages passed');
process.exit(0);
