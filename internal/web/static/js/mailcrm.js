// «Почта» → CRM: the search of a card to link the letter to (the reading pane's CRM menu). Typing finds deals, leads,
// contacts, companies (/mail/crm/search); a found one, picked, sends the form with its address as the target.
(() => {
  let timer = 0, seq = 0;
  const show = async (input) => {
    const form = input.closest('[data-crm-search]');
    const box = form?.querySelector('[data-crm-res]');
    if (!box) return;
    const q = input.value.trim();
    const n = ++seq;
    if (q.length < 2) { box.replaceChildren(); return; }
    let list = [];
    try {
      const res = await fetch('/mail/crm/search?q=' + encodeURIComponent(q), { headers: { Accept: 'application/json' } });
      if (res.ok) list = await res.json();
    } catch { list = []; }
    if (n !== seq) return; // a later search is on its way
    box.replaceChildren(...list.map(it => {
      const b = document.createElement('button');
      b.type = 'button';
      b.className = 'ghost small';
      b.setAttribute('role', 'option');
      b.dataset.path = it.path;
      b.textContent = it.kind_name + ': ' + it.title + (it.sub ? ' · ' + it.sub : '');
      return b;
    }));
  };
  document.addEventListener('input', e => {
    const input = e.target.closest?.('[data-crm-q]');
    if (!input) return;
    clearTimeout(timer);
    timer = setTimeout(() => show(input), 250);
  });
  document.addEventListener('click', e => {
    const b = e.target.closest?.('[data-crm-res] button[data-path]');
    if (!b) return;
    const form = b.closest('[data-crm-search]');
    form.querySelector('[data-crm-target]').value = b.dataset.path;
    form.requestSubmit ? form.requestSubmit() : form.submit();
  });
  document.addEventListener('keydown', e => { // Enter in the search picks the first card found
    const input = e.target.closest?.('[data-crm-q]');
    if (!input || e.key !== 'Enter') return;
    e.preventDefault();
    input.closest('[data-crm-search]')?.querySelector('[data-crm-res] button[data-path]')?.click();
  });
})();

// «Поделиться»: the user's chats come when the menu opens (/mail/share/rooms); a chat picked hides the new chat's
// name and people; «Участникам переписки» of a post shows its people.
(() => {
  const sync = (menu) => {
    menu.querySelectorAll('form').forEach(f => {
      const to = f.querySelector('[data-share-to]');
      const people = f.querySelector('[data-share-people]');
      if (to && people) people.hidden = to.value !== 'people';
      const room = f.querySelector('[data-share-rooms]');
      const fresh = f.querySelector('[data-share-new]');
      if (room && fresh) fresh.hidden = room.value !== '';
    });
  };
  document.addEventListener('toggle', async e => {
    const menu = e.target.closest?.('[data-share]');
    if (!menu || !menu.open) return;
    sync(menu);
    const select = menu.querySelector('[data-share-rooms]');
    if (!select || select.dataset.loaded) return;
    select.dataset.loaded = '1';
    try {
      const res = await fetch('/mail/share/rooms', { headers: { Accept: 'application/json' } });
      if (!res.ok) return;
      for (const r of await res.json()) select.add(new Option(r.name, r.id));
    } catch { /* the new chat stays */ }
  }, true);
  document.addEventListener('change', e => {
    if (e.target.matches?.('[data-share-to], [data-share-rooms]')) sync(e.target.closest('[data-share]'));
  });
})();
