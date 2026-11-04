// «Озвучить текст» (tts.js) in jsdom with a fake server: each file field of a sound gets the panel once (a form added
// later too); «Озвучить» posts the text, the voice and the rate, puts the WAV into the field as tts.wav and shows the
// player; an empty text asks nothing; the server's message is shown when it refuses. node test/js/tts.test.mjs
import { JSDOM } from 'jsdom';
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';

const script = readFileSync(new URL('../../internal/web/static/js/tts.js', import.meta.url), 'utf8');
const tpl = `<template id="tts-tpl"><div class="tts"><button type="button" data-tts-toggle>Озвучить текст</button>
  <div data-tts-panel hidden data-wait="…" data-done="Готово" data-fail="Недоступно"><textarea data-tts-text></textarea>
  <select data-tts-voice><option value="ru_RU-irina-medium">Ирина</option><option value="en_US-amy-medium">Amy</option></select>
  <select data-tts-rate><option value="1" selected>1</option><option value="1.2">1.2</option></select>
  <button type="button" data-tts-speak>Озвучить</button><audio hidden data-tts-audio></audio><p data-tts-msg></p></div></div></template>`;
const dom = new JSDOM(`<!doctype html><body>${tpl}<form id="f"><input type="file" name="greeting" id="g" data-tts>
  <input type="file" name="other" id="o"></form></body>`, { runScripts: 'outside-only', url: 'http://go24.test/settings/telephony' });
const w = dom.window, d = w.document;
const sent = [];
let answer = { ok: true };
w.fetch = async (url, opts) => {
  sent.push({ url, method: opts.method, body: Object.fromEntries(opts.body.entries()) });
  return answer.ok ? { ok: true, blob: async () => new w.Blob(['RIFF....'], { type: 'audio/wav' }) }
    : { ok: false, text: async () => answer.text };
};
w.URL.createObjectURL = () => 'blob:wav';
w.URL.revokeObjectURL = () => {};
w.HTMLMediaElement.prototype.play = () => Promise.resolve();
class DT { constructor() { this.list = []; this.items = { add: f => this.list.push(f) }; } get files() { return this.list; } }
w.DataTransfer = DT;
const g = d.getElementById('g');
let files = null;
Object.defineProperty(g, 'files', { get: () => files, set: v => { files = v; } });
w.eval(script);
const tick = () => new Promise(r => setTimeout(r, 10));

assert.equal(d.querySelectorAll('.tts').length, 1, 'only the field of a sound gets the panel');
assert.equal(g.nextElementSibling.className, 'tts');
const box = g.nextElementSibling, $ = s => box.querySelector(s);
$('[data-tts-toggle]').click();
assert.equal($('[data-tts-panel]').hidden, false, 'the button opens the panel');

$('[data-tts-speak]').click();
await tick();
assert.equal(sent.length, 0, 'an empty text asks nothing');

let changed = 0;
g.addEventListener('change', () => changed++);
$('[data-tts-text]').value = '  Здравствуйте!  ';
$('[data-tts-voice]').value = 'en_US-amy-medium';
$('[data-tts-rate]').value = '1.2';
$('[data-tts-speak]').click();
await tick();
assert.deepEqual(sent[0], { url: '/telephony/tts', method: 'POST', body: { text: 'Здравствуйте!', voice: 'en_US-amy-medium', rate: '1.2' } });
assert.equal(files.length, 1);
assert.equal(files[0].name, 'tts.wav', 'the WAV is put into the field');
assert.equal(changed, 1);
assert.equal($('[data-tts-audio]').hidden, false, 'the player is shown');
assert.equal($('[data-tts-msg]').textContent, 'Готово');
assert.equal($('[data-tts-speak]').disabled, false);

answer = { ok: false, text: 'Введите текст до 1000 знаков.\n' };
$('[data-tts-speak]').click();
await tick();
assert.equal($('[data-tts-msg]').textContent, 'Введите текст до 1000 знаков.', 'the server\'s message');

// a form opened later (a drawer) gets its panel too, once
const f2 = d.createElement('div');
f2.innerHTML = '<input type="file" name="sound" id="s" data-tts>';
d.body.appendChild(f2);
await tick();
assert.equal(d.querySelectorAll('.tts').length, 2);
assert.equal(d.getElementById('s').nextElementSibling.className, 'tts');
console.log('tts: ok');
