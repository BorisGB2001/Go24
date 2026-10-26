// The softphone of go24 (/phone, a window of its own): JsSIP registers the employee's account "w<number>" on go24's
// Asterisk over /telephony/ws; calls in and out, mute, hold, the keypad (DTMF while talking), transfer — at once (REFER)
// or after talking to the colleague first (a second call, then REFER with Replaces). Pages of go24 send it
// their «Позвонить», «Ответить», «Отклонить» (/telephony/live), and it tells them what the line does
// (/telephony/state) — they show an incoming call too. The call card (/telephony/card): the client of
// CRM with their open deals, or «Создать лид / контакт», the calls before and «Комментарий к звонку» (/telephony/note),
// kept after the call till the next one. One window per browser: a second one hands its number to the
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
  const xferBtn = $('[data-sp-xfer]'), xfer = $('[data-sp-xfer-panel]'), xferAsk = $('[data-sp-xfer-ask]'), xferTalk = $('[data-sp-xfer-talk]');
  const xferTo = $('[data-sp-xfer-to]'), xferWho = $('[data-sp-xfer-who]'), completeBtn = $('[data-sp-complete]'), audio2 = $('[data-sp-audio2]');
  const host = t.host || location.hostname;
  const pc = { iceServers: [] }; // Asterisk is in the office network: no STUN, no TURN
  const title = document.title;
  let ua = null, session = null, state = 'idle', registered = false, since = 0, tick = null;
  let consult = null, transferred = ''; // the call to the colleague before a transfer; the number the call went to

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
  // ---- the call card ----
  const card = $('[data-sp-card]'), noteBox = $('[data-sp-note]'), noteState = $('[data-sp-note-state]');
  let cardFor = null; // {number, at, saved}: the call the card and its comment are of
  const el = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text) e.textContent = text; return e; };
  const anchor = (url, text) => { const a = el('a', '', text); a.href = url; a.target = '_blank'; a.rel = 'noopener'; return a; };
  const saveNote = () => {
    const c = cardFor;
    if (!c || noteBox.value.trim() === c.saved) return;
    const text = noteBox.value.trim();
    post('/telephony/note', { number: c.number, at: c.at, comment: text }).then(r => {
      if (!r || cardFor !== c) return;
      c.saved = text;
      noteState.textContent = r.call ? card.dataset.saved : card.dataset.pending;
    });
  };
  const fill = (d, number, dir) => {
    const client = $('[data-sp-client]'), deals = $('[data-sp-deals]'), create = $('[data-sp-create]'), calls = $('[data-sp-calls]');
    const cl = d.client;
    client.hidden = !cl;
    if (cl) {
      $('[data-sp-client-kind]').textContent = cl.kind;
      const nameEl = $('[data-sp-client-name]');
      nameEl.replaceChildren(cl.url ? anchor(cl.url, cl.name) : el('span', '', cl.name));
      const extra = $('[data-sp-client-extra]');
      extra.replaceChildren();
      if (cl.company) { const c = el('div', 'small', card.dataset.company + ': '); c.append(anchor(cl.company_url, cl.company)); extra.append(c); }
      if (cl.stage) extra.append(el('div', 'small', card.dataset.stage + ': ' + cl.stage));
      if (cl.responsible) extra.append(el('div', 'small muted', card.dataset.resp + ': ' + cl.responsible));
    }
    const dl = (cl && cl.deals) || [];
    deals.hidden = !dl.length;
    $('[data-sp-deal-list]').replaceChildren(...dl.map(x => {
      const li = el('li');
      const st = el('span', 'sp-stage', x.stage);
      if (x.color) st.style.setProperty('--c', x.color);
      li.append(anchor(x.url, x.title), ' ', st, el('span', 'muted small', ' ' + x.amount));
      return li;
    }));
    const cr = d.create || [];
    create.hidden = !cr.length;
    create.replaceChildren(...cr.map(x => {
      const b = el('button', 'ghost small', '+ ' + x.title);
      b.type = 'button';
      b.addEventListener('click', () => {
        b.disabled = true;
        const win = window.open('', '_blank'); // opened on the click: not a blocked popup
        post('/telephony/card', { number, what: x.what, dir }).then(r => {
          if (r && r.url) { if (win) win.location = r.url; else window.open(r.url, '_blank'); load(number, dir, true); }
          else { win?.close(); b.disabled = false; }
        });
      });
      return b;
    }));
    const cs = d.calls || [];
    calls.hidden = !cs.length;
    $('[data-sp-call-list]').replaceChildren(...cs.map(x => {
      const li = el('li', 'sp-call-' + x.dir);
      li.append(el('span', 'small', x.at + ' · ' + x.kind), el('span', 'muted small', ' · ' + (x.who ? x.who + ' · ' : '') + x.time + ' · ' + x.status));
      if (x.comment) li.append(el('div', 'small sp-call-note', x.comment));
      return li;
    }));
  };
  const load = (number, dir, again) => {
    fetch('/telephony/card?number=' + encodeURIComponent(number), { headers: { Accept: 'application/json' } })
      .then(r => r.ok ? r.json() : null).catch(() => null).then(d => {
        if (!d || d.colleague) { if (!again) card.hidden = true; return; }
        if (!again) {
          cardFor = { number, at: d.at, saved: d.comment || '' };
          noteBox.value = d.comment || '';
          noteState.textContent = '';
        }
        fill(d, number, dir);
        card.hidden = false;
      });
  };
  const openCard = (number, dir) => {
    saveNote(); // the comment of the call before
    cardFor = null;
    card.hidden = true;
    load(number, dir, false);
  };
  $('[data-sp-note-save]').addEventListener('click', saveNote);
  noteBox.addEventListener('blur', saveNote);
  $('[data-sp-card-close]').addEventListener('click', () => { saveNote(); card.hidden = true; cardFor = null; });

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
    xferBtn.hidden = state !== 'talking' || !!consult;
    if (state !== 'talking') xfer.hidden = true;
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

  const remoteAudio = (conn, el = audio) => conn.addEventListener('track', e => { el.srcObject = e.streams[0]; el.play?.().catch(() => {}); });

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
      let text = transferred ? t.transferred.replace('%s', transferred) : t.ended;
      if (transferred) transferred = '';
      else if (e && [c.BUSY, c.REJECTED, c.UNAVAILABLE, c.NOT_FOUND, c.NO_ANSWER].includes(e.cause)) text = t.busy;
      else if (e && e.originator !== 'local' && ![c.BYE, c.CANCELED, c.ANSWERED_ELSEWHERE].includes(e.cause) && e.cause) text = t.failed.replace('%s', e.cause);
      say(text, registered ? 'ok' : '');
      session = null; audio.srcObject = null;
      if (consult) try { consult.terminate(); } catch { /* gone already */ }
      muteBtn.classList.remove('on'); holdBtn.classList.remove('on');
      saveNote();
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
    openCard(number, 'out');
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

  // ---- transfer: at once, or after talking to the colleague (the client on hold meanwhile) ----
  const clean = v => (v || '').replace(/[\s\-().]/g, '');
  const target = () => {
    const n = clean(xferTo.value);
    if (!/^\+?[0-9*#]{1,20}$/.test(n)) { say(t.badNumber, 'err'); xferTo.focus(); return ''; }
    return n;
  };
  const xferMode = talking => { xferAsk.hidden = talking; xferTalk.hidden = !talking; };
  const refer = (to, opts = {}) => {
    try {
      session.refer(to, { ...opts, eventHandlers: {
        accepted: () => { transferred = clean(xferTo.value); },
        requestFailed: () => say(t.xferFailed, 'err'),
        failed: () => say(t.xferFailed, 'err'),
      } });
    } catch { say(t.xferFailed, 'err'); }
  };
  const setHold = on => {
    if (!session || session.isOnHold().local === on) return;
    on ? session.hold() : session.unhold();
    holdBtn.classList.toggle('on', on);
    holdBtn.title = on ? t.unhold : t.hold;
  };
  xferBtn.addEventListener('click', () => {
    xfer.hidden = !xfer.hidden;
    xferMode(false);
    if (!xfer.hidden) xferTo.focus();
  });
  $('[data-sp-xfer-cancel]').addEventListener('click', () => { xfer.hidden = true; });
  $('[data-sp-blind]').addEventListener('click', () => {
    const n = target();
    if (n && session) refer(`sip:${n}@${host}`);
  });
  $('[data-sp-consult]').addEventListener('click', () => {
    const n = target();
    if (!n || !session || consult) return;
    setHold(true);
    xferMode(true);
    xferWho.textContent = t.xferCalling.replace('%s', n);
    completeBtn.disabled = true;
    try {
      consult = ua.call(`sip:${n}@${host}`, { mediaConstraints: { audio: true, video: false }, pcConfig: pc,
        rtcOfferConstraints: { offerToReceiveAudio: true, offerToReceiveVideo: false } });
    } catch { consult = null; xferMode(false); say(t.xferFailed, 'err'); return; }
    const c = consult;
    c.on('peerconnection', e => remoteAudio(e.peerconnection, audio2));
    if (c.connection) remoteAudio(c.connection, audio2);
    c.on('icecandidate', e => { if (e.candidate.type === 'host') e.ready(); });
    c.on('confirmed', () => { xferWho.textContent = t.xferTalking.replace('%s', n); completeBtn.disabled = false; });
    const gone = () => {
      if (consult !== c) return;
      consult = null; audio2.srcObject = null;
      if (session && !transferred) { xferMode(false); say(t.xferEnded, 'busy'); }
      buttons();
    };
    c.on('ended', gone);
    c.on('failed', gone);
    buttons();
  });
  completeBtn.addEventListener('click', () => {
    if (!session || !consult) return;
    refer(consult.remote_identity.uri, { replaces: consult });
  });
  $('[data-sp-return]').addEventListener('click', () => {
    if (consult) try { consult.terminate(); } catch { /* gone already */ }
    setHold(false);
    xfer.hidden = true;
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
      openCard(number, 'in');
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
