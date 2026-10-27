// «Отправить в чат»: a button [data-disk-send="file id"] (data-name — the file's name) opens the list of the
// person's chats with a search; the chosen chat gets the file as a message from them, then «Открыть чат» leads there.
(() => {
  const me = document.currentScript;
  const l = k => me.dataset['l' + k[0].toUpperCase() + k.slice(1)] || k;
  const el = (tag, cls, text) => { const n = document.createElement(tag); if (cls) n.className = cls; if (text != null) n.textContent = text; return n; };
  let chats = null;

  const open = async (id, name) => {
    const dlg = el('dialog', 'im-fwd-pick dsend');
    const form = el('form');
    form.method = 'dialog';
    const filter = el('input');
    filter.type = 'search';
    filter.placeholder = l('filter');
    const list = el('div', 'im-fwd-list');
    const note = el('p', 'small');
    const close = el('button', 'ghost', l('close'));
    const foot = el('p', 'row');
    foot.append(close);
    form.append(el('b', '', l('title').replace('%s', name)), filter, list, note, foot);
    dlg.append(form);
    dlg.addEventListener('close', () => dlg.remove());
    document.body.append(dlg);
    dlg.showModal?.();
    if (!chats) {
      try {
        const res = await fetch('/disk/chats', { headers: { Accept: 'application/json' } });
        const d = await res.json();
        if (!res.ok) throw new Error(d.error);
        chats = d;
      } catch (e) { note.textContent = e.message || l('failed'); return; }
    }
    const draw = () => {
      const q = filter.value.trim().toLowerCase();
      const shown = chats.filter(c => !q || c.name.toLowerCase().includes(q));
      list.replaceChildren(...shown.map(c => {
        const b = el('button', 'ghost', c.name);
        b.type = 'button';
        b.dataset.room = c.id;
        return b;
      }));
      if (!shown.length) list.append(el('span', 'muted small', l('none')));
    };
    draw();
    filter.addEventListener('input', draw);
    filter.focus();
    list.addEventListener('click', async e => {
      const b = e.target.closest('[data-room]');
      if (!b) return;
      list.querySelectorAll('button').forEach(x => { x.disabled = true; });
      const res = await fetch('/files/' + encodeURIComponent(id) + '/send', {
        method: 'POST', headers: { 'X-Go24-Form': '1', Accept: 'application/json' }, body: new URLSearchParams({ room: b.dataset.room }) });
      const d = await res.json().catch(() => ({}));
      if (!res.ok) {
        note.textContent = d.error || l('failed');
        list.querySelectorAll('button').forEach(x => { x.disabled = false; });
        return;
      }
      filter.hidden = list.hidden = true;
      note.textContent = d.text + ': ' + b.textContent;
      const go = el('a', 'button primary', l('open'));
      go.href = d.url;
      foot.prepend(go);
    });
  };

  document.addEventListener('click', e => {
    const b = e.target.closest('[data-disk-send]');
    if (!b) return;
    e.preventDefault();
    b.closest('details')?.removeAttribute('open');
    open(b.dataset.diskSend, b.dataset.name || '');
  });
})();
