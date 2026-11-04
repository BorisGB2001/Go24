// «Иммерсивное средство чтения» of a letter, as Outlook's Immersive Reader ([data-immersive] of mail.html «mmore»,
// the dialog — template «mimmersive»). The letter opens over the whole page as calm, large text: its subject, sender
// and paragraphs (headings, lists, quotes, pictures kept; the rest of the formatting dropped). «Параметры текста» —
// size, wider spacing, font, column width, background; «Параметры чтения» — line focus on one, three or five lines
// (the arrows move the text a line); reading aloud with the browser's voices highlights the word spoken, a click on
// a word reads from it. The preferences stay in the browser; Esc closes.
(() => {
  const KEY = 'go24-immersive';
  const DEFAULTS = { size: 24, spacing: false, font: 'sans', width: 'medium', theme: 'auto', focus: 0, rate: 1 };
  const SKIP = new Set(['SCRIPT', 'STYLE', 'HEAD', 'TITLE', 'NOSCRIPT', 'TEMPLATE', 'SVG']);
  const BLOCK = new Set(['P', 'DIV', 'LI', 'TR', 'TD', 'TH', 'H1', 'H2', 'H3', 'H4', 'H5', 'H6', 'BLOCKQUOTE', 'PRE', 'UL', 'OL',
    'TABLE', 'TBODY', 'SECTION', 'ARTICLE', 'HEADER', 'FOOTER', 'HR', 'DL', 'DT', 'DD', 'FIGURE', 'CENTER', 'ADDRESS']);
  const KIND = { H1: 'h', H2: 'h', H3: 'h', H4: 'h', H5: 'h', H6: 'h', LI: 'li', BLOCKQUOTE: 'q' };

  const prefs = () => {
    try { return { ...DEFAULTS, ...JSON.parse(localStorage.getItem(KEY) || '{}') }; } catch { return { ...DEFAULTS }; }
  };
  const keep = p => { try { localStorage.setItem(KEY, JSON.stringify(p)); } catch { /* a private window */ } };

  // blocks are the letter's text as paragraphs: {kind: p|h|li|q, text} and pictures {kind: img, src}
  const blocks = root => {
    const out = [];
    let text = '', kind = 'p';
    const end = () => {
      const t = text.replace(/\s+/g, ' ').trim();
      if (t) out.push({ kind, text: t });
      text = '';
    };
    const walk = (el, k) => {
      for (const n of el.childNodes) {
        if (n.nodeType === 3) {
          if (!text) kind = k;
          text += n.data;
        } else if (n.nodeType === 1 && !SKIP.has(n.tagName.toUpperCase())) {
          const tag = n.tagName.toUpperCase();
          if (tag === 'IMG') {
            const src = n.getAttribute('src') || '';
            if (/^(\/|data:image\/|https?:)/.test(src) && !(+n.getAttribute('width') > 0 && +n.getAttribute('width') <= 2 && +n.getAttribute('height') <= 2)) { // not a tracking pixel
              end();
              out.push({ kind: 'img', src: n.src || src, alt: n.getAttribute('alt') || '' });
            }
            continue;
          }
          if (tag === 'BR' || BLOCK.has(tag)) end();
          walk(n, KIND[tag] || (BLOCK.has(tag) && k === 'h' ? 'p' : k));
          if (BLOCK.has(tag)) end();
        }
      }
    };
    walk(root, 'p');
    end();
    return out;
  };

  // words: a paragraph's text with each word in a span that knows where it starts in the text
  const words = (el, text) => {
    let at = 0;
    for (const part of text.split(/(\s+)/)) {
      if (!part) continue;
      if (/^\s+$/.test(part)) el.append(part);
      else {
        const w = document.createElement('span');
        w.className = 'mimm-w';
        w.dataset.o = String(at);
        w.textContent = part;
        el.append(w);
      }
      at += part.length;
    }
  };

  // sentences: a long paragraph is spoken in pieces (browsers cut long utterances), each knowing where it starts
  const sentences = t => {
    const out = [];
    let at = 0;
    for (const s of t.match(/[^.!?…]+[.!?…]*\s*/g) || [t]) {
      if (out.length && (out[out.length - 1].text + s).length < 200) out[out.length - 1].text += s;
      else out.push({ text: s, at });
      at += s.length;
    }
    return out;
  };

  let cur = null; // the reader open: one at a time

  function open(art) {
    close();
    const tpl = document.querySelector('template[data-immersive-ui]');
    if (!tpl) return;
    const msg = art.querySelector(`[data-msg="${art.dataset.letter}"]`) || art.querySelector('[data-msg]');
    const body = msg?.querySelector('.mtr-body') || msg?.querySelector('iframe.mailhtml')?.contentDocument?.body || msg?.querySelector('.mailbody');
    const box = tpl.content.firstElementChild.cloneNode(true);
    const text = box.querySelector('[data-imm-text]');
    const r = { art, box, text, back: document.activeElement, paras: [], i: 0, playing: false, run: 0, p: prefs(), voices: [] };
    r.lang = body?.closest('[lang]')?.getAttribute('lang') || body?.ownerDocument.documentElement.lang || document.documentElement.lang || '';
    const para = (tag, t, kind) => {
      const el = document.createElement(tag);
      el.className = 'mimm-p';
      if (kind) el.dataset.kind = kind;
      el.dataset.text = t;
      words(el, t);
      r.paras.push(el);
      return el;
    };
    const subject = art.querySelector('h2')?.textContent.replace(/\s+/g, ' ').trim();
    if (subject) text.append(para('h1', subject));
    const who = msg?.querySelector('.mmsg-who b')?.textContent.replace(/\s+/g, ' ').trim();
    if (who) text.append(para('p', who, 'who'));
    let list = null;
    for (const b of body ? blocks(body) : []) {
      if (b.kind === 'img') {
        list = null;
        const img = document.createElement('img');
        img.src = b.src;
        img.alt = b.alt;
        img.loading = 'lazy';
        text.append(img);
        continue;
      }
      if (b.kind === 'li') {
        if (!list) text.append(list = document.createElement('ul'));
        list.append(para('li', b.text));
        continue;
      }
      list = null;
      text.append(para(b.kind === 'h' ? 'h3' : b.kind === 'q' ? 'blockquote' : 'p', b.text));
    }
    for (const el of box.querySelectorAll('[data-imm-set]')) {
      const k = el.dataset.immSet;
      if (el.type === 'checkbox') el.checked = !!r.p[k];
      else if (el.type === 'radio') el.checked = el.value === String(r.p[k]);
      else el.value = String(r.p[k]);
    }
    const rate = box.querySelector('[data-imm-rate]');
    rate.value = String(r.p.rate);
    if (!rate.value) rate.value = '1';
    const tts = 'speechSynthesis' in window;
    box.querySelector('.mimm-voice').hidden = !tts;
    if (tts) {
      r.voices = speechSynthesis.getVoices().filter(v => !r.lang || v.lang.toLowerCase().startsWith(r.lang.slice(0, 2).toLowerCase()));
      const vs = box.querySelector('[data-imm-voice]');
      vs.hidden = r.voices.length < 2;
      r.voices.forEach((v, k) => vs.add(new Option(v.name, String(k), v.default, v.default)));
    }
    cur = r;
    document.body.append(box);
    apply(r);
    document.documentElement.classList.add('mimm-open');
    box.querySelector('[data-imm-scroll]').focus();
    box.addEventListener('input', e => change(r, e.target));
    box.addEventListener('change', e => change(r, e.target));
    box.addEventListener('click', e => click(r, e));
  }

  function change(r, el) {
    if (el.matches('[data-imm-rate]')) {
      r.p.rate = +el.value || 1;
      keep(r.p);
      if (r.playing) speakFrom(r, r.i, r.at || 0);
      return;
    }
    if (el.matches('[data-imm-voice]')) {
      r.voice = r.voices[+el.value];
      if (r.playing) speakFrom(r, r.i, r.at || 0);
      return;
    }
    const k = el.dataset.immSet;
    if (!k) return;
    if (el.type === 'radio' && !el.checked) return;
    r.p[k] = el.type === 'checkbox' ? el.checked : (k === 'size' || k === 'focus') ? +el.value : el.value;
    keep(r.p);
    apply(r);
  }

  function apply(r) {
    const { box, p } = r;
    box.style.setProperty('--imm-size', p.size + 'px');
    box.dataset.font = p.font;
    box.dataset.width = p.width;
    box.dataset.theme = p.theme;
    box.classList.toggle('mimm-wide', !!p.spacing);
    box.dataset.focus = String(p.focus || 0);
    for (const b of box.querySelectorAll('[data-imm-theme]')) b.setAttribute('aria-pressed', String(b.dataset.immTheme === p.theme));
    masks(r);
  }

  // line focus: the window of N lines in the middle of the screen, the rest dimmed by two masks
  function masks(r) {
    const n = +r.p.focus || 0, top = r.box.querySelector('[data-imm-mask="top"]'), bottom = r.box.querySelector('[data-imm-mask="bottom"]');
    const scroll = r.box.querySelector('[data-imm-scroll]');
    top.hidden = bottom.hidden = !n;
    scroll.style.paddingTop = scroll.style.paddingBottom = '';
    if (!n) return;
    const h = lineHeight(r) * n, box = scroll.getBoundingClientRect(), mid = box.top + (box.height || innerHeight) / 2;
    const above = Math.max(0, mid - h / 2 - box.top);
    top.style.top = box.top + 'px';
    top.style.height = above + 'px';
    scroll.style.paddingTop = above + 'px'; // the first line starts in the window, the last can reach it
    scroll.style.paddingBottom = Math.max(0, box.height - above - lineHeight(r)) + 'px';
    bottom.style.top = (mid + h / 2) + 'px';
    bottom.style.bottom = '0';
  }

  const lineHeight = r => {
    const cs = getComputedStyle(r.text), lh = parseFloat(cs.lineHeight);
    return lh > 0 ? lh : (parseFloat(cs.fontSize) || r.p.size) * (r.p.spacing ? 2.2 : 1.7);
  };

  function click(r, e) {
    const b = e.target.closest('[data-imm],[data-imm-theme]');
    if (b?.dataset.immTheme) {
      r.p.theme = b.dataset.immTheme;
      keep(r.p);
      apply(r);
      return;
    }
    if (b?.dataset.imm === 'close') return close();
    if (b?.dataset.imm === 'play') return r.playing ? pause(r) : speakFrom(r, r.i, r.at || 0);
    const w = e.target.closest('.mimm-w');
    if (w && 'speechSynthesis' in window) {
      const p = w.closest('.mimm-p');
      speakFrom(r, r.paras.indexOf(p), +w.dataset.o);
    }
  }

  const playButton = r => {
    const b = r.box.querySelector('[data-imm="play"]'), k = r.playing ? 'pause' : 'play';
    b.innerHTML = r.box.querySelector(`[data-i="${k}"]`)?.innerHTML || (r.playing ? '⏸' : '▶');
    b.title = r.box.querySelector(`[data-l="${k}"]`)?.textContent || k;
    b.setAttribute('aria-label', b.title);
    b.setAttribute('aria-pressed', String(r.playing));
  };

  const unmark = r => {
    for (const el of r.text.querySelectorAll('.mimm-on, .mimm-reading')) el.classList.remove('mimm-on', 'mimm-reading');
  };

  // mark highlights the word that starts at the paragraph's offset (or the paragraph, if the voice tells no words)
  function mark(r, p, offset) {
    for (const el of r.text.querySelectorAll('.mimm-on')) el.classList.remove('mimm-on');
    let w = null;
    for (const x of p.querySelectorAll('.mimm-w')) {
      if (+x.dataset.o > offset) break;
      w = x;
    }
    if (!w) return;
    w.classList.add('mimm-on');
    r.at = +w.dataset.o;
    w.scrollIntoView?.({ block: +r.p.focus ? 'center' : 'nearest', behavior: 'smooth' });
  }

  function speakFrom(r, i, offset) {
    if (cur !== r) return;
    const run = ++r.run; // the utterances of a reading stopped before do nothing
    speechSynthesis.cancel();
    unmark(r);
    if (i < 0) i = 0;
    if (i >= r.paras.length) {
      r.playing = false;
      r.i = 0;
      r.at = 0;
      return playButton(r);
    }
    r.i = i;
    r.at = offset;
    r.playing = true;
    playButton(r);
    const p = r.paras[i];
    p.classList.add('mimm-reading');
    mark(r, p, offset);
    const parts = sentences(p.dataset.text.slice(offset));
    parts.forEach((s, k) => {
      const u = new SpeechSynthesisUtterance(s.text);
      u.lang = r.lang;
      u.rate = r.p.rate;
      if (r.voice) u.voice = r.voice;
      u.onboundary = e => { if (r.run === run && (!e.name || e.name === 'word')) mark(r, p, offset + s.at + e.charIndex); };
      if (k === parts.length - 1) u.onend = () => { if (r.run === run && r.playing) speakFrom(r, i + 1, 0); };
      speechSynthesis.speak(u);
    });
  }

  function pause(r) {
    r.run++;
    r.playing = false;
    speechSynthesis.cancel();
    playButton(r);
  }

  function close() {
    const r = cur;
    if (!r) return;
    cur = null;
    r.run++;
    if (r.playing) speechSynthesis.cancel();
    r.box.remove();
    document.documentElement.classList.remove('mimm-open');
    r.back?.focus?.();
  }

  document.addEventListener('click', e => {
    const b = e.target.closest('[data-immersive]');
    if (!b) return;
    e.preventDefault();
    b.closest('details')?.removeAttribute('open');
    const art = b.closest('[data-letter]');
    if (art) open(art);
  });
  document.addEventListener('keydown', e => {
    const r = cur;
    if (!r) return;
    if (e.key === 'Escape') {
      const menu = r.box.querySelector('details[open]');
      if (menu) menu.removeAttribute('open');
      else close();
      e.preventDefault();
      return;
    }
    if (e.target.closest?.('input, select, textarea, summary, button')) return;
    if (e.key === ' ' && 'speechSynthesis' in window) {
      e.preventDefault();
      if (r.playing) pause(r);
      else speakFrom(r, r.i, r.at || 0);
    } else if ((e.key === 'ArrowDown' || e.key === 'ArrowUp') && +r.p.focus) {
      e.preventDefault();
      r.box.querySelector('[data-imm-scroll]').scrollBy?.({ top: (e.key === 'ArrowDown' ? 1 : -1) * lineHeight(r) });
    }
  });
  addEventListener('resize', () => { if (cur) masks(cur); });
  addEventListener('pagehide', () => { if (cur?.playing) speechSynthesis.cancel(); });
})();
