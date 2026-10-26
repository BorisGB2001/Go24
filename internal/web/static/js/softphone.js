// The softphone of go24 (/phone, a window of its own): JsSIP registers the employee's account "w<number>" on go24's
// Asterisk over /telephony/ws; calls in and out, mute, hold, the keypad (DTMF while talking). Pages of go24 send it
// their «Позвонить», «Ответить», «Отклонить» (/telephony/live), and it tells them what the line does
// (/telephony/state) — they show an incoming call too. One window per browser: a second one hands its number to the
// first and closes.
(() => {
  const root = document.querySelector('[data-softphone]');
  if (!root || !window.JsSIP) return;
  const t = root.dataset;
  const $ = s => root.querySelector(s);
  const input = $('[data-sp-input]'), status = $('[data-sp-status]'), dot = $('[data-sp-dot]'), audio = $('[data-sp-audio]');
  const who = $('[data-sp-who]'), photo = $('[data-sp-photo]'), name = $('[data-sp-name]'), link = $('[data-sp-link]'), num = $('[data-sp-num]'), timer = $('[data-sp-timer]');
  const callBtn = $('[data-sp-call]'), callText = $('[data-sp-call-text]'), hangBtn = $('[data-sp-hang]'), hangText = $('[data-sp-hang-text]');
  const muteBtn = $('[data-sp-mute]'), holdBtn = $('[data-sp-hold]');
  const host = t.host || location.hostname;
  const pc = { iceServers: [] }; // Asterisk is in the office network: no STUN, no TURN
  const title = document.title;
  let ua = null, session = null, state = 'idle', registered = false, since = 0, tick = null;

  const say = (text, cls) => { status.textContent = text; dot.className = 'sp-dot' + (cls ? ' ' + cls : ''); };
  const post = (url, data) => fetch(url, { method: 'POST', body: new URLSearchParams(data), headers: { Accept: 'application/json' } })
    .then(r => r.ok ? r.json() : null).catch(() => null);

  // ---- the line: the window shows it and tells the pages ----
  const report = (st, number, dir) => {
    state = st;
    document.title = st === 'idle' ? title : (number || '') + ' · ' + title;
    post('/telephony/state', { phone: t.id, state: st, number: number || '', dir: dir || 'out' }).then(ev => {
      if (!ev || !session || st === 'idle') return;
      name.textContent = ev.from || number || '';
      num.textContent = ev.user ? number : '';
      if (ev.url) link.href = ev.url; else link.removeAttribute('href'); // the caller's card
      photo.hidden = !ev.photo;
      if (ev.photo) photo.src = ev.photo;
    });
  };
  const show = (number, display) => {
    who.hidden = false;
    name.textContent = display || number; num.textContent = display ? number : ''; photo.hidden = true;
    link.removeAttribute('href');
    timer.textContent = '';
  };
  const buttons = () => {
    const ringingIn = session && state === 'ringing';
    callBtn.hidden = !!session && !ringingIn;
    callText.textContent = ringingIn ? callText.dataset.answer : callText.dataset.call;
    hangBtn.hidden = !session;
    hangText.textContent = ringingIn ? hangText.dataset.decline : hangText.dataset.hangup;
    muteBtn.hidden = holdBtn.hidden = state !== 'talking';
  };
  const startTimer = () => {
    since = Date.now();
    clearInterval(tick);
    tick = setInterval(() => {
      const s = Math.floor((Date.now() - since) / 1000);
      timer.textContent = Math.floor(s / 60) + ':' + String(s % 60).padStart(2, '0');
    }, 1000);
  };

  // the tune of an incoming call (as ring.js): two tones every three seconds
  let actx = null, beat = null;
  const tune = on => {
    clearInterval(beat); beat = null;
    if (!on) return;
    const play = () => {
      try {
        actx = actx || new (window.AudioContext || window.webkitAudioContext)();
        [0, 0.25].forEach((at, i) => {
          const o = actx.createOscillator(), g = actx.createGain();
          o.frequency.value = i ? 660 : 880;
          g.gain.setValueAtTime(0.0001, actx.currentTime + at);
          g.gain.exponentialRampToValueAtTime(0.2, actx.currentTime + at + 0.02);
          g.gain.exponentialRampToValueAtTime(0.0001, actx.currentTime + at + 0.22);
          o.connect(g).connect(actx.destination);
          o.start(actx.currentTime + at); o.stop(actx.currentTime + at + 0.25);
        });
      } catch { /* no sound */ }
    };
    play();
    beat = setInterval(play, 3000);
  };
  let note = null;

  const remoteAudio = conn => conn.addEventListener('track', e => { audio.srcObject = e.streams[0]; audio.play?.().catch(() => {}); });

  const wire = (s, number, dir) => {
    session = s;
    s.on('peerconnection', e => remoteAudio(e.peerconnection));
    if (s.connection) remoteAudio(s.connection);
    s.on('icecandidate', e => { if (e.candidate.type === 'host') e.ready(); }); // the office network: host candidates do
    s.on('progress', () => { if (dir === 'out') say(t.ringing, 'busy'); });
    s.on('confirmed', () => {
      tune(false); note?.close?.();
      say(number, 'talk'); startTimer(); report('talking', number, dir); buttons();
    });
    s.on('hold', () => say(t.onHold, 'busy'));
    s.on('unhold', () => say(number, 'talk'));
    const end = e => {
      tune(false); note?.close?.(); note = null;
      clearInterval(tick);
      const c = JsSIP.C.causes;
      let text = t.ended;
      if (e && [c.BUSY, c.REJECTED, c.UNAVAILABLE, c.NOT_FOUND, c.NO_ANSWER].includes(e.cause)) text = t.busy;
      else if (e && e.originator !== 'local' && ![c.BYE, c.CANCELED, c.ANSWERED_ELSEWHERE].includes(e.cause) && e.cause) text = t.failed.replace('%s', e.cause);
      say(text, registered ? 'ok' : '');
      session = null; audio.srcObject = null;
      muteBtn.classList.remove('on'); holdBtn.classList.remove('on');
      report('idle', number, dir); buttons();
      setTimeout(() => { if (!session) { who.hidden = true; if (registered) say(t.ready, 'ok'); } }, 4000);
    };
    s.on('ended', end);
    s.on('failed', end);
  };

  const dial = number => {
    number = (number || '').replace(/[\s\-().]/g, '').replace(/^tel:/, '');
    if (!/^\+?[0-9*#]{1,20}$/.test(number)) { say(t.badNumber, 'err'); return; }
    if (session) { say(t.busyLine, 'busy'); return; }
    if (!ua || !registered) { pending = number; return; }
    input.value = number;
    show(number);
    say(t.dialing, 'busy');
    state = 'dialing'; // buttons() before JsSIP answers
    try {
      const s = ua.call(`sip:${number}@${host}`, {
        mediaConstraints: { audio: true, video: false }, pcConfig: pc, rtcOfferConstraints: { offerToReceiveAudio: true, offerToReceiveVideo: false },
      });
      wire(s, number, 'out');
      report('dialing', number, 'out');
    } catch (e) { say(t.failed.replace('%s', e.message || e), 'err'); }
    buttons();
  };
  let pending = t.dial || '';

  const answer = () => {
    if (!session || state !== 'ringing') return;
    tune(false);
    session.answer({ mediaConstraints: { audio: true, video: false }, pcConfig: pc });
  };
  // «Отклонить» an incoming call says «busy» (the log shows it so); otherwise the call ends
  const hangup = () => { if (session) try { session.terminate(state === 'ringing' ? { status_code: 486, reason_phrase: 'Busy Here' } : undefined); } catch { /* gone already */ } };

  // ---- the controls ----
  callBtn.addEventListener('click', () => { if (session) answer(); else dial(input.value); });
  hangBtn.addEventListener('click', hangup);
  input.addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); if (session) answer(); else dial(input.value); } });
  $('[data-sp-back]').addEventListener('click', () => { input.value = input.value.slice(0, -1); input.focus(); });
  $('[data-sp-keys]').addEventListener('click', e => {
    const k = e.target.closest('[data-key]')?.dataset.key;
    if (!k) return;
    if (session && state === 'talking') try { session.sendDTMF(k); } catch { /* no DTMF now */ }
    input.value += k;
  });
  muteBtn.addEventListener('click', () => {
    if (!session) return;
    const on = !session.isMuted().audio;
    on ? session.mute({ audio: true }) : session.unmute({ audio: true });
    muteBtn.classList.toggle('on', on);
    muteBtn.title = on ? t.unmute : t.mute;
    muteBtn.innerHTML = on ? '<i class="i i-microphone-off" aria-hidden="true"></i>' : '<i class="i i-microphone" aria-hidden="true"></i>';
  });
  holdBtn.addEventListener('click', () => {
    if (!session) return;
    const on = !session.isOnHold().local;
    on ? session.hold() : session.unhold();
    holdBtn.classList.toggle('on', on);
    holdBtn.title = on ? t.unhold : t.hold;
  });
  // a notification needs the permission, asked on a click
  addEventListener('click', () => { if (window.Notification?.permission === 'default') Notification.requestPermission().catch(() => {}); }, { once: true });

  // ---- the commands of the pages ----
  const command = d => {
    if (d.cmd === 'dial') dial(d.number);
    else if (d.cmd === 'answer') answer();
    else if (d.cmd === 'hangup') hangup();
  };
  const listen = () => {
    const es = new EventSource('/telephony/live?id=' + encodeURIComponent(t.id));
    es.addEventListener('cmd', ev => { try { command(JSON.parse(ev.data)); } catch { /* bad event */ } });
  };

  // ---- the line itself ----
  const start = () => {
    const socket = new JsSIP.WebSocketInterface((location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + '/telephony/ws');
    ua = new JsSIP.UA({ sockets: [socket], uri: `sip:${t.login}@${host}`, password: t.password, register_expires: 90,
      session_timers: false, user_agent: 'go24' });
    ua.on('connecting', () => say(t.connecting));
    ua.on('registered', () => {
      registered = true;
      if (!session) say(t.ready, 'ok');
      report('online');
      if (pending) { const n = pending; pending = ''; history.replaceState(null, '', location.pathname); dial(n); }
    });
    const lost = () => { registered = false; if (!session) say(t.offline, 'err'); };
    ua.on('unregistered', lost);
    ua.on('registrationFailed', lost);
    ua.on('disconnected', lost);
    ua.on('newRTCSession', e => {
      if (e.originator !== 'remote') return;
      const s = e.session;
      if (session) { s.terminate({ status_code: 486, reason_phrase: 'Busy Here' }); return; }
      const number = s.remote_identity.uri.user, display = s.remote_identity.display_name;
      state = 'ringing';
      wire(s, number, 'in');
      show(number, display && display !== number ? display : '');
      say(t.ringing, 'busy');
      buttons();
      tune(true);
      report('ringing', number, 'in');
      if (window.Notification?.permission === 'granted') {
        try { note = new Notification(t.incoming.replace('%s', display || number), { tag: 'go24-phone', requireInteraction: true }); note.onclick = () => focus(); } catch { /* none */ }
      }
    });
    ua.start();
    listen();
    addEventListener('pagehide', () => {
      navigator.sendBeacon?.('/telephony/state', new URLSearchParams({ phone: t.id, state: 'offline' }));
      try { ua.stop(); } catch { /* closing */ }
    });
  };

  // the microphone is asked at once, so a call does not wait for the question
  const mic = () => navigator.mediaDevices?.getUserMedia({ audio: true })
    .then(st => st.getTracks().forEach(x => x.stop()))
    .catch(() => say(t.micDenied, 'err'));

  // one softphone per browser: an older window takes the number, this one closes
  const bc = window.BroadcastChannel ? new BroadcastChannel('go24-phone') : null;
  let alone = true;
  bc?.addEventListener('message', ev => {
    const d = ev.data || {};
    if (d.hello && ua) { bc.postMessage({ here: d.hello }); if (d.dial) dial(d.dial); }
    if (d.here === t.id + seq) alone = false;
  });
  const seq = Math.random().toString(36).slice(2);
  bc?.postMessage({ hello: t.id + seq, dial: pending });
  setTimeout(() => {
    if (!alone) { window.close(); return; }
    buttons();
    mic().finally(start);
  }, bc ? 300 : 0);
})();
