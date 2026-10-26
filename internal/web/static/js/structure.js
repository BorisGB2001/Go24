// «Структура компании»: the search above the tree leaves the departments whose name or people (or a team
// of theirs) match
// (with the departments above them; under a found department — all of it), marks the people found and opens their
// lists; Enter scrolls to the first one. Administrators (.orgtree[data-struct-edit]) drag: a person of a department's
// list onto another department's card — moved there (op=move_user), a department by its title row onto another
// card — under it (op=move); the move goes to POST /company/departments, then the page is loaded again.
(() => {
  const tree = document.querySelector('.orgtree');
  if (!tree) return;
  const find = document.querySelector('.structfind'), none = document.querySelector('.structnone');
  const kids = dept => [...(dept.querySelector(':scope > .dkids')?.children || [])].filter(el => el.matches('.dept'));
  const top = tree.querySelector(':scope > .dept');

  // show marks a department and returns whether it stays: it matches, or something under it does
  const show = (dept, q, all) => {
    const card = dept.querySelector(':scope > .dcard');
    const named = card.querySelector('.dname')?.textContent.toLowerCase().includes(q);
    let hit = false;
    for (const p of card.querySelectorAll('.person')) {
      const found = p.textContent.toLowerCase().includes(q);
      p.classList.toggle('found', found);
      hit ||= found;
    }
    const list = card.querySelector('.dpeople');
    if (list?.querySelector('.found') && !list.open) {
      list.open = true;
      list.dataset.foundOpen = '';
    }
    // its teams: found by name or by a person; under a found department — all of them
    let team = false;
    for (const tc of dept.querySelectorAll(':scope > .dteams > .teamcard')) {
      const tnamed = tc.querySelector('.teamname')?.textContent.toLowerCase().includes(q);
      let thit = false;
      for (const p of tc.querySelectorAll('.person')) {
        const found = p.textContent.toLowerCase().includes(q);
        p.classList.toggle('found', found);
        thit ||= found;
      }
      const tlist = tc.querySelector('.dpeople');
      if (tlist?.querySelector('.found') && !tlist.open) {
        tlist.open = true;
        tlist.dataset.foundOpen = '';
      }
      tc.classList.toggle('found', !!tnamed);
      tc.classList.toggle('struct-hide', !(all || named || tnamed || thit));
      team ||= tnamed || thit;
    }
    let below = false;
    for (const k of kids(dept)) below = show(k, q, all || named) || below;
    const stays = all || named || hit || team || below;
    dept.classList.toggle('struct-hide', !stays);
    card.classList.toggle('found', !!named);
    return stays;
  };
  const search = () => {
    const q = find.value.trim().toLowerCase();
    for (const list of tree.querySelectorAll('.dpeople[data-found-open]')) { // what the last search opened
      list.open = false;
      delete list.dataset.foundOpen;
    }
    if (!q) {
      tree.querySelectorAll('.struct-hide, .found').forEach(el => el.classList.remove('struct-hide', 'found'));
      if (none) none.hidden = true;
      return;
    }
    const any = show(top, q, false);
    if (none) none.hidden = any;
  };
  find?.addEventListener('input', search);
  find?.addEventListener('keydown', e => {
    if (e.key === 'Escape' && find.value) {
      find.value = '';
      search();
    } else if (e.key === 'Enter') {
      e.preventDefault();
      tree.querySelector('.found')?.scrollIntoView?.({ block: 'center', behavior: 'smooth' });
    }
  });

  if (!('structEdit' in tree.dataset)) return;
  for (const p of tree.querySelectorAll('.dcard .dpeople .person')) p.draggable = true;
  for (const d of tree.querySelectorAll('.dkids .dept > .dcard > .dhead')) d.draggable = true;

  let dragged = null; // {kind: user|dept, id, from (user: their department; dept: its element)}
  const clear = () => tree.querySelectorAll('.drop-in').forEach(el => el.classList.remove('drop-in'));
  const target = e => {
    const card = e.target.closest?.('.dcard');
    if (!card || !dragged || !tree.contains(card)) return null;
    const dept = card.closest('.dept');
    if (dragged.kind === 'user') return dept.dataset.dept === dragged.from ? null : { card, dept };
    // a department: not onto itself nor under itself, nor where it already is
    if (dragged.el.contains(dept) || dragged.el.parentElement.closest('.dept') === dept) return null;
    return { card, dept };
  };

  tree.addEventListener('dragstart', e => {
    const p = e.target.closest?.('.dcard .dpeople .person');
    const head = e.target.closest?.('.dhead[draggable]');
    if (p) {
      dragged = { kind: 'user', id: p.dataset.user, from: p.closest('.dept').dataset.dept, el: p };
    } else if (head) {
      const el = head.closest('.dept');
      dragged = { kind: 'dept', id: el.dataset.dept, el };
    } else {
      return;
    }
    e.dataTransfer.effectAllowed = 'move';
    e.dataTransfer.setData('text/plain', dragged.id);
    dragged.el.classList.add('dragging');
  });
  tree.addEventListener('dragend', () => { dragged?.el.classList.remove('dragging'); dragged = null; clear(); });
  tree.addEventListener('dragover', e => {
    const t = target(e);
    clear();
    if (!t) return;
    e.preventDefault();
    t.card.classList.add('drop-in');
  });
  tree.addEventListener('drop', async e => {
    const t = target(e);
    clear();
    if (!t) return;
    e.preventDefault();
    const body = new URLSearchParams();
    if (dragged.kind === 'user') {
      body.set('op', 'move_user');
      body.set('user', dragged.id);
      body.set('from', dragged.from);
      body.set('to', t.dept.dataset.dept);
    } else {
      body.set('op', 'move');
      body.set('id', dragged.id);
      body.set('parent', t.dept.dataset.dept);
    }
    const res = await fetch('/company/departments', { method: 'POST', headers: { Accept: 'application/json' }, body });
    if (!res.ok) {
      let msg = res.statusText;
      try { msg = (await res.json()).error || msg; } catch { /* ignore */ }
      alert(msg);
      return;
    }
    location.reload();
  });
})();
