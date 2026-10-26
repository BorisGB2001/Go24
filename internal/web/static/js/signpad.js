// «Подпись»: «Ваша подпись» — the signer draws it (mouse, finger, pen), writes it by the name or uploads
// a picture; the form gets it as a PNG data URL in «signature». A signature kept to sign with is drawn at once; a
// drawn one outlives the page reloaded (a wrong code) in the session. Signing needs one.
(() => {
  const key = () => 'sgpad:' + location.pathname;
  const store = (v) => { try { v ? sessionStorage.setItem(key(), v) : sessionStorage.removeItem(key()); } catch (_) {} };
  const stored = () => { try { return sessionStorage.getItem(key()) || ''; } catch (_) { return ''; } };

  const init = (pad) => {
    const canvas = pad.querySelector('[data-pad]'), input = pad.querySelector('input[name="signature"]');
    const ctx = canvas.getContext('2d');
    if (!ctx) return;
    let empty = true, last = null;
    const pen = () => { ctx.lineWidth = 4; ctx.lineCap = ctx.lineJoin = 'round'; ctx.strokeStyle = '#1a2a6c'; };
    const clear = () => { ctx.clearRect(0, 0, canvas.width, canvas.height); empty = true; input.value = ''; store(''); };
    // the signature without the empty margins of the pad: it fills its cell of the signing sheet
    const cropped = () => {
      const { width: w, height: h } = canvas, px = ctx.getImageData(0, 0, w, h).data;
      let x0 = w, y0 = h, x1 = -1, y1 = -1;
      for (let y = 0; y < h; y++) for (let x = 0; x < w; x++) {
        if (px[(y * w + x) * 4 + 3] > 0) { x0 = Math.min(x0, x); x1 = Math.max(x1, x); y0 = Math.min(y0, y); y1 = Math.max(y1, y); }
      }
      if (x1 < 0) return '';
      const m = 6, out = document.createElement('canvas');
      x0 = Math.max(0, x0 - m); y0 = Math.max(0, y0 - m); x1 = Math.min(w - 1, x1 + m); y1 = Math.min(h - 1, y1 + m);
      out.width = x1 - x0 + 1; out.height = y1 - y0 + 1;
      out.getContext('2d').drawImage(canvas, x0, y0, out.width, out.height, 0, 0, out.width, out.height);
      return out.toDataURL('image/png');
    };
    const keep = () => {
      input.value = cropped(); empty = !input.value;
      store(input.value); pad.classList.toggle('sgpad-need', empty);
    };
    // a picture fits the pad, keeping its proportions, in the middle
    const put = (img) => {
      ctx.clearRect(0, 0, canvas.width, canvas.height);
      const k = Math.min(canvas.width / img.width, canvas.height / img.height, 1);
      const w = img.width * k, h = img.height * k;
      ctx.drawImage(img, (canvas.width - w) / 2, (canvas.height - h) / 2, w, h);
      keep();
    };
    const load = (src) => { const img = new Image(); img.onload = () => put(img); img.src = src; };
    const at = (e) => {
      const r = canvas.getBoundingClientRect();
      return [(e.clientX - r.left) * canvas.width / r.width, (e.clientY - r.top) * canvas.height / r.height];
    };
    canvas.addEventListener('pointerdown', e => {
      e.preventDefault();
      canvas.setPointerCapture?.(e.pointerId);
      last = at(e);
      pen();
      ctx.beginPath(); ctx.arc(last[0], last[1], 1.5, 0, 2 * Math.PI); ctx.fillStyle = ctx.strokeStyle; ctx.fill();
    });
    canvas.addEventListener('pointermove', e => {
      if (!last) return;
      const p = at(e);
      pen();
      ctx.beginPath(); ctx.moveTo(last[0], last[1]); ctx.lineTo(p[0], p[1]); ctx.stroke();
      last = p;
    });
    const up = () => { if (last) { last = null; keep(); } };
    canvas.addEventListener('pointerup', up);
    canvas.addEventListener('pointercancel', up);
    pad.querySelector('[data-pad-clear]')?.addEventListener('click', clear);
    pad.querySelector('[data-pad-write]')?.addEventListener('click', () => {
      const name = canvas.dataset.name || '';
      if (!name) return;
      ctx.clearRect(0, 0, canvas.width, canvas.height);
      let size = 72;
      do { ctx.font = `italic ${size}px "Segoe Script", "Brush Script MT", "Comic Sans MS", cursive`; size -= 4; }
      while (ctx.measureText(name).width > canvas.width - 40 && size > 16);
      ctx.fillStyle = '#1a2a6c'; ctx.textAlign = 'center'; ctx.textBaseline = 'middle';
      ctx.fillText(name, canvas.width / 2, canvas.height / 2);
      keep();
    });
    pad.querySelector('[data-pad-file]')?.addEventListener('change', e => {
      const f = e.target.files[0];
      if (!f) return;
      const rd = new FileReader();
      rd.onload = () => load(rd.result);
      rd.readAsDataURL(f);
      e.target.value = '';
    });
    const first = input.value || stored();
    if (first) load(first);
    // signing needs a signature; refusing (another form) does not
    pad.closest('form')?.addEventListener('submit', e => {
      if (!empty || input.value) return;
      e.preventDefault();
      pad.classList.add('sgpad-need');
      pad.scrollIntoView({ block: 'center' });
      alert(pad.dataset.need);
    });
    if (!first) clear();
  };
  const start = () => document.querySelectorAll('[data-sgpad]').forEach(init);
  document.readyState === 'loading' ? document.addEventListener('DOMContentLoaded', start) : start();
})();
