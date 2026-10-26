// «База знаний»: the tree of pages at the left, as in Confluence. The open page is marked and the branches
// down to it are open; a branch is opened or closed with its arrow (remembered for the base in this browser). Those
// who write in the base (nav[data-kb-edit]) get «+» — a subpage — at each page and drag pages: dropped onto the
// upper or lower edge of a page — before or after it, onto its middle — under it, last. The new place goes to
// POST /kb/{space}/{page}/place (parent, before), then the page is loaded again.
(() => {
  const nav = document.querySelector('nav.kbnav');
  const root = nav?.querySelector(':scope > ul.kbtree');
  if (!root) return;
  root.classList.add('kbtree-js');
  const space = nav.dataset.kbSpace, edit = 'kbEdit' in nav.dataset;
  const key = 'go24.kb.open.' + space;
  let opened = [];
  try { opened = JSON.parse(localStorage.getItem(key) || '[]'); } catch { /* ignore */ }
  const remember = () => {
    const ids = [...root.querySelectorAll('li.open')].map(li => li.dataset.kbId);
    try { localStorage.setItem(key, JSON.stringify(ids)); } catch { /* ignore */ }
  };

  for (const li of root.querySelectorAll('li')) {
    const a = li.querySelector(':scope > a');
    li.dataset.kbId = a.dataset.kbPage;
    const sub = li.querySelector(':scope > ul');
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'kbtoggle';
    if (sub) {
      btn.title = nav.dataset.kbToggle || '';
      btn.addEventListener('click', () => { li.classList.toggle('open'); remember(); });
      if (opened.includes(li.dataset.kbId)) li.classList.add('open');
    } else {
      btn.disabled = true;
    }
    li.insertBefore(btn, a);
    if (edit) {
      const add = document.createElement('a');
      add.className = 'kbadd';
      add.href = `/kb/${space}?new=1&parent=${encodeURIComponent(li.dataset.kbId)}`;
      add.title = nav.dataset.kbAdd || '';
      add.textContent = '+';
      a.after(add);
      a.draggable = true;
    }
  }

  const cur = document.querySelector('[data-kb-current]')?.dataset.kbCurrent;
  const on = cur && root.querySelector(`[data-kb-page="${CSS.escape(cur)}"]`);
  if (on) {
    on.classList.add('on');
    for (let li = on.closest('li'); li; li = li.parentElement.closest('li')) {
      if (li.querySelector(':scope > ul')) li.classList.add('open');
    }
  }
  if (!edit) return;

  let dragged = null;
  const clear = () => root.querySelectorAll('.drop-before, .drop-after, .drop-in').forEach(el => el.classList.remove('drop-before', 'drop-after', 'drop-in'));
  // where a drop onto the link goes: before, after or in
  const where = (a, e) => {
    const r = a.getBoundingClientRect(), y = e.clientY - r.top;
    return y < r.height / 4 ? 'before' : y > r.height * 3 / 4 ? 'after' : 'in';
  };
  const target = e => {
    const a = e.target.closest?.('a[data-kb-page]');
    if (!a || !dragged || !root.contains(a)) return null;
    const li = a.closest('li');
    if (li === dragged || dragged.contains(li)) return null; // not onto itself nor under itself
    return { a, li, at: where(a, e) };
  };

  root.addEventListener('dragstart', e => {
    const a = e.target.closest?.('a[data-kb-page]');
    if (!a) return;
    dragged = a.closest('li');
    e.dataTransfer.effectAllowed = 'move';
    e.dataTransfer.setData('text/plain', dragged.dataset.kbId);
    dragged.classList.add('dragging');
  });
  root.addEventListener('dragend', () => { dragged?.classList.remove('dragging'); dragged = null; clear(); });
  root.addEventListener('dragover', e => {
    const t = target(e);
    clear();
    if (!t) return;
    e.preventDefault();
    t.a.classList.add('drop-' + t.at);
  });
  root.addEventListener('drop', async e => {
    const t = target(e);
    clear();
    if (!t) return;
    e.preventDefault();
    const id = dragged.dataset.kbId, body = new URLSearchParams();
    if (t.at === 'in') {
      body.set('parent', t.li.dataset.kbId);
    } else {
      body.set('parent', t.li.parentElement.closest('li')?.dataset.kbId || '');
      let next = t.at === 'before' ? t.li : t.li.nextElementSibling;
      if (next === dragged) next = dragged.nextElementSibling;
      body.set('before', next?.dataset.kbId || '');
    }
    if (t.at === 'in') {
      t.li.classList.add('open');
      remember();
    }
    const res = await fetch(`/kb/${space}/${id}/place`, { method: 'POST', headers: { Accept: 'application/json' }, body });
    if (!res.ok) {
      let msg = res.statusText;
      try { msg = (await res.json()).error || msg; } catch { /* ignore */ }
      alert(msg);
      return;
    }
    location.reload();
  });
})();
