// «Озвучить текст» (GO24DEV-1002): beside each file field of a sound ([data-tts]) — a text, a voice and a rate; «Озвучить»
// asks go24 for the WAV (POST /telephony/tts), plays it and puts it into the field as tts.wav, so the form saves it as
// an uploaded file. The panel is #tts-tpl of the page (the template "tts").
(() => {
  const tpl = document.getElementById('tts-tpl');
  if (!tpl) return;
  const attach = input => {
    if (input.dataset.ttsOn) return;
    input.dataset.ttsOn = '1';
    const box = tpl.content.firstElementChild.cloneNode(true);
    input.insertAdjacentElement('afterend', box);
    const $ = s => box.querySelector(s);
    const panel = $('[data-tts-panel]'), msg = $('[data-tts-msg]'), audio = $('[data-tts-audio]'), speak = $('[data-tts-speak]');
    $('[data-tts-toggle]').addEventListener('click', () => {
      panel.hidden = !panel.hidden;
      if (!panel.hidden) $('[data-tts-text]').focus();
    });
    speak.addEventListener('click', async () => {
      const text = $('[data-tts-text]').value.trim();
      if (!text) { $('[data-tts-text]').focus(); return; }
      const body = new FormData();
      body.set('text', text);
      body.set('voice', $('[data-tts-voice]').value);
      body.set('rate', $('[data-tts-rate]').value);
      speak.disabled = true;
      msg.textContent = panel.dataset.wait;
      try {
        const res = await fetch('/telephony/tts', { method: 'POST', body });
        if (!res.ok) { msg.textContent = (await res.text()).trim() || panel.dataset.fail; return; }
        const wav = await res.blob();
        const dt = new DataTransfer();
        dt.items.add(new File([wav], 'tts.wav', { type: 'audio/wav' }));
        input.files = dt.files;
        input.dispatchEvent(new Event('change', { bubbles: true }));
        if (audio.src) URL.revokeObjectURL(audio.src);
        audio.src = URL.createObjectURL(wav);
        audio.hidden = false;
        audio.play?.()?.catch?.(() => {});
        msg.textContent = panel.dataset.done;
      } catch {
        msg.textContent = panel.dataset.fail;
      } finally {
        speak.disabled = false;
      }
    });
  };
  const scan = root => root.querySelectorAll('input[type=file][data-tts]').forEach(attach);
  scan(document);
  // forms of a drawer or a <template> opened later
  new MutationObserver(ms => ms.forEach(m => m.addedNodes.forEach(n => n.nodeType === 1 && scan(n.matches?.('[data-tts]') ? n.parentNode : n))))
    .observe(document.body, { childList: true, subtree: true });
})();
