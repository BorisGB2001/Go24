// The background of one's video in a call: none, a light or a strong blur, a ready background or
// one's own picture; the person is cut out of the camera picture by MediaPipe selfie segmentation right in the browser
// (static/js/vendor/mediapipe, fetched when the image is built). The choice is remembered in the browser and set on
// the camera of the next calls too. call.js calls go24CallBg(room, box) once its room exists.
(() => {
  const KEY = 'go24.call.bg', OWN = 'go24.call.bg.own';
  const get = k => { try { return localStorage.getItem(k); } catch { return null; } };
  const put = (k, v) => { try { if (v == null) localStorage.removeItem(k); else localStorage.setItem(k, v); return true; } catch { return false; } };
  // the ready backgrounds: soft gradients drawn on a canvas, so nothing is downloaded for them
  const presets = [
    ['#a1c4fd', '#c2e9fb', '#e0f7ff'], ['#f6d365', '#fda085', '#fbc2eb'], ['#43cea2', '#185a9d', '#0f2f4f'],
    ['#2c3e50', '#4ca1af', '#c4e0e5'], ['#e0e0e0', '#bdbdbd', '#8e9eab'], ['#3a1c71', '#d76d77', '#ffaf7b'],
  ];
  const css = stops => `linear-gradient(135deg, ${stops.join(', ')})`;
  const paintPreset = stops => {
    const c = document.createElement('canvas');
    c.width = 1280; c.height = 720;
    const x = c.getContext('2d');
    const g = x.createLinearGradient(0, 0, c.width, c.height);
    stops.forEach((s, i) => g.addColorStop(i / (stops.length - 1), s));
    x.fillStyle = g;
    x.fillRect(0, 0, c.width, c.height);
    // a few blurred circles, as the light of a room out of focus
    for (let i = 0; i < 7; i++) {
      const r = 60 + (i * 37) % 120, cx = (i * 211) % c.width, cy = (i * 137 + 90) % c.height;
      const rg = x.createRadialGradient(cx, cy, 0, cx, cy, r);
      rg.addColorStop(0, 'rgba(255,255,255,.28)');
      rg.addColorStop(1, 'rgba(255,255,255,0)');
      x.fillStyle = rg;
      x.fillRect(cx - r, cy - r, r * 2, r * 2);
    }
    return c;
  };

  // the segmenter is made once per page; the GPU where the browser has it, else the processor
  let seg;
  const segmenter = base => seg ||= (async () => {
    const { FilesetResolver, ImageSegmenter } = await import(base + 'vision_bundle.mjs');
    const files = await FilesetResolver.forVisionTasks(base + 'wasm');
    const make = delegate => ImageSegmenter.createFromOptions(files, {
      baseOptions: { modelAssetPath: base + 'selfie_segmenter.tflite', delegate },
      runningMode: 'VIDEO', outputConfidenceMasks: true, outputCategoryMask: false,
    });
    return make('GPU').catch(() => make('CPU'));
  })().catch(err => { seg = null; throw err; });
  // frames are paced by a worker: the timers of a page in a background tab slow down to once a second
  const ticker = ms => {
    const w = new Worker(URL.createObjectURL(new Blob([`setInterval(() => postMessage(0), ${ms})`], { type: 'text/javascript' })));
    return w;
  };

  // a LiveKit track processor: the camera picture → the person over the background, as a canvas stream
  class Background {
    constructor(base, paint) { this.name = 'go24-background'; this.base = base; this.paint = paint; }
    async init({ track, element }) {
      this.seg = await segmenter(this.base);
      this.src = element;
      this.out = document.createElement('canvas');
      this.person = document.createElement('canvas');
      this.mask = document.createElement('canvas');
      this.ctx = this.out.getContext('2d');
      this.pctx = this.person.getContext('2d');
      this.mctx = this.mask.getContext('2d');
      this.size(track);
      this.processedTrack = this.out.captureStream(30).getVideoTracks()[0];
      this.clock = ticker(33);
      this.clock.onmessage = () => this.frame();
    }
    async restart({ track, element }) { this.src = element; this.size(track); }
    async destroy() { this.clock?.terminate(); this.clock = null; }
    size(track) {
      const s = track?.getSettings?.() || {};
      this.resize(s.width || 640, s.height || 480);
    }
    resize(w, h) {
      if (this.out.width === w && this.out.height === h) return;
      this.out.width = this.person.width = w;
      this.out.height = this.person.height = h;
    }
    frame() {
      const v = this.src;
      if (!v || v.readyState < 2 || !v.videoWidth) return;
      this.resize(v.videoWidth, v.videoHeight);
      let ts = performance.now();
      if (ts <= this.last) ts = this.last + 1;
      this.last = ts;
      try { this.seg.segmentForVideo(v, ts, r => this.masked(r)); } catch (err) { console.warn(err); return; }
      this.draw(v);
    }
    // the mask of the person goes to the alpha of a small canvas, mixed with the last one so the edge does not flicker
    masked(r) {
      const m = r?.confidenceMasks?.[0];
      if (!m) return;
      const a = m.getAsFloat32Array();
      if (!this.img || this.img.width !== m.width || this.img.height !== m.height) {
        this.img = new ImageData(m.width, m.height);
        this.prev = new Float32Array(a.length);
        this.mask.width = m.width;
        this.mask.height = m.height;
      }
      const px = this.img.data, prev = this.prev;
      for (let i = 0; i < a.length; i++) px[i * 4 + 3] = (prev[i] = prev[i] * 0.35 + a[i] * 0.65) * 255;
      this.mctx.putImageData(this.img, 0, 0);
    }
    draw(v) {
      const { ctx, pctx } = this, w = this.out.width, h = this.out.height;
      this.paint(ctx, v, w, h);
      pctx.globalCompositeOperation = 'copy';
      pctx.drawImage(this.mask, 0, 0, w, h);
      pctx.globalCompositeOperation = 'source-in';
      pctx.drawImage(v, 0, 0, w, h);
      ctx.globalCompositeOperation = 'source-over';
      ctx.drawImage(this.person, 0, 0);
    }
  }

  // painters of the background: the camera picture blurred, or a picture covering the frame
  const filters = typeof document !== 'undefined' && 'filter' in (document.createElement('canvas').getContext?.('2d') || {});
  const small = document.createElement('canvas');
  const blur = r => (ctx, v, w, h) => {
    ctx.globalCompositeOperation = 'copy';
    if (filters) {
      ctx.filter = `blur(${r}px)`;
      ctx.drawImage(v, -r, -r, w + 2 * r, h + 2 * r);
      ctx.filter = 'none';
      return;
    }
    // no canvas filters (older Safari): down and up again blurs too
    small.width = Math.max(1, Math.round(w / r)); small.height = Math.max(1, Math.round(h / r));
    small.getContext('2d').drawImage(v, 0, 0, small.width, small.height);
    ctx.imageSmoothingEnabled = true;
    ctx.drawImage(small, 0, 0, w, h);
  };
  const cover = img => (ctx, _v, w, h) => {
    const iw = img.naturalWidth || img.width, ih = img.naturalHeight || img.height;
    const k = Math.max(w / iw, h / ih);
    ctx.globalCompositeOperation = 'copy';
    ctx.drawImage(img, (w - iw * k) / 2, (h - ih * k) / 2, iw * k, ih * k);
  };

  window.go24CallBg = (room, box) => {
    const LK = window.LivekitClient;
    const pane = box.querySelector('[data-call-bgs]');
    if (!pane) return;
    const base = pane.dataset.base;
    const status = box.querySelector('[data-call-status]');
    const tell = text => { status.hidden = false; status.textContent = text; setTimeout(() => { if (status.textContent === text) status.hidden = true; }, 5000); };
    if (typeof Worker === 'undefined' || !HTMLCanvasElement.prototype.captureStream || typeof WebAssembly === 'undefined') {
      pane.closest('[data-call-bg-pane]')?.setAttribute('hidden', '');
      return;
    }
    const upload = pane.querySelector('[data-call-bg-file]');
    const add = (kind, title, look) => {
      const b = document.createElement('button');
      b.type = 'button';
      b.className = 'callbg';
      b.dataset.bg = kind;
      b.title = title;
      if (look) b.style.backgroundImage = look;
      pane.insertBefore(b, upload.closest('label'));
      return b;
    };
    presets.forEach((p, i) => add('p' + (i + 1), pane.dataset.preset.replace('%s', i + 1), css(p)));
    let own = get(OWN);
    const ownBtn = () => pane.querySelector('[data-bg="own"]');
    const showOwn = () => {
      ownBtn()?.remove();
      if (!own) return;
      const b = add('own', pane.dataset.own, `url("${own}")`);
      const x = document.createElement('span');
      x.className = 'callbg-x';
      x.textContent = '✕';
      x.title = pane.dataset.remove;
      b.append(x);
    };
    showOwn();

    let choice = get(KEY) || 'none';
    if (!pane.querySelector(`[data-bg="${CSS.escape(choice)}"]`)) choice = 'none';
    const pictures = {};
    const picture = kind => {
      if (kind.startsWith('p')) return pictures[kind] ||= paintPreset(presets[+kind.slice(1) - 1]);
      return new Promise((ok, fail) => { const i = new Image(); i.onload = () => ok(i); i.onerror = fail; i.src = own; });
    };
    const painter = async kind => kind === 'blur' ? blur(6) : kind === 'blur2' ? blur(16) : cover(await picture(kind));
    const mark = () => pane.querySelectorAll('[data-bg]').forEach(b => b.classList.toggle('on', b.dataset.bg === choice));
    mark();

    const camera = () => room.localParticipant?.getTrackPublication?.(LK.Track.Source.Camera)?.track;
    let busy = Promise.resolve();
    const apply = () => busy = busy.then(async () => {
      const t = camera();
      if (!t?.setProcessor) return;
      const p = t.getProcessor?.();
      if (choice === 'none') { if (p) await t.stopProcessor(); return; }
      const paint = await painter(choice);
      if (p?.name === 'go24-background') { p.paint = paint; return; }
      try {
        tell(pane.dataset.loading);
        await t.setProcessor(new Background(base, paint));
        if (status.textContent === pane.dataset.loading) status.hidden = true;
      } catch (err) {
        console.warn(err);
        tell(pane.dataset.failed);
        choice = 'none';
        mark();
      }
    }).catch(err => console.warn(err));
    room.on(LK.RoomEvent.LocalTrackPublished, pub => { if (pub.source === LK.Track.Source.Camera && choice !== 'none') apply(); });

    pane.addEventListener('click', e => {
      const b = e.target.closest('[data-bg]');
      if (!b) return;
      if (e.target.closest('.callbg-x')) {
        own = null;
        put(OWN, null);
        showOwn();
        if (choice === 'own') choice = 'none';
      } else choice = b.dataset.bg;
      put(KEY, choice === 'none' ? null : choice);
      mark();
      apply();
    });
    // one's own picture: made no larger than 1280×720 so it fits in the browser's storage
    upload.addEventListener('change', async () => {
      const f = upload.files[0];
      upload.value = '';
      if (!f || !f.type.startsWith('image/')) return;
      const url = URL.createObjectURL(f);
      try {
        const img = await new Promise((ok, fail) => { const i = new Image(); i.onload = () => ok(i); i.onerror = fail; i.src = url; });
        const k = Math.min(1, 1280 / img.naturalWidth, 720 / img.naturalHeight);
        const c = document.createElement('canvas');
        c.width = Math.round(img.naturalWidth * k); c.height = Math.round(img.naturalHeight * k);
        c.getContext('2d').drawImage(img, 0, 0, c.width, c.height);
        own = c.toDataURL('image/jpeg', 0.85);
      } catch { tell(pane.dataset.failed); return; } finally { URL.revokeObjectURL(url); }
      put(OWN, own); // too large for the storage: still the background of this call
      showOwn();
      choice = 'own';
      put(KEY, choice);
      mark();
      apply();
    });
  };
})();
