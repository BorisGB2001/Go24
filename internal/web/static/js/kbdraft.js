// «База знаний»: the page's editor keeps its text by itself (form[data-kb-draft]) — a few seconds after typing
// stops, and every half a minute anyway, so the others see «Сейчас редактирует»; the answer tells when the draft
// was saved ([data-kb-draft-saved]), who else edits the page now ([data-kb-editing]) and who saved it meanwhile
// ([data-kb-changed]). Leaving the editor without saving sends the text once more as it goes (act=leave), but not
// after «Отменить черновик» (form[data-kb-draft-drop]).
(() => {
  const PAUSE = 3000, BEAT = 30000;
  document.querySelectorAll('form[data-kb-draft]').forEach(f => {
    const url = f.dataset.kbDraft, snap = () => (f.elements.title?.value || '') + '\0' + (f.elements.body?.value || '');
    let sent = snap(), timer = 0, done = false;
    const data = act => {
      const d = new FormData();
      d.set('title', f.elements.title?.value || '');
      d.set('body', f.elements.body?.value || '');
      d.set('base', f.elements.base?.value || '');
      if (act) d.set('act', act);
      return d;
    };
    const show = (sel, text) => { const el = f.querySelector(sel); if (el && text !== undefined) el.textContent = text; };
    const save = async () => {
      clearTimeout(timer);
      if (done) return;
      const now = snap();
      try {
        const res = await fetch(url, { method: 'POST', headers: { Accept: 'application/json' }, body: data() });
        if (!res.ok) return;
        sent = now;
        const a = await res.json();
        show('[data-kb-draft-saved]', a.saved || '');
        show('[data-kb-editing]', a.editing || '');
        show('[data-kb-changed]', a.changed || '');
      } catch { /* offline: the next beat tries again */ }
    };
    let beat = 0;
    const go = () => { done = false; clearInterval(beat); beat = setInterval(save, BEAT); };
    f.addEventListener('input', () => {
      if (done) go(); // the form came back with a mistake: typing on keeps the draft again
      clearTimeout(timer);
      timer = setTimeout(save, PAUSE);
    });
    go();
    save(); // the others see the editor open from the start
    const stop = () => { done = true; clearTimeout(timer); clearInterval(beat); };
    f.addEventListener('submit', stop);
    // «Отменить черновик»: nothing is sent after it, or the text would come back as the page reloads
    document.querySelectorAll('form[data-kb-draft-drop]').forEach(x => x.addEventListener('submit', stop));
    window.addEventListener('pagehide', () => {
      if (done) return;
      done = true;
      clearInterval(beat);
      const d = data('leave');
      if (navigator.sendBeacon) navigator.sendBeacon(url, d);
      else fetch(url, { method: 'POST', body: d, keepalive: true });
    });
    f.kbDraftSent = () => sent === snap(); // the text as it is reached the server: leaving loses nothing (kbedit.js)
  });
})();
