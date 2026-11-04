// The voice in the mail, as in Outlook: «Диктовка» in a letter being written ([data-dictate] of mail.html «mcompose»)
// and «Прочитать вслух» of an open letter ([data-speak] of «mmore»). Dictation records the microphone a phrase at
// a time — a pause after speech or half a minute closes it — and sends each phrase to go24's Whisper (POST
// /mail/dictate); the texts come in the order spoken, at the place the cursor was. Reading aloud speaks the subject,
// the sender and the letter paragraph by paragraph with the browser's voices; the paragraph read is highlighted.
(() => {
  const headers = { Accept: 'application/json', 'X-Go24-Form': '1' };

  // ---------- «Диктовка» ----------
  const LOUD = 0.02;        // the level (RMS) of speech; below it — silence
  const PAUSE = 900;        // ms of silence that end a phrase
  const LONGEST = 30000;    // ms: a phrase is cut anyway
  const label = (form, k) => form.querySelector(`template[data-dictate-labels]`)?.content.querySelector(`[data-l="${k}"]`)?.textContent || k;
  let dict = null; // the dictation going on: one at a time

  // where the text goes: the cursor if it is in the editor, otherwise the end of the text
  const anchor = ed => {
    const sel = document.getSelection();
    if (sel?.rangeCount && ed.contains(sel.getRangeAt(0).startContainer)) {
      const r = sel.getRangeAt(0).cloneRange();
      r.collapse(false);
      return r;
    }
    const r = document.createRange();
    r.selectNodeContents(ed);
    r.collapse(false);
    return r;
  };

  // put puts a phrase at the range and moves the range after it; a space before it, if the text runs on. The cursor
  // waiting there moves on with it; one put elsewhere stays.
  const put = (ed, at, text) => {
    if (!ed.contains(at.startContainer)) at = anchor(ed);
    const sel = document.getSelection(), cur = sel?.rangeCount ? sel.getRangeAt(0) : null;
    const follow = cur?.collapsed && cur.startContainer === at.startContainer && cur.startOffset === at.startOffset;
    const n = at.startContainer, before = n.nodeType === 3 ? n.data.slice(0, at.startOffset) : n.childNodes[at.startOffset - 1]?.textContent || '';
    const t = document.createTextNode((before && !/\s$/.test(before) ? ' ' : '') + text);
    at.insertNode(t);
    at.setStartAfter(t);
    at.collapse(true);
    if (follow) {
      sel.removeAllRanges();
      sel.addRange(at.cloneRange());
    }
    ed.dispatchEvent(new Event('input', { bubbles: true })); // the draft is kept (compose.js)
    return at;
  };

  const show = (d, state) => {
    d.btn.classList.toggle('on', dict === d); // the microphone is on; its last phrases may still be recognized
    d.btn.setAttribute('aria-pressed', String(dict === d));
    if (d.state) {
      d.state.hidden = !state;
      d.state.textContent = state ? label(d.form, state) : '';
    }
  };

  const fail = (d, msg) => {
    if (d.state) {
      d.state.hidden = false;
      d.state.textContent = msg;
      d.state.classList.add('err');
    }
  };

  async function start(btn) {
    const form = btn.closest('form'), ed = form?.querySelector('[data-editor]');
    if (!ed) return;
    const d = { form, ed, btn, state: form.querySelector('[data-dictate-state]'), at: anchor(ed), queue: Promise.resolve(), lang: form.querySelector('[data-dictate-lang]')?.value || '' };
    d.state?.classList.remove('err');
    if (!navigator.mediaDevices?.getUserMedia || !window.MediaRecorder) return fail(d, label(form, 'no_mic'));
    try {
      d.stream = await navigator.mediaDevices.getUserMedia({ audio: { echoCancellation: true, noiseSuppression: true } });
    } catch {
      return fail(d, label(form, 'no_mic'));
    }
    dict = d;
    show(d, 'listening');
    const Ctx = window.AudioContext || window.webkitAudioContext;
    if (Ctx) {
      d.ctx = new Ctx();
      d.meter = d.ctx.createAnalyser();
      d.meter.fftSize = 1024;
      d.ctx.createMediaStreamSource(d.stream).connect(d.meter);
    }
    record(d);
    const buf = new Float32Array(1024);
    d.tick = setInterval(() => {
      const now = Date.now();
      if (d.meter) {
        d.meter.getFloatTimeDomainData(buf);
        let sum = 0;
        for (const v of buf) sum += v * v;
        if (Math.sqrt(sum / buf.length) > LOUD) { d.spoke = true; d.heard = now; }
      } else d.spoke = true; // no meter: phrases by time only
      if ((d.spoke && d.heard && now - d.heard > PAUSE) || now - d.began > LONGEST) cut(d);
    }, 100);
    ed.focus(); // the cursor back where the text goes
    const sel = document.getSelection();
    sel?.removeAllRanges();
    sel?.addRange(d.at.cloneRange());
  }

  // record starts a phrase; each phrase is a whole recording, so Whisper takes each alone
  function record(d) {
    const rec = new MediaRecorder(d.stream), parts = [];
    d.rec = rec;
    d.began = Date.now();
    d.spoke = false;
    d.heard = 0;
    rec.ondataavailable = e => e.data.size && parts.push(e.data);
    rec.onstop = () => {
      if (!rec.spoke || !parts.length) {
        if (dict !== d && !d.pending) show(d, null);
        return;
      }
      const blob = new Blob(parts, { type: rec.mimeType || 'audio/webm' });
      d.pending = (d.pending || 0) + 1;
      show(d, 'recognizing');
      const sent = send(d, blob);
      d.queue = d.queue.then(() => sent).then(text => {
        if (text) d.at = put(d.ed, d.at, text);
      }).catch(err => fail(d, err.message)).finally(() => {
        if (--d.pending === 0 && !d.state?.classList.contains('err')) show(d, dict === d ? 'listening' : null);
      });
    };
    rec.start();
  }

  const send = async (d, blob) => {
    const body = new FormData();
    body.append('audio', blob, 'dictation.' + (/ogg/.test(blob.type) ? 'ogg' : /mp4/.test(blob.type) ? 'm4a' : 'webm'));
    if (d.lang) body.append('lang', d.lang);
    const res = await fetch('/mail/dictate', { method: 'POST', body, headers });
    const data = await res.json().catch(() => null);
    if (!res.ok) throw new Error(data?.error || res.statusText);
    return (data?.text || '').trim();
  };

  // cut closes the phrase and starts the next one, if anything was said
  function cut(d) {
    if (!d.spoke) { d.began = Date.now(); return; }
    const rec = d.rec;
    rec.spoke = true;
    rec.stop();
    if (dict === d) record(d);
  }

  function stop() {
    const d = dict;
    if (!d) return;
    dict = null;
    clearInterval(d.tick);
    if (d.rec?.state === 'recording') {
      d.rec.spoke = d.spoke;
      d.rec.stop();
    }
    d.stream.getTracks().forEach(t => t.stop());
    d.ctx?.close();
    show(d, d.spoke || d.pending ? 'recognizing' : null);
  }

  document.addEventListener('click', e => {
    const b = e.target.closest('[data-dictate]');
    if (!b) return;
    e.preventDefault();
    if (dict) {
      const same = dict.btn === b;
      stop();
      if (same) return;
    }
    start(b);
  });
  document.addEventListener('keydown', e => { if (e.key === 'Escape' && dict) { stop(); e.stopPropagation(); } }, true);
  // a letter sent or closed: its microphone is let go
  setInterval(() => { if (dict && !dict.btn.isConnected) stop(); }, 1000);
  document.addEventListener('change', e => {
    if (!e.target.matches('[data-dictate-lang]')) return;
    try { localStorage.setItem('go24.dictate.lang', e.target.value); } catch {}
    if (dict) dict.lang = e.target.value;
  });
  // the language chosen before is kept in the browser
  const langs = () => document.querySelectorAll('[data-dictate-lang]:not([data-set])').forEach(s => {
    s.dataset.set = '1';
    let l = '';
    try { l = localStorage.getItem('go24.dictate.lang') || ''; } catch {}
    if (l && [...s.options].some(o => o.value === l)) s.value = l;
  });
  langs();
  new MutationObserver(langs).observe(document.body, { childList: true, subtree: true });

  // ---------- «Прочитать вслух» ----------
  const BLOCK = new Set('P DIV LI TR TD TH H1 H2 H3 H4 H5 H6 BLOCKQUOTE PRE TABLE UL OL DL DT DD HR SECTION ARTICLE HEADER FOOTER'.split(' '));
  const SKIP = new Set(['SCRIPT', 'STYLE', 'TEMPLATE', 'NOSCRIPT', 'HEAD', 'TITLE']);

  // paragraphs gives the text of an element by paragraph — a line break or a block ends one — each with its range
  const paragraphs = root => {
    const out = [], doc = root.ownerDocument;
    let first = null, last = null, text = '';
    const end = () => {
      if (first && text.trim()) {
        const r = doc.createRange();
        r.setStart(first, 0);
        r.setEnd(last, last.data.length);
        out.push({ text: text.replace(/\s+/g, ' ').trim(), range: r });
      }
      first = last = null;
      text = '';
    };
    const walk = el => {
      for (const n of el.childNodes) {
        if (n.nodeType === 3) {
          if (!n.data.trim() && !first) continue;
          first ||= n;
          last = n;
          text += n.data;
        } else if (n.nodeType === 1 && !SKIP.has(n.tagName)) {
          if (n.tagName === 'BR' || BLOCK.has(n.tagName)) end();
          walk(n);
          if (BLOCK.has(n.tagName)) end();
        }
      }
    };
    walk(root);
    end();
    return out;
  };

  // sentences: a long paragraph is spoken in pieces (browsers cut long utterances)
  const sentences = t => t.match(/[^.!?…]+[.!?…]*\s*/g)?.reduce((a, s) => {
    if (a.length && (a[a.length - 1] + s).length < 200) a[a.length - 1] += s;
    else a.push(s);
    return a;
  }, []) || [t];

  const speakLabel = (art, k) => art.querySelector('template[data-speak-labels]')?.content.querySelector(`[data-l="${k}"]`)?.textContent || k;
  let read = null; // the letter being read

  const mark = (p, on) => {
    const win = p?.range.startContainer.ownerDocument.defaultView;
    if (!win?.CSS?.highlights || !win.Highlight) return;
    if (!on) { win.CSS.highlights.delete('go24-speak'); return; }
    const doc = win.document;
    if (!doc.getElementById('go24-speak-style')) {
      const st = doc.createElement('style');
      st.id = 'go24-speak-style';
      st.textContent = '::highlight(go24-speak) { background: #fff1a8; color: #000; }';
      (doc.head || doc.documentElement).append(st);
    }
    win.CSS.highlights.set('go24-speak', new win.Highlight(p.range));
    const el = p.range.startContainer.parentElement;
    if (el && doc === document) el.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
  };

  function speakFrom(i) {
    const r = read;
    if (!r) return;
    speechSynthesis.cancel();
    mark(r.paras[r.i], false);
    if (i < 0) i = 0;
    if (i >= r.paras.length) return quit();
    r.i = i;
    r.paused = false;
    bar(r);
    const p = r.paras[i];
    mark(p, true);
    const parts = sentences(p.text);
    parts.forEach((s, k) => {
      const u = new SpeechSynthesisUtterance(s);
      u.lang = r.lang;
      u.rate = r.rate;
      if (r.voice) u.voice = r.voice;
      if (k === parts.length - 1) u.onend = () => { if (read === r && r.i === i && !r.paused) speakFrom(i + 1); };
      speechSynthesis.speak(u);
    });
  }

  function quit() {
    const r = read;
    if (!r) return;
    read = null;
    speechSynthesis.cancel();
    mark(r.paras[r.i], false);
    r.bar.remove();
  }

  const bar = r => {
    const pause = r.bar.querySelector('[data-sp="pause"]');
    pause.innerHTML = '';
    pause.append(r.icons[r.paused ? 'play' : 'pause'].cloneNode(true));
    pause.title = speakLabel(r.art, r.paused ? 'resume' : 'pause');
    r.bar.querySelector('[data-sp-at]').textContent = `${r.i + 1} / ${r.paras.length}`;
  };

  function speak(art) {
    quit();
    const msg = art.querySelector(`[data-msg="${art.dataset.letter}"]`) || art.querySelector('[data-msg]');
    const frame = msg?.querySelector('iframe.mailhtml');
    const body = frame?.contentDocument?.body || msg?.querySelector('.mailbody');
    const head = [art.querySelector('h2'), msg?.querySelector('.mmsg-who b')].filter(Boolean)
      .map(el => ({ text: el.textContent.replace(/\s+/g, ' ').trim(), range: (() => { const r = document.createRange(); r.selectNodeContents(el); return r; })() }));
    const paras = [...head, ...(body ? paragraphs(body) : [])];
    if (!paras.length) return;
    const tpl = art.querySelector('template[data-speak-labels]');
    const r = { art, paras, i: 0, rate: 1, lang: body?.closest('[lang]')?.getAttribute('lang') || document.documentElement.lang || '', icons: {} };
    for (const k of ['play', 'pause']) r.icons[k] = tpl?.content.querySelector(`[data-i="${k}"]`)?.firstElementChild || document.createTextNode(k === 'play' ? '▶' : '⏸');
    r.bar = document.createElement('div');
    r.bar.className = 'row mc-banner mspeak';
    r.bar.setAttribute('role', 'toolbar');
    r.bar.setAttribute('aria-label', speakLabel(art, 'title'));
    r.bar.innerHTML = tpl?.content.querySelector('[data-bar]')?.innerHTML || '';
    const voices = speechSynthesis.getVoices().filter(v => !r.lang || v.lang.toLowerCase().startsWith(r.lang.slice(0, 2).toLowerCase()));
    const vs = r.bar.querySelector('[data-sp-voice]');
    if (vs) {
      vs.hidden = voices.length < 2;
      voices.forEach((v, k) => vs.add(new Option(v.name, String(k), v.default, v.default)));
      vs.addEventListener('change', () => { r.voice = voices[+vs.value]; speakFrom(r.i); });
    }
    r.bar.querySelector('[data-sp-rate]')?.addEventListener('change', e => { r.rate = +e.target.value || 1; speakFrom(r.i); });
    r.bar.addEventListener('click', e => {
      const b = e.target.closest('[data-sp]');
      if (!b) return;
      const what = b.dataset.sp;
      if (what === 'stop') return quit();
      if (what === 'prev') return speakFrom(r.i - 1);
      if (what === 'next') return speakFrom(r.i + 1);
      if (r.paused) { r.paused = false; speechSynthesis.resume(); if (!speechSynthesis.speaking) speakFrom(r.i); } else { r.paused = true; speechSynthesis.pause(); }
      bar(r);
    });
    (art.querySelector('.mread-actions') || art.firstElementChild).after(r.bar);
    read = r;
    speakFrom(0);
  }

  document.documentElement.classList.toggle('go24-tts', 'speechSynthesis' in window); // no voice — no «Прочитать вслух»
  document.addEventListener('click', e => {
    const b = e.target.closest('[data-speak]');
    if (!b) return;
    e.preventDefault();
    b.closest('details')?.removeAttribute('open');
    const art = b.closest('[data-letter]');
    if (art) speak(art);
  });
  // another letter opened: the reading stops
  setInterval(() => { if (read && !read.bar.isConnected) quit(); }, 1000);
  addEventListener('pagehide', () => { if (read) speechSynthesis.cancel(); });

})();
