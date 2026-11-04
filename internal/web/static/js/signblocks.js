// «Подпись»: the blocks on the pages of the document — the sender picks a signer and a block (signature,
// name, date, stamp, text, a field the signer fills in — its caption from the text, required or not), clicks a page to put it there, drags it to move, by its corner to resize, × removes it;
// the form keeps them as JSON in «blocks» (the box in fractions of the page from its top left corner).
(() => {
  const form = document.querySelector('[data-sgblocks]');
  if (!form) return;
  const input = form.querySelector('input[name="blocks"]'), signer = form.querySelector('[data-signer]');
  const textIn = form.querySelector('[data-text]'), reqIn = form.querySelector('[data-required]'), count = form.querySelector('[data-count]');
  const sizes = { signature: [0.25, 0.06], name: [0.3, 0.03], date: [0.15, 0.03], stamp: [0.18, 0.13], text: [0.3, 0.03], field: [0.3, 0.03] };
  const label = (kind) => (form.querySelector(`[data-kind="${kind}"]`) || {}).textContent || kind;
  const who = (ord) => { const o = [...signer.options].find((o) => +o.value === ord); return o ? o.textContent : String(ord); };
  const clamp = (v, lo, hi) => Math.min(Math.max(v, lo), hi);
  const round = (v) => Math.round(v * 10000) / 10000;
  let blocks = [];
  try { blocks = JSON.parse(input.value || '[]') || []; } catch (_) { blocks = []; }
  let kind = '';

  const keep = () => {
    input.value = JSON.stringify(blocks.map(({ el, id, value, ...b }) => ({ ...b, x: round(b.x), y: round(b.y), w: round(b.w), h: round(b.h) })));
    count.textContent = (count.dataset.tpl || '%d').replace('%d', blocks.length);
  };
  const place = (b) => {
    Object.assign(b.el.style, { left: b.x * 100 + '%', top: b.y * 100 + '%', width: b.w * 100 + '%', height: b.h * 100 + '%' });
  };
  const draw = (b) => {
    const page = form.querySelector(`.sgb-page[data-page="${b.page}"]`);
    if (!page) return false;
    const el = document.createElement('div');
    el.className = 'sgb-block sgb-' + b.kind;
    el.tabIndex = 0;
    const cap = document.createElement('span');
    cap.textContent = label(b.kind) + ((b.kind === 'text' || b.kind === 'field') && b.text ? ': ' + b.text : '') +
      (b.required ? ' *' : '') + ' · ' + who(b.signer);
    const del = document.createElement('button');
    del.type = 'button'; del.className = 'sgb-del'; del.textContent = '×'; del.title = del.ariaLabel = form.dataset.del || '×';
    const size = document.createElement('i');
    size.className = 'sgb-size';
    el.append(cap, del, size);
    b.el = el;
    place(b);
    page.append(el);
    const remove = () => { el.remove(); blocks = blocks.filter((x) => x !== b); keep(); };
    del.addEventListener('click', (e) => { e.stopPropagation(); remove(); });
    el.addEventListener('keydown', (e) => { if (e.key === 'Delete' || e.key === 'Backspace') { e.preventDefault(); remove(); } });
    el.addEventListener('click', (e) => e.stopPropagation());
    el.addEventListener('pointerdown', (e) => {
      if (e.target === del) return;
      e.preventDefault();
      const r = page.getBoundingClientRect(), resize = e.target === size;
      const start = { px: e.clientX, py: e.clientY, x: b.x, y: b.y, w: b.w, h: b.h };
      el.setPointerCapture && el.setPointerCapture(e.pointerId);
      const move = (m) => {
        const dx = (m.clientX - start.px) / (r.width || 1), dy = (m.clientY - start.py) / (r.height || 1);
        if (resize) {
          b.w = clamp(start.w + dx, 0.02, 1 - b.x); b.h = clamp(start.h + dy, 0.01, 1 - b.y);
        } else {
          b.x = clamp(start.x + dx, 0, 1 - b.w); b.y = clamp(start.y + dy, 0, 1 - b.h);
        }
        place(b);
      };
      const up = () => { el.removeEventListener('pointermove', move); el.removeEventListener('pointerup', up); keep(); };
      el.addEventListener('pointermove', move);
      el.addEventListener('pointerup', up);
    });
    return true;
  };

  form.querySelectorAll('[data-kind]').forEach((btn) => btn.addEventListener('click', () => {
    kind = kind === btn.dataset.kind ? '' : btn.dataset.kind;
    form.querySelectorAll('[data-kind]').forEach((x) => x.setAttribute('aria-pressed', String(x.dataset.kind === kind)));
    form.classList.toggle('sgb-placing', !!kind);
  }));
  form.querySelectorAll('.sgb-page').forEach((page) => page.addEventListener('click', (e) => {
    if (!kind) return;
    const r = page.getBoundingClientRect(), [w, h] = sizes[kind];
    const b = { signer: +signer.value, kind, page: +page.dataset.page, w, h,
      x: clamp((e.clientX - r.left) / (r.width || 1) - w / 2, 0, 1 - w), y: clamp((e.clientY - r.top) / (r.height || 1) - h / 2, 0, 1 - h) };
    if (kind === 'text' || kind === 'field') {
      b.text = textIn.value.trim();
      if (!b.text) { textIn.focus(); return; }
    }
    if (kind === 'field' && reqIn && reqIn.checked) b.required = true;
    blocks.push(b);
    draw(b);
    keep();
    kind = '';
    form.querySelectorAll('[data-kind]').forEach((x) => x.setAttribute('aria-pressed', 'false'));
    form.classList.remove('sgb-placing');
  }));
  blocks = blocks.filter(draw);
  keep();
})();
