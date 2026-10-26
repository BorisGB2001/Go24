// «Файл с Диска»: a button [data-disk-pick] opens the drive picker (my drive, the company's, the projects', folders
// shared with me). The chosen file is either sent at once (data-to: POST file=ID there, then the page reloads) or
// kept in the button's form as a hidden input (data-input: its name) with a chip, sent with the form.
(function () {
  const el = (tag, cls, text) => { const n = document.createElement(tag); if (cls) n.className = cls; if (text != null) n.textContent = text; return n; };
  let dlg = null;
  const close = () => { dlg?.remove(); dlg = null; };

  const choose = async (btn, f) => {
    const l = btn.dataset;
    if (l.to) {
      const res = await fetch(l.to, { method: 'POST', headers: { 'X-Go24-Form': '1', Accept: 'application/json' }, body: new URLSearchParams({ file: f.id }) });
      const data = await res.json().catch(() => ({}));
      if (!res.ok) { alert(data.error || l.lFailed); return; }
      close();
      const u = new URL(data.redirect || location.href, location.href);
      if (u.pathname !== location.pathname) { location.href = u; return; }
      location.hash = u.hash;
      location.reload();
      return;
    }
    const form = btn.closest('form');
    if (form.querySelector(`input[name="${l.input}"][value="${f.id}"]`)) { close(); return; }
    const chip = el('span', 'pill dpick-chip', '📎 ' + f.name + ' ');
    const input = el('input');
    input.type = 'hidden';
    input.name = l.input;
    input.value = f.id;
    const x = el('button', 'ghost small', '×');
    x.type = 'button';
    x.title = l.lRemove;
    x.addEventListener('click', () => chip.remove());
    chip.append(input, x);
    (form.querySelector('[data-disk-picked]') || btn.parentNode).append(chip);
    close();
  };

  const open = async (btn, which = 'my', folder = '') => {
    const l = btn.dataset;
    let d;
    try {
      const res = await fetch(`/disk/pick?which=${encodeURIComponent(which)}&folder=${encodeURIComponent(folder)}`, { headers: { Accept: 'application/json' } });
      d = await res.json();
      if (!res.ok) throw new Error(d.error);
    } catch (err) { alert(err.message || l.lFailed); return; }
    close();
    dlg = el('div', 'mviewer mpicker');
    dlg.setAttribute('role', 'dialog');
    dlg.dataset.diskPickDlg = '';
    const card = el('div', 'card mpicker-card');
    const head = el('div', 'row');
    const drives = el('select');
    drives.setAttribute('aria-label', l.lTitle);
    for (const v of d.drives || []) { const o = el('option', '', v.name); o.value = v.which; o.selected = v.which === d.which; drives.append(o); }
    drives.addEventListener('change', () => open(btn, drives.value, ''));
    const x = el('button', 'ghost small', '×');
    x.type = 'button';
    x.title = l.lClose;
    x.addEventListener('click', close);
    head.append(el('b', '', l.lTitle), drives, el('span', 'grow'), x);
    const go = (text, id) => { const b = el('button', 'ghost small', text); b.type = 'button'; b.addEventListener('click', () => open(btn, d.which, id)); return b; };
    const crumbs = el('div', 'row small mpicker-path');
    crumbs.append(go(l.lTop, ''));
    (d.path || []).forEach(p => crumbs.append('/', go(p.name, p.id)));
    const items = el('div', 'mpicker-items');
    (d.folders || []).forEach(f => { const b = go('📁 ' + f.name, f.id); b.className = 'ghost mpicker-item'; items.append(b); });
    (d.files || []).forEach(f => {
      const b = el('button', 'ghost mpicker-item');
      b.type = 'button';
      b.dataset.file = f.id;
      b.append(el('span', 'grow', f.name), el('span', 'small muted', f.size_text));
      b.addEventListener('click', () => choose(btn, f));
      items.append(b);
    });
    if (!items.children.length) items.append(el('p', 'muted', l.lEmpty));
    card.append(head, crumbs, items);
    dlg.append(card);
    dlg.addEventListener('click', e => { if (e.target === dlg) close(); });
    document.body.append(dlg);
  };

  document.addEventListener('click', e => {
    const btn = e.target.closest?.('[data-disk-pick]');
    if (!btn) return;
    e.preventDefault();
    open(btn);
  });
  document.addEventListener('keydown', e => { if (dlg && e.key === 'Escape') close(); });
  // a form sent and cleared: its chosen files go too
  document.addEventListener('reset', e => setTimeout(() => e.target.querySelectorAll?.('.dpick-chip').forEach(c => c.remove())));
})();
