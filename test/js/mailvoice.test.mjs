// The voice in the mail (static/js/mailvoice.js) in jsdom: «Диктовка» records a phrase at a time — a pause after
// speech closes it — sends each to /mail/dictate and puts the texts at the cursor in the order spoken (a slow
// answer does not swap them), silence is not sent, a second click or Esc stops and lets the microphone go, no
// microphone — the message says so; «Прочитать вслух» speaks the subject, the sender and the letter paragraph by
// paragraph (a long one in pieces), «Пауза», «Следующий», «Остановить», the player gone at the end.
// node test/js/mailvoice.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/mailvoice.js', import.meta.url), 'utf8');
const wait = ms => new Promise(r => setTimeout(r, ms));
const until = async (f, what) => {
  for (let i = 0; i < 300; i++) { if (f()) return; await wait(10); }
  assert.fail('waited in vain: ' + what + ' — ' + globalThis.__ed?.textContent);
};

const page = `<!doctype html><body>
<form data-compose><span class="mc-dictate"><button type="button" data-dictate aria-pressed="false">Диктовка</button>
  <select data-dictate-lang><option value="">язык интерфейса</option><option value="en">английский</option></select>
  <span data-dictate-state hidden></span>
  <template data-dictate-labels><span data-l="listening">Говорите…</span><span data-l="recognizing">Распознаю…</span><span data-l="no_mic">Нет микрофона</span></template></span>
  <div class="mc-editor" contenteditable="true" data-editor><p>Добрый день</p></div></form>
<article class="mailletter" data-letter="m1"><h2>Отчёт за октябрь</h2><div class="row mread-actions"><details open><div><button type="button" data-speak>Прочитать вслух</button></div></details></div>
  <details class="mmsg" open data-msg="m1"><summary><div class="mmsg-who"><b>Анна Петрова</b></div></summary>
    <div class="mailbody">Коллеги, привет.<br>Отчёт во вложении.<br><br><div><p>Итоги:</p><ul><li>продажи растут</li><li>${'очень длинная фраза '.repeat(15)}конец. ${'вторая длинная фраза '.repeat(8)}всё.</li></ul></div></div></details>
  <template data-speak-labels><span data-l="title">Прочитать вслух</span><span data-l="pause">Пауза</span><span data-l="resume">Продолжить</span>
    <span data-i="play"><i class="i-play"></i></span><span data-i="pause"><i class="i-pause"></i></span>
    <div data-bar><b>Прочитать вслух</b><button data-sp="prev">⏮</button><button data-sp="pause">⏸</button><button data-sp="next">⏭</button><span data-sp-at></span>
      <select data-sp-rate><option>1</option><option>2</option></select><select data-sp-voice hidden></select><button data-sp="stop">⏹</button></div></template></article></body>`;

const dom = new JSDOM(page, { runScripts: 'outside-only', url: 'http://go24.test/mail', pretendToBeVisual: true });
const w = dom.window, d = w.document;
const store = new Map();
Object.defineProperty(w, 'localStorage', { value: { getItem: k => store.get(k) ?? null, setItem: (k, v) => store.set(k, String(v)) } });

// the microphone: a level the test sets; each recorder gives one piece when stopped
let level = 0, micOK = true, tracksStopped = 0;
const recs = [];
w.navigator.mediaDevices = { getUserMedia: async () => {
  if (!micOK) throw new Error('denied');
  return { getTracks: () => [{ stop: () => tracksStopped++ }] };
} };
w.MediaRecorder = class {
  constructor() { this.state = 'inactive'; this.mimeType = 'audio/webm'; recs.push(this); }
  start() { this.state = 'recording'; }
  stop() { this.state = 'inactive'; this.ondataavailable({ data: new w.Blob(['opus' + recs.indexOf(this)], { type: 'audio/webm' }) }); setTimeout(() => this.onstop(), 0); }
};
w.AudioContext = class {
  createAnalyser() { return { fftSize: 0, getFloatTimeDomainData: b => b.fill(level) }; }
  createMediaStreamSource() { return { connect() {} }; }
  close() {}
};
// Whisper: the first phrase answers slowly
const sent = [];
const answers = ['Первая фраза.', 'Вторая.', 'Третья.'];
w.fetch = async (url, o) => {
  const n = sent.length;
  sent.push({ url: String(url), lang: o.body.get('lang'), file: o.body.get('audio') });
  await wait(n === 0 ? 400 : 10);
  return { ok: true, json: async () => ({ text: answers[n] }) };
};

// the voices of the browser
const spoken = [];
let paused = 0;
w.SpeechSynthesisUtterance = class { constructor(t) { this.text = t; } };
w.speechSynthesis = { speaking: false, getVoices: () => [], cancel() { this.q = []; }, pause() { paused++; }, resume() {},
  speak(u) { spoken.push(u.text); setTimeout(() => { if (!w.__hold) u.onend?.(); }, 5); } };

w.eval(script);

// ---------- «Диктовка» ----------
const ed = d.querySelector('[data-editor]'), btn = d.querySelector('[data-dictate]'), state = d.querySelector('[data-dictate-state]');
let inputs = 0;
globalThis.__ed = ed;
ed.addEventListener('input', () => inputs++);
const caret = d.createRange();
caret.setStart(ed.querySelector('p').firstChild, 'Добрый день'.length);
d.getSelection().removeAllRanges();
d.getSelection().addRange(caret);

btn.click();
await until(() => btn.classList.contains('on'), 'dictation on');
assert.equal(state.textContent, 'Говорите…');
assert.equal(btn.getAttribute('aria-pressed'), 'true');
// silence alone is not sent
await wait(1200);
assert.equal(sent.length, 0, 'silence sent');
// a phrase, then a pause
level = 0.1; await wait(300); level = 0;
await until(() => sent.length === 1, 'the first phrase sent after the pause');
assert.equal(sent[0].url, '/mail/dictate');
assert.equal(sent[0].lang, null, 'the interface language by default');
assert.equal(sent[0].file.name, 'dictation.webm');
assert.equal(state.textContent, 'Распознаю…');
// the second phrase, ended by a click; its quick answer waits for the first
level = 0.1; await wait(200);
btn.click();
level = 0;
await until(() => sent.length === 2, 'the last phrase sent on stop');
assert.equal(tracksStopped, 1, 'the microphone is let go');
assert.ok(!btn.classList.contains('on'));
await until(() => ed.textContent.includes('Вторая.'), 'both texts in');
assert.equal(ed.textContent, 'Добрый день Первая фраза. Вторая.');
assert.ok(inputs >= 2, 'the draft is told');
await until(() => state.hidden, 'the state gone');

// the language chosen is sent and kept; Esc stops
const sel = d.querySelector('[data-dictate-lang]');
sel.value = 'en';
sel.dispatchEvent(new w.Event('change', { bubbles: true }));
assert.equal(store.get('go24.dictate.lang'), 'en');
btn.click();
await until(() => btn.classList.contains('on'), 'dictation on again');
level = 0.1; await wait(200); level = 0;
d.dispatchEvent(new w.KeyboardEvent('keydown', { key: 'Escape' }));
await until(() => sent.length === 3, 'Esc sends the phrase');
assert.equal(sent[2].lang, 'en');
await until(() => ed.textContent.endsWith('Вторая. Третья.'), 'the third text at the end');
assert.equal(tracksStopped, 2);

// no microphone
micOK = false;
btn.click();
await until(() => !state.hidden, 'the message');
assert.equal(state.textContent, 'Нет микрофона');
assert.ok(state.classList.contains('err'));
assert.ok(!btn.classList.contains('on'));

// ---------- «Прочитать вслух» ----------
assert.ok(d.documentElement.classList.contains('go24-tts'), 'the button shown where there are voices');
d.querySelector('[data-speak]').click();
const bar = d.querySelector('.mspeak');
assert.ok(bar, 'the player over the letter');
assert.equal(bar.previousElementSibling, d.querySelector('.mread-actions'));
assert.ok(!d.querySelector('.mread-actions details').open, 'the menu closed');
await until(() => !d.querySelector('.mspeak'), 'read to the end');
assert.deepEqual(spoken.slice(0, 6), ['Отчёт за октябрь', 'Анна Петрова', 'Коллеги, привет.', 'Отчёт во вложении.', 'Итоги:', 'продажи растут']);
const long = spoken.slice(6);
assert.ok(long.length >= 2 && long.every(s => s.length <= 320), 'a long paragraph in pieces: ' + long.map(s => s.length));
assert.ok(long.join('').trim().endsWith('всё.'));

// pause, next, stop
spoken.length = 0;
w.__hold = true; // each paragraph waits
d.querySelector('[data-speak]').click();
const bar2 = d.querySelector('.mspeak');
assert.equal(bar2.querySelector('[data-sp-at]').textContent, '1 / 7');
bar2.querySelector('[data-sp="pause"]').click();
assert.equal(paused, 1);
assert.equal(bar2.querySelector('[data-sp="pause"]').title, 'Продолжить');
bar2.querySelector('[data-sp="next"]').click();
assert.equal(spoken.at(-1), 'Анна Петрова');
assert.equal(bar2.querySelector('[data-sp-at]').textContent, '2 / 7');
bar2.querySelector('[data-sp="stop"]').click();
assert.ok(!d.querySelector('.mspeak'), 'the player gone');

console.log('mailvoice.test.mjs: ok');
process.exit(0);
