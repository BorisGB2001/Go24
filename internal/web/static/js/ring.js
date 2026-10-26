// «Звонит …»: a call from a chat rings on any page of go24 (the event comes through the stream of menu.js): who
// calls, a tune, «Ответить» opens the call (with or without the camera), «Отклонить» tells the caller. It stops when
// answered on another device, when the caller gives up, or when the time is out.
(() => {
  const texts = document.querySelector('[data-ring-texts]');
  if (!texts) return;
  const t = texts.dataset;
  const rings = new Map(); // ring → {el, timer}
  let ctx = null, beat = null;
  // the tune: two short tones every three seconds (Web Audio, nothing to download); a browser may keep it silent
  // until the page has been clicked once, the window still shows
  const tune = on => {
    clearInterval(beat); beat = null;
    if (!on || document.body.hasAttribute('data-dnd')) return; // «Не беспокоить»: the call shows, silently
    const play = () => {
      try {
        ctx = ctx || new (window.AudioContext || window.webkitAudioContext)();
        [0, 0.25].forEach((at, i) => {
          const o = ctx.createOscillator(), g = ctx.createGain();
          o.frequency.value = i ? 660 : 880;
          g.gain.setValueAtTime(0.0001, ctx.currentTime + at);
          g.gain.exponentialRampToValueAtTime(0.2, ctx.currentTime + at + 0.02);
          g.gain.exponentialRampToValueAtTime(0.0001, ctx.currentTime + at + 0.22);
          o.connect(g).connect(ctx.destination);
          o.start(ctx.currentTime + at); o.stop(ctx.currentTime + at + 0.25);
        });
      } catch { /* no sound */ }
    };
    play();
    beat = setInterval(play, 3000);
  };
  const close = id => {
    const r = rings.get(id);
    if (!r) return;
    clearTimeout(r.timer); r.el.remove(); r.note?.close?.();
    rings.delete(id);
    if (!rings.size) tune(false);
  };
  const post = (id, action) => fetch(`/call/ring/${encodeURIComponent(id)}/${action}`, { method: 'POST', headers: { Accept: 'application/json' } }).catch(() => {});
  const open = url => { if (!window.open(url, '_blank')) location.href = url; };
  const button = (cls, text, fn) => { const b = document.createElement('button'); b.type = 'button'; b.className = cls; b.textContent = text; b.addEventListener('click', fn); return b; };
  addEventListener('go24:call', e => {
    const d = e.detail || {};
    if (d.kind === 'stop') { close(d.ring); return; }
    if (d.kind !== 'ring' || rings.has(d.ring) || d.left <= 0) return;
    const el = document.createElement('div');
    el.className = 'ringpop';
    el.dataset.ring = d.ring;
    if (d.photo) { const img = document.createElement('img'); img.src = d.photo; img.alt = ''; el.append(img); }
    const who = document.createElement('div');
    who.className = 'ringwho';
    const b = document.createElement('b');
    b.textContent = t.calling.replace('%s', d.from || '');
    const what = document.createElement('span');
    what.className = 'small muted';
    what.textContent = (d.video ? t.video : t.audio) + (d.chat ? ' · ' + t.inChat.replace('%s', d.chat) : '');
    who.append(b, what);
    const bar = document.createElement('div');
    bar.className = 'row';
    bar.append(button('primary', t.accept, () => { post(d.ring, 'accept'); close(d.ring); open(d.url); }));
    if (d.video) bar.append(button('ghost', t.audioOnly, () => { post(d.ring, 'accept'); close(d.ring); open(d.url.replace('video=1', 'video=0')); }));
    bar.append(button('danger', t.decline, () => { post(d.ring, 'decline'); close(d.ring); }));
    el.append(who, bar);
    document.body.append(el);
    const r = { el, timer: setTimeout(() => close(d.ring), (d.left || 45) * 1000) };
    if (document.visibilityState !== 'visible' && window.Notification?.permission === 'granted') {
      try { r.note = new Notification(b.textContent, { body: what.textContent, tag: 'go24-call-' + d.ring, requireInteraction: true }); r.note.onclick = () => { focus(); }; } catch { /* none */ }
    }
    rings.set(d.ring, r);
    tune(true);
  });
})();
