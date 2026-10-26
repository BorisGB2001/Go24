// @-mentions: in a field with [data-mention], "@" and the start of a name show matching employees; a click,
// Enter or Tab puts "@Full Name " into the text. The server finds the mentions by these names.
(() => {
  let people = null; // [{id, name}], loaded once per page
  const load = async () => {
    if (people) return people;
    try {
      const res = await fetch('/people', { headers: { Accept: 'application/json' } });
      people = res.ok ? await res.json() : [];
    } catch { people = []; }
    return people;
  };
  let box = null, field = null, start = 0, items = [], sel = 0;
  const close = () => { box?.remove(); box = null; field = null; };
  const pick = p => {
    const f = field;
    const caret = f.selectionStart;
    f.value = f.value.slice(0, start) + '@' + p.name + ' ' + f.value.slice(caret);
    const pos = start + p.name.length + 2;
    f.setSelectionRange(pos, pos);
    close();
    f.focus();
    f.dispatchEvent(new Event('input', { bubbles: true }));
  };
  const show = () => {
    if (!items.length) { close(); return; }
    if (!box) {
      box = document.createElement('div');
      box.className = 'mention-pop';
      box.addEventListener('mousedown', e => { // before the field loses focus
        const el = e.target.closest('[data-i]');
        if (el) { e.preventDefault(); pick(items[Number(el.dataset.i)]); }
      });
      document.body.append(box);
      window.go24Pop?.(box, close, field);
    }
    box.innerHTML = '';
    items.forEach((p, i) => {
      const el = document.createElement('div');
      el.className = 'mention-item' + (i === sel ? ' on' : '');
      el.dataset.i = i;
      el.textContent = p.name;
      box.append(el);
    });
    const r = field.getBoundingClientRect();
    const h = Math.min(items.length, 8) * 34 + 8;
    const below = r.bottom + h < window.innerHeight;
    box.style.left = Math.max(8, Math.min(r.left, window.innerWidth - 288)) + 'px';
    box.style.top = (below ? r.bottom + 4 : Math.max(8, r.top - h - 4)) + 'px';
  };
  document.addEventListener('input', async e => {
    const f = e.target;
    if (!f.matches?.('[data-mention]')) return;
    const before = f.value.slice(0, f.selectionStart);
    const m = before.match(/(?:^|[\s(«"])@([^@\n]{0,40})$/u);
    if (!m || m[1].split(' ').length > 3) { if (field === f) close(); return; }
    const q = m[1].toLowerCase();
    const list = await load();
    if (f.value.slice(0, f.selectionStart) !== before) return; // typed on meanwhile
    field = f;
    start = before.length - m[1].length - 1;
    items = list.filter(p => {
      const n = p.name.toLowerCase();
      return n.startsWith(q) || n.split(' ').some(w => w.startsWith(q));
    }).slice(0, 8);
    sel = 0;
    show();
  });
  // capture: runs before the chat's own Enter-to-send
  document.addEventListener('keydown', e => {
    if (!box || e.target !== field) return;
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      sel = (sel + (e.key === 'ArrowDown' ? 1 : items.length - 1)) % items.length;
      show();
    } else if (e.key === 'Enter' || e.key === 'Tab') {
      e.preventDefault();
      e.stopImmediatePropagation();
      pick(items[sel]);
    } else if (e.key === 'Escape') {
      e.stopImmediatePropagation();
      close();
    }
  }, true);
  document.addEventListener('focusout', e => { if (e.target === field) setTimeout(close, 150); });
  addEventListener('scroll', () => box && field && show(), true);
})();
