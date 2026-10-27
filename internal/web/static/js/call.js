// go24 call page: joins the LiveKit room with the token go24 signed, shows everyone's video and sound, and switches
// the microphone, the camera and screen sharing; — the people of the call with their microphones and
// cameras, the choice of devices, «Сетка» or «Докладчик» (a shared screen is shown large by itself), full screen,
// ringing the chat's people who are not in the call yet, «Поднять руку», reactions and the chat of the call,
// «Шумоподавление», the host of the call turning off others' microphones and cameras and putting people out; the
// background of one's video — callbg.js.
(() => {
  const box = document.querySelector('[data-call]');
  if (!box || !window.LivekitClient) return;
  const LK = window.LivekitClient;
  const grid = box.querySelector('[data-call-grid]');
  const status = box.querySelector('[data-call-status]');
  // noise suppression of the browser, as «Шумоподавление»; on unless one turned it off
  let quiet = true;
  try { quiet = localStorage.getItem('go24.call.noise') !== '0'; } catch { /* no storage */ }
  const room = new LK.Room({ adaptiveStream: true, dynacast: true, audioCaptureDefaults: { noiseSuppression: quiet, echoCancellation: true, autoGainControl: true } });
  window.go24CallBg?.(room, box);

  const nameOf = p => p.isLocal ? (p.name || '') + ' (' + box.dataset.you + ')' : (p.name || p.identity);
  const isScreen = track => track.source === LK.Track?.Source?.ScreenShare || track.source === 'screen_share';
  // one tile per participant (video of the camera, or their name), and one more for a screen they share; sound plays
  // without a tile of its own
  const tile = (p, screen) => {
    const sid = p.identity + (screen ? ':screen' : '');
    let t = grid.querySelector(`[data-sid="${CSS.escape(sid)}"]`);
    if (!t) {
      t = document.createElement('div');
      t.className = 'calltile' + (screen ? ' screen' : '');
      t.dataset.sid = sid;
      t.dataset.who = p.identity;
      const name = document.createElement('span');
      name.className = 'callname';
      name.textContent = nameOf(p) + (screen ? ' · ' + box.dataset.screen : '');
      t.append(name);
      grid.append(t);
    }
    return t;
  };
  const attach = (track, p) => {
    const el = track.attach();
    if (track.kind === 'video') {
      const t = tile(p, isScreen(track));
      t.querySelectorAll('video').forEach(v => v.remove());
      t.prepend(el);
    } else if (!p.isLocal) {
      el.hidden = true;
      document.body.append(el);
    }
    people();
  };
  const detach = (track, p) => {
    track?.detach().forEach(el => el.remove());
    if (track && isScreen(track) && p) grid.querySelector(`[data-sid="${CSS.escape(p.identity + ':screen')}"]`)?.remove();
    people();
  };
  const count = () => { status.hidden = room.remoteParticipants.size > 0 || !status.textContent; };

  // the people of the call: the list with their microphones and cameras, the marks on their tiles; the layout —
  // «Докладчик» puts a shared screen (or whoever speaks) large, the others in a row
  const list = box.querySelector('[data-call-people]');
  const all = () => [room.localParticipant, ...room.remoteParticipants.values()].filter(Boolean);
  const hands = new Set();
  // the host of the call (as «ведущий», go24 marks them in the token): «ведущий» by their name; to the
  // host, by everyone else — «Выключить микрофон / камеру», «Остановить показ экрана», «Удалить из звонка»
  const isHost = p => { try { return !!JSON.parse(p.metadata || '{}').host; } catch { return false; } };
  const hostBtn = (text, icon, f) => {
    const b = document.createElement('button');
    b.type = 'button';
    b.className = 'ghost small';
    b.title = text;
    b.setAttribute('aria-label', text);
    b.textContent = icon;
    b.addEventListener('click', f);
    return b;
  };
  let layout = 'grid';
  let speaker = '';
  const arrange = () => {
    const screen = grid.querySelector('.calltile.screen');
    const big = screen || (speaker && grid.querySelector(`.calltile[data-sid="${CSS.escape(speaker)}"]`));
    const on = !!screen || layout === 'speaker';
    grid.classList.toggle('speaker', on);
    grid.querySelectorAll('.calltile').forEach(t => t.classList.toggle('main', on && t === (big || grid.querySelector('.calltile'))));
  };
  const people = () => {
    const ps = all();
    ps.forEach(p => {
      const t = grid.querySelector(`.calltile[data-sid="${CSS.escape(p.identity)}"]`);
      t?.classList.toggle('mic-off', !p.isMicrophoneEnabled);
      t?.classList.toggle('cam-off', !p.isCameraEnabled);
      t?.classList.toggle('hand', hands.has(p.identity));
    });
    if (list) {
      list.innerHTML = '';
      ps.forEach(p => {
        const li = document.createElement('li');
        li.textContent = nameOf(p);
        const marks = document.createElement('span');
        marks.className = 'muted';
        marks.textContent = (isHost(p) ? ' · ' + box.dataset.hostMark : '') + (hands.has(p.identity) ? ' ✋' : '') + (p.isMicrophoneEnabled ? ' 🎤' : ' 🔇') + (p.isCameraEnabled ? ' 📷' : '') + (p.isScreenShareEnabled ? ' 🖥' : '');
        li.append(marks);
        if (box.dataset.host && !p.isLocal) {
          const tools = document.createElement('span');
          tools.className = 'callhost';
          if (p.isMicrophoneEnabled) tools.append(hostBtn(box.dataset.muteMic, '🔇', () => moderate('mute', p, 'microphone')));
          if (p.isCameraEnabled) tools.append(hostBtn(box.dataset.muteCam, '🚫', () => moderate('mute', p, 'camera')));
          if (p.isScreenShareEnabled) tools.append(hostBtn(box.dataset.stopScreen, '⏹', () => moderate('mute', p, 'screen_share')));
          tools.append(hostBtn(box.dataset.remove, '✕', () => {
            if (confirm(box.dataset.removeConfirm.replace('%s', nameOf(p)))) moderate('remove', p);
          }));
          li.append(tools);
        }
        list.append(li);
      });
    }
    const n = box.querySelector('[data-call-count]');
    if (n) n.textContent = ps.length;
    if (connected) { // the host may have turned one's microphone or camera off
      label(box.querySelector('[data-call-mic]'), lp().isMicrophoneEnabled);
      label(box.querySelector('[data-call-cam]'), lp().isCameraEnabled);
      label(box.querySelector('[data-call-screen]'), lp().isScreenShareEnabled);
    }
    arrange();
  };
  let connected = false;

  // a call from a chat rings its people (ring.js on their pages): «Звоним…» until someone comes, their answers,
  // and the end of the ringing — when nobody answers in time, or when the caller leaves
  const ring = box.dataset.ring;
  let ringing = !!ring;
  const endRing = () => {
    if (!ringing) return;
    ringing = false;
    navigator.sendBeacon?.(`/call/ring/${encodeURIComponent(ring)}/end`) || fetch(`/call/ring/${encodeURIComponent(ring)}/end`, { method: 'POST', keepalive: true }).catch(() => {});
  };
  if (ring) {
    addEventListener('go24:call', e => {
      const d = e.detail || {};
      if (d.ring !== ring || room.remoteParticipants.size) return;
      if (d.kind === 'accepted') status.textContent = box.dataset.accepted.replace('%s', d.person || '');
      if (d.kind === 'declined') status.textContent = box.dataset.declined.replace('%s', d.person || '');
      status.hidden = false;
    });
    setTimeout(() => {
      if (!ringing) return;
      endRing();
      if (!room.remoteParticipants.size) { status.hidden = false; status.textContent = box.dataset.noAnswer; }
    }, 45000);
  }

  room
    .on(LK.RoomEvent.TrackSubscribed, (track, _pub, p) => attach(track, p))
    .on(LK.RoomEvent.TrackUnsubscribed, (track, _pub, p) => detach(track, p))
    .on(LK.RoomEvent.LocalTrackPublished, (pub) => attach(pub.track, room.localParticipant))
    .on(LK.RoomEvent.LocalTrackUnpublished, (pub) => detach(pub.track, room.localParticipant))
    .on(LK.RoomEvent.TrackMuted, () => people())
    .on(LK.RoomEvent.TrackUnmuted, () => people())
    .on(LK.RoomEvent.ParticipantConnected, (p) => { tile(p); count(); people(); if (hands.has(lp().identity)) send({ t: 'hand', up: true }); })
    .on(LK.RoomEvent.ParticipantDisconnected, (p) => { hands.delete(p.identity); grid.querySelectorAll(`[data-who="${CSS.escape(p.identity)}"]`).forEach(t => t.remove()); count(); people(); })
    .on(LK.RoomEvent.ActiveSpeakersChanged, (speakers) => {
      grid.querySelectorAll('.calltile').forEach(t => t.classList.toggle('speaking', speakers.some(s => s.identity === t.dataset.sid)));
      if (speakers[0] && !speakers[0].isLocal) { speaker = speakers[0].identity; arrange(); }
    })
    .on(LK.RoomEvent.Disconnected, (reason) => {
      status.hidden = false;
      status.textContent = reason === LK.DisconnectReason?.PARTICIPANT_REMOVED ? box.dataset.removed : box.dataset.failed;
    });

  function label(btn, on) { const s = btn?.querySelector('span'); if (!s) return; s.textContent = on ? s.dataset.textOn : s.dataset.textOff; btn.classList.toggle('off', !on); }
  const toggle = (sel, get, set) => box.querySelector(sel).addEventListener('click', async e => {
    const btn = e.currentTarget;
    try { await set(!get()); } catch (err) { console.warn(err); }
    label(btn, get());
  });
  const lp = () => room.localParticipant;
  toggle('[data-call-mic]', () => lp().isMicrophoneEnabled, on => lp().setMicrophoneEnabled(on));
  toggle('[data-call-cam]', () => lp().isCameraEnabled, on => lp().setCameraEnabled(on));
  toggle('[data-call-screen]', () => lp().isScreenShareEnabled, on => lp().setScreenShareEnabled(on));
  box.querySelector('[data-call-layout]')?.addEventListener('click', e => {
    layout = layout === 'grid' ? 'speaker' : 'grid';
    label(e.currentTarget, layout === 'grid');
    arrange();
  });
  box.querySelector('[data-call-full]')?.addEventListener('click', () => {
    if (document.fullscreenElement) document.exitFullscreen?.(); else box.requestFullscreen?.().catch(() => {});
  });
  // the devices: microphone, camera, speaker (where the browser lets one choose it)
  const devices = box.querySelector('[data-call-devices]');
  devices?.addEventListener('toggle', async () => {
    if (!devices.open) return;
    for (const sel of devices.querySelectorAll('select[data-kind]')) {
      let list = [];
      try { list = await LK.Room.getLocalDevices(sel.dataset.kind); } catch { /* none */ }
      const current = room.getActiveDevice?.(sel.dataset.kind);
      sel.innerHTML = '';
      list.forEach((d, i) => sel.append(new Option(d.label || `${sel.dataset.kind} ${i + 1}`, d.deviceId, false, d.deviceId === current)));
      sel.closest('label').hidden = !list.length;
    }
  });
  devices?.addEventListener('change', e => {
    const sel = e.target.closest('select[data-kind]');
    if (sel) room.switchActiveDevice(sel.dataset.kind, sel.value).catch(err => console.warn(err));
  });
  const noise = box.querySelector('[data-call-noise]');
  if (noise) noise.checked = quiet;
  noise?.addEventListener('change', () => {
    quiet = noise.checked;
    try { localStorage.setItem('go24.call.noise', quiet ? '1' : '0'); } catch { /* no storage */ }
    const mic = lp().getTrackPublication?.(LK.Track.Source.Microphone)?.track;
    mic?.restartTrack({ deviceId: room.getActiveDevice?.('audioinput'), noiseSuppression: quiet, echoCancellation: true, autoGainControl: true }).catch(err => console.warn(err));
  });
  // «Позвонить остальным»: the chat's people not in the call yet are rung
  box.querySelector('[data-call-ring-more]')?.addEventListener('click', async e => {
    const b = e.currentTarget;
    b.disabled = true;
    try {
      const r = await fetch('/call/ring-more', { method: 'POST', headers: { Accept: 'application/json' }, body: new URLSearchParams({ room: box.dataset.chat, video: box.dataset.video === 'false' ? '0' : '1' }) });
      status.hidden = false;
      status.textContent = r.ok ? box.dataset.ringingMore : box.dataset.failed;
    } catch { /* offline */ }
    setTimeout(() => { b.disabled = false; }, 5000);
  });
  //, over the data channel of the room (guests too): «Поднять руку» — ✋ on the tile and in the list,
  // the others are told; reactions float over the sender's tile for a moment; the chat of the call — the messages of
  // the people in it while they are in it, the unread ones counted on its button
  const send = (msg, to) => lp().publishData?.(new TextEncoder().encode(JSON.stringify(msg)), { reliable: true, topic: 'go24', ...(to ? { destinationIdentities: to } : {}) })?.catch?.(err => console.warn(err));
  const tell = text => { status.hidden = false; status.textContent = text; clearTimeout(tell.t); tell.t = setTimeout(() => { if (status.textContent === text) status.hidden = true; }, 4000); };
  const handBtn = box.querySelector('[data-call-hand]');
  const hand = (p, up) => {
    if (up === hands.has(p.identity)) return;
    if (up) hands.add(p.identity); else hands.delete(p.identity);
    if (p.isLocal) { handBtn.textContent = up ? handBtn.dataset.down : handBtn.dataset.up; handBtn.classList.toggle('primary', up); }
    else if (up) tell(box.dataset.handUp.replace('%s', nameOf(p)));
    people();
  };
  handBtn?.addEventListener('click', () => { const up = !hands.has(lp().identity); hand(lp(), up); send({ t: 'hand', up }); });
  const emojis = [...box.querySelectorAll('[data-call-emoji]')].map(b => b.dataset.callEmoji);
  const react = (p, e) => {
    if (!emojis.includes(e)) return;
    const t = grid.querySelector(`.calltile[data-sid="${CSS.escape(p.identity)}"]`);
    if (!t) return;
    const r = document.createElement('span');
    r.className = 'callreact';
    r.textContent = e;
    t.append(r);
    setTimeout(() => r.remove(), 3000);
  };
  box.querySelectorAll('[data-call-emoji]').forEach(b => b.addEventListener('click', () => {
    react(lp(), b.dataset.callEmoji);
    send({ t: 'react', e: b.dataset.callEmoji });
    b.closest('details')?.removeAttribute('open');
  }));
  const chat = box.querySelector('[data-call-chat]');
  const msgs = chat?.querySelector('[data-call-msgs]');
  const unread = chat?.querySelector('[data-call-unread]');
  const say = (p, text) => {
    if (!msgs || typeof text !== 'string' || !text.trim()) return;
    const li = document.createElement('li');
    const who = document.createElement('b');
    who.textContent = nameOf(p);
    const when = document.createElement('span');
    when.className = 'muted small';
    when.textContent = ' ' + new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    const body = document.createElement('div');
    body.textContent = text.slice(0, 2000);
    li.append(who, when, body);
    msgs.append(li);
    msgs.scrollTop = msgs.scrollHeight;
    if (!chat.open && !p.isLocal) { unread.textContent = +unread.textContent + 1; unread.hidden = false; }
  };
  chat?.addEventListener('toggle', () => {
    if (!chat.open) return;
    unread.textContent = '0';
    unread.hidden = true;
    msgs.scrollTop = msgs.scrollHeight;
    chat.querySelector('textarea, input[name="text"]')?.focus();
  });
  chat?.querySelector('form')?.addEventListener('submit', e => {
    e.preventDefault();
    const f = e.currentTarget.elements.text;
    const text = f.value.trim().slice(0, 2000);
    if (!text) return;
    say(lp(), text);
    send({ t: 'msg', text });
    f.value = '';
  });
  chat?.querySelector('textarea')?.addEventListener('keydown', e => {
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); e.currentTarget.form.requestSubmit(); }
  });
  room.on(LK.RoomEvent.DataReceived, (payload, p, _kind, topic) => {
    if (!p || (topic && topic !== 'go24')) return;
    let m;
    try { m = JSON.parse(new TextDecoder().decode(payload)); } catch { return; }
    if (m?.t === 'hand') hand(p, !!m.up);
    if (m?.t === 'muted' && isHost(p) && ['microphone', 'camera', 'screen_share'].includes(m.what)) {
      if (m.what === 'screen_share') lp().setScreenShareEnabled(false).catch(() => {});
      tell(box.dataset['muted' + m.what.replace(/(^|_)(\w)/g, (_, _u, c) => c.toUpperCase())]);
      people();
    }
    else if (m?.t === 'react') react(p, m.e);
    else if (m?.t === 'msg') say(p, m.text);
  });

  box.querySelector('[data-call-leave]').addEventListener('click', async () => {
    await room.disconnect();
    location.href = box.querySelector('a.button').href;
  });

  // guests, as the guest entry: «Пригласить гостя» makes a link for people without an account (for an hour,
  // a day or a week, with a password or not), the links in force with «Отозвать»; with the lobby on, a guest waits
  // until someone of the call lets them in — «… ждёт в прихожей: Впустить / Отклонить» above the call
  const guests = box.dataset.guests;
  if (guests !== undefined) {
    const lobby = box.querySelector('[data-call-lobby]');
    const links = box.querySelector('[data-call-links]');
    const api = (path, body) => fetch(path + '?' + guests, { method: body ? 'POST' : 'GET', headers: { Accept: 'application/json' }, body, cache: 'no-store' });
    const copy = async url => {
      try { await navigator.clipboard.writeText(url); status.hidden = false; status.textContent = box.dataset.copied; } catch { prompt('', url); }
    };
    const btn = (text, cls, on) => { const b = document.createElement('button'); b.type = 'button'; b.className = cls; b.textContent = text; b.addEventListener('click', on); return b; };
    let timer;
    const load = async () => {
      clearTimeout(timer);
      let d;
      try { const r = await api('/call/guests'); if (r.ok) d = await r.json(); } catch { /* offline */ }
      if (d) {
        lobby.innerHTML = '';
        d.waiting.forEach(g => {
          const row = document.createElement('div');
          row.className = 'row flash';
          const t = document.createElement('span');
          t.style.flex = '1';
          t.textContent = box.dataset.lobbyText.replace('%s', g.name);
          row.append(t,
            btn(lobby.dataset.admit || '✓', 'primary', async () => { await api(`/call/lobby/${g.id}/admit`, new URLSearchParams()); load(); }),
            btn(lobby.dataset.deny || '✕', 'ghost', async () => { await api(`/call/lobby/${g.id}/deny`, new URLSearchParams()); load(); }));
          lobby.append(row);
        });
        lobby.hidden = !d.waiting.length;
        links.innerHTML = '';
        d.links.forEach(l => {
          const li = document.createElement('li');
          li.className = 'row';
          const t = document.createElement('span');
          t.className = 'small';
          t.style.flex = '1';
          t.textContent = (l.password ? '🔒 ' : '') + box.dataset.until.replace('%s', new Date(l.expires).toLocaleString([], { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' }));
          li.append(t, btn(links.dataset.copy || '⧉', 'ghost', () => copy(l.url)),
            btn(links.dataset.revoke || '✕', 'ghost', async () => { await api(`/call/guests/${l.id}/revoke`, new URLSearchParams()); load(); }));
          links.append(li);
        });
        links.hidden = !d.links.length;
      }
      timer = setTimeout(load, d && d.links.length ? 4000 : 20000); // a lobby needs the people of the call soon
    };
    box.querySelector('[data-call-invite-form]')?.addEventListener('submit', async e => {
      e.preventDefault();
      const f = e.currentTarget;
      const body = new URLSearchParams(new FormData(f));
      try {
        const r = await api('/call/guests', body);
        if (r.ok) { const l = await r.json(); f.elements.password.value = ''; await copy(l.url); }
      } catch { /* offline */ }
      load();
    });
    load();
  }

  // the host's tools: LiveKit turns the tracks off (go24 asks it), the host's page tells the person why
  async function moderate(action, p, what) {
    try {
      const r = await fetch('/call/moderate?' + box.dataset.host, { method: 'POST', headers: { Accept: 'application/json' },
        body: new URLSearchParams({ action, who: p ? p.identity : '', what: what || '' }) });
      const d = await r.json().catch(() => ({}));
      if (!r.ok) { tell(d.error || box.dataset.failed); return; }
      if (action === 'mute') send({ t: 'muted', what }, p ? [p.identity] : undefined);
      if (!p) tell(box.dataset.mutedAll);
    } catch { tell(box.dataset.failed); }
  }
  box.querySelector('[data-call-mute-all]')?.addEventListener('click', () => moderate('mute', null, 'microphone'));

  // recording: «Начать запись» when the administrators let calls be recorded; everyone in the call
  // sees «Идёт запись» while LiveKit Egress records the room (LiveKit tells the room itself)
  const recBtn = box.querySelector('[data-call-record]');
  const recMark = box.querySelector('[data-call-rec]');
  let recording = false;
  const recShow = on => {
    recording = on;
    if (!recBtn) return;
    recMark.hidden = !on;
    recBtn.textContent = on ? recBtn.dataset.stop : recBtn.dataset.start;
    recBtn.classList.toggle('danger', on);
  };
  if (recBtn) {
    const recAPI = body => fetch('/call/record?' + box.dataset.record, { method: body ? 'POST' : 'GET', headers: { Accept: 'application/json' }, body, cache: 'no-store' });
    room.on(LK.RoomEvent.RecordingStatusChanged, on => recShow(on));
    recBtn.addEventListener('click', async () => {
      recBtn.disabled = true;
      try {
        const r = await recAPI(new URLSearchParams({ action: recording ? 'stop' : 'start' }));
        const d = await r.json().catch(() => ({}));
        if (r.ok) {
          if (recording && !d.on) { status.hidden = false; status.textContent = box.dataset.saving; }
          recShow(d.on);
        } else if (d.error) { status.hidden = false; status.textContent = d.error; }
      } catch { /* offline */ }
      recBtn.disabled = false;
    });
    recAPI().then(r => r.ok ? r.json() : {}).then(d => {
      recBtn.hidden = !d.enabled;
      if (d.on || room.isRecording) recShow(true);
    }).catch(() => {});
  }

  (async () => {
    try {
      await room.connect(box.dataset.lk, box.dataset.token);
      tile(room.localParticipant);
      room.remoteParticipants.forEach(p => tile(p));
      count();
      people();
      status.textContent = ring && !room.remoteParticipants.size ? box.dataset.ringing : '';
      status.hidden = !status.textContent; // connected: no empty bar
      if (box.dataset.video === 'false') await room.localParticipant.setMicrophoneEnabled(true).catch(() => {}); // a call with sound only
      else try { await room.localParticipant.enableCameraAndMicrophone(); } catch (err) {
        await room.localParticipant.setMicrophoneEnabled(true).catch(() => {}); // no camera: sound only
      }
      connected = true;
      if (room.isRecording) recShow(true);
      people();
    } catch (err) {
      console.warn(err);
      status.textContent = box.dataset.failed;
    }
  })();
  addEventListener('pagehide', () => { endRing(); room.disconnect(); });
})();
