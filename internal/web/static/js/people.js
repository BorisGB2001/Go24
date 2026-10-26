// The people picker of every form ([data-upick], template "upick") ui.entity-selector: the chosen
// people are chips (each carries a hidden input, so the form posts them), typing searches the server from the
// first letter (name, surname, position, department, email; «ё», case and the keyboard layout do not matter).
// The list opens with the tabs «Последние» (people of my recent tasks), «Отделы» (a department opened shows its
// sub-departments and only its people, typing searches within it; for several, the whole department) and «Все». ↑↓ Enter choose, Esc closes, Backspace in the empty
// field removes the last chip; one person (responsible) or several (accomplices, a flow's team).
(() => {
  const PAGE = 50;
  let box = null, pop = null, state = null, timer = 0, depts = null;
  const multi = b => 'multiple' in b.dataset;
  const field = b => b.querySelector('.upick-q');
  const chosen = b => new Set([...b.querySelectorAll('.upick-chip')].map(c => c.dataset.id));
  const fold = s => (s || '').toLowerCase().replaceAll('ё', 'е');

  const get = async url => {
    try {
      const res = await fetch(url, { headers: { Accept: 'application/json' } });
      return res.ok ? await res.json() : [];
    } catch { return []; }
  };
  // data-collab: only that collab's members; "@project" — the collab picked in the form's project field (a new task)
  const collab = b => {
    const c = b.dataset.collab || '';
    if (c !== '@project') return c;
    const o = b.closest('form')?.querySelector('select[name="project"]')?.selectedOptions[0];
    return o && 'collab' in o.dataset ? o.value : '';
  };
  const people = (b, params) => {
    const q = new URLSearchParams(params);
    if ('self' in b.dataset) q.set('self', '1');
    if ('im' in b.dataset) q.set('im', '1');
    const c = collab(b);
    if (c) q.set('collab', c);
    return get('/people?' + q);
  };

  // the avatar as on the server (web.avatar): a photo or initials on a hue taken from the name
  const avatar = p => {
    const el = document.createElement('span');
    el.className = 'ava';
    if (p.photo) { el.classList.add('photo'); el.style.setProperty('--avatar-photo', `url("${p.photo}")`); return el; }
    let h = 0x811c9dc5;
    for (const c of new TextEncoder().encode(p.name)) { h ^= c; h = Math.imul(h, 0x01000193) >>> 0; }
    el.style.setProperty('--h', h % 360);
    el.textContent = (p.name.split(/\s+/).filter(w => /^[\p{L}\p{N}]/u.test(w)).slice(0, 2).map(w => [...w][0]).join('') || '?').toUpperCase();
    return el;
  };

  const changed = (b, submit) => {
    b.dispatchEvent(new Event('change', { bubbles: true }));
    if (submit && 'submit' in b.dataset) b.closest('form')?.requestSubmit();
  };
  // the mark «гость» of a guest of collabs
  const guestMark = b => {
    const m = document.createElement('span');
    m.className = 'pill guest';
    m.textContent = b.dataset.guestMark || 'guest';
    return m;
  };
  const chip = (b, p) => {
    const el = document.createElement('span');
    el.className = 'upick-chip';
    el.dataset.id = p.id;
    const name = document.createElement('span');
    name.className = 'upick-name';
    name.textContent = p.name;
    if (p.guest) name.append(' ', guestMark(b));
    const input = document.createElement('input');
    input.type = 'hidden'; input.name = b.dataset.name; input.value = p.id;
    el.append(name, input);
    if (!('required' in b.dataset)) {
      const rm = document.createElement('button');
      rm.type = 'button'; rm.className = 'ghost'; rm.dataset.rm = ''; rm.title = b.dataset.rmTitle || '×';
      rm.innerHTML = '<i class="i i-x" aria-hidden="true"></i>';
      el.append(rm);
    }
    return el;
  };
  const add = (b, list) => {
    const have = chosen(b);
    if (!multi(b)) { b.querySelectorAll('.upick-chip').forEach(c => c.remove()); have.clear(); list = list.slice(0, 1); }
    list.filter(p => !have.has(p.id)).forEach(p => field(b).before(chip(b, p)));
    field(b).value = '';
    changed(b, true);
  };
  const remove = (b, id) => {
    b.querySelector(`.upick-chip[data-id="${CSS.escape(id)}"]`)?.remove();
    changed(b, false);
  };

  // ---------- the list ----------
  const extras = (b, q) => [...b.querySelectorAll('[data-extra]')]
    .map(e => ({ id: e.dataset.extra, name: e.textContent, extra: true }))
    .filter(p => !q || fold(p.name).split(/\s+/).some(w => w.startsWith(fold(q).trim())));

  const close = () => {
    pop?.remove();
    pop = null; box = null; state = null;
    clearTimeout(timer);
  };
  const place = () => {
    if (!pop || !box) return;
    const r = box.getBoundingClientRect();
    pop.style.left = Math.max(8, Math.min(r.left + scrollX, scrollX + document.documentElement.clientWidth - pop.offsetWidth - 8)) + 'px';
    pop.style.top = (r.bottom + scrollY + 4) + 'px';
    pop.style.minWidth = Math.min(Math.max(r.width, 300), document.documentElement.clientWidth - 16) + 'px';
  };

  const render = () => {
    if (!pop) return;
    const tabs = pop.querySelector('.upick-tabs');
    tabs.hidden = state.tab === 'search';
    tabs.querySelectorAll('button').forEach(t => t.classList.toggle('on', t.dataset.tab === state.tab));
    const crumbs = pop.querySelector('.upick-crumbs');
    crumbs.hidden = state.tab !== 'depts' || !state.dept;
    crumbs.innerHTML = '';
    for (let d = state.dept, path = []; d && !crumbs.hidden; d = byID(d.parent)) {
      path.unshift(d);
      if (!d.parent) path.forEach((x, i) => {
        if (i) crumbs.append(' › ');
        const a = document.createElement(x === state.dept ? 'b' : 'a');
        a.textContent = x.name;
        a.dataset.crumb = x.id;
        crumbs.append(a);
      });
    }
    const list = pop.querySelector('.upick-list');
    list.innerHTML = '';
    const have = chosen(box);
    state.items.forEach((it, i) => {
      const row = document.createElement('div');
      row.className = 'upick-row ' + it.kind + (i === state.sel ? ' on' : '');
      row.dataset.i = i;
      row.setAttribute('role', 'option');
      if (it.kind === 'person') {
        if (have.has(it.p.id)) row.classList.add('chosen');
        if (!it.p.extra) row.append(avatar(it.p));
        const text = document.createElement('span');
        text.className = 'upick-text';
        const n = document.createElement('span');
        n.textContent = it.p.name;
        text.append(n);
        if (it.p.guest) n.append(' ', guestMark(box));
        const sub = [it.p.position, it.p.dept].filter(Boolean).join(' · ');
        if (sub) { const s = document.createElement('small'); s.textContent = sub; text.append(s); }
        row.append(text);
      } else if (it.kind === 'dept') {
        const n = document.createElement('span');
        n.className = 'upick-dname';
        n.textContent = it.d.name;
        const c = document.createElement('small');
        c.textContent = it.d.people;
        row.append(n, c);
        if (multi(box) && it.d.people) { // the whole department: a button of its own, the name opens it
          const all = document.createElement('button');
          all.type = 'button'; all.className = 'ghost'; all.dataset.add = ''; all.title = box.dataset.deptAll;
          all.innerHTML = '<i class="i i-plus" aria-hidden="true"></i>';
          row.append(all);
        }
      } else if (it.kind === 'back') {
        row.textContent = '‹ ' + it.d.name;
      } else if (it.kind === 'scope') {
        const n = document.createElement('span');
        n.textContent = `${box.dataset.inDept} «${it.d.name}»`;
        const all = document.createElement('b');
        all.textContent = box.dataset.everywhere;
        row.append(n, all);
      } else {
        row.textContent = it.kind === 'more' ? box.dataset.more : box.dataset.deptAll + ' · ' + it.d.name;
      }
      list.append(row);
    });
    if (!state.items.length) {
      const none = document.createElement('div');
      none.className = 'upick-none muted';
      none.textContent = state.loading ? '…' : box.dataset.empty;
      list.append(none);
    }
    list.querySelector('.on')?.scrollIntoView?.({ block: 'nearest' });
    place();
  };

  const byID = id => id && depts?.find(d => d.id === id);
  // the whole department: its people, or (data-dept-chip) the department itself as one choice
  const addDept = async d => {
    add(box, 'deptChip' in box.dataset ? [{ id: 'dept:' + d.id, name: '🏢 ' + d.name }] : await people(box, { dept: d.id, deep: '1' }));
    render();
  };

  const show = async (tab, extra = {}) => {
    const b = box, st = { tab, items: [], sel: 0, loading: true, ...extra };
    state = st;
    render();
    const q = field(b).value.trim();
    let items = [];
    const persons = list => list.map(p => ({ kind: 'person', p }));
    if (tab === 'search') {
      items = persons(extras(b, q)).concat(persons(await people(b, { q, limit: PAGE + 1 })));
    } else if (tab === 'recent') {
      let list = await people(b, { recent: '1', limit: PAGE + 1 });
      if (!list.length) list = await people(b, { limit: PAGE + 1 });
      items = persons(extras(b, '')).concat(persons(list));
    } else if (tab === 'all') {
      items = persons(extras(b, '')).concat(persons(await people(b, { limit: PAGE + 1 })));
    } else if (tab === 'depts') {
      // a department opened: its sub-departments and only its people; typing searches it with the sub-departments
      depts ??= await get('/people/departments');
      st.dept ??= depts.find(d => !d.parent);
      const d = st.dept;
      if (d && q) {
        items = [{ kind: 'scope', d }].concat(persons(await people(b, { dept: d.id, deep: '1', q, limit: PAGE + 1 })));
      } else if (d) {
        const up = byID(d.parent);
        if (up) items.push({ kind: 'back', d: up });
        if (multi(b) && d.people) items.push({ kind: 'deptall', d });
        items = items.concat(depts.filter(x => x.parent === d.id).map(x => ({ kind: 'dept', d: x })),
          persons(await people(b, { dept: d.id })));
      }
    }
    if (state !== st) return; // another tab or query meanwhile
    const n = items.filter(it => it.kind === 'person' && !it.p.extra).length;
    if (n > PAGE) { items.splice(items.findLastIndex(it => it.kind === 'person'), 1); items.push({ kind: 'more' }); }
    st.items = items;
    st.loading = false;
    st.sel = Math.max(0, items.findIndex(it => it.kind === 'person' || it.kind === 'dept'));
    render();
  };

  const more = async () => {
    const st = state, b = box;
    const shown = st.items.filter(it => it.kind === 'person' && !it.p.extra).length;
    const params = { limit: PAGE + 1, offset: shown };
    if (st.tab === 'search') params.q = field(b).value.trim();
    if (st.tab === 'recent') params.recent = '1';
    if (st.tab === 'depts') Object.assign(params, { dept: st.dept.id, deep: '1', q: field(b).value.trim() });
    const list = await people(b, params);
    if (state !== st) return;
    st.items.pop();
    list.slice(0, PAGE).forEach(p => st.items.push({ kind: 'person', p }));
    if (list.length > PAGE) st.items.push({ kind: 'more' });
    render();
  };

  const activate = async it => {
    const b = box;
    if (!it) return;
    if (it.kind === 'person') {
      if (multi(b) && chosen(b).has(it.p.id)) remove(b, it.p.id);
      else add(b, [it.p]);
      if (!multi(b)) { close(); return; }
      if (state.tab === 'search') show('search');
      else if (state.tab === 'depts' && state.items[0]?.kind === 'scope') show('depts', { dept: state.dept });
      else render();
      field(b).focus();
    } else if (it.kind === 'dept') show('depts', { dept: it.d });
    else if (it.kind === 'back') show('depts', { dept: it.d });
    else if (it.kind === 'scope') show('search');
    else if (it.kind === 'more') more();
    else if (it.kind === 'deptall') addDept(it.d);
  };

  const open = b => {
    if (box === b) return;
    close();
    box = b;
    pop = document.createElement('div');
    pop.className = 'upick-pop';
    pop.setAttribute('role', 'listbox');
    const tabs = document.createElement('div');
    tabs.className = 'upick-tabs';
    const names = (b.dataset.tabs || 'Recent|Departments|All').split('|');
    ['recent', 'depts', 'all'].forEach((t, i) => {
      if (t === 'depts' && ('guest' in document.body.dataset || collab(b))) return; // a collab: no company structure
      const btn = document.createElement('button');
      btn.type = 'button'; btn.className = 'ghost'; btn.dataset.tab = t; btn.textContent = names[i];
      tabs.append(btn);
    });
    const crumbs = document.createElement('div');
    crumbs.className = 'upick-crumbs';
    const list = document.createElement('div');
    list.className = 'upick-list';
    pop.append(tabs, crumbs, list);
    pop.addEventListener('mousedown', e => { // keeps the focus in the field
      e.preventDefault();
      const t = e.target.closest('[data-tab]');
      if (t) { show(t.dataset.tab); return; }
      const c = e.target.closest('[data-crumb]');
      if (c) { show('depts', { dept: byID(c.dataset.crumb) }); return; }
      const row = e.target.closest('[data-i]');
      if (!row) return;
      const it = state.items[Number(row.dataset.i)];
      if (e.target.closest('[data-add]')) addDept(it.d); else activate(it);
    });
    document.body.append(pop);
    window.go24Pop?.(pop, close, b);
    show(field(b).value.trim() ? 'search' : 'recent');
  };

  document.addEventListener('focusin', e => {
    const b = e.target.closest?.('[data-upick]');
    if (b && e.target === field(b)) open(b);
  });
  // a form that adds the one person picked (data-submit): its «+» with no one picked opens the list instead
  document.addEventListener('submit', e => {
    const b = e.target.querySelector?.('[data-upick][data-submit]:not([data-multiple])');
    if (!b || chosen(b).size) return;
    e.preventDefault();
    field(b).focus();
    open(b);
  });
  document.addEventListener('mousedown', e => {
    const b = e.target.closest?.('[data-upick]');
    if (!b) { if (pop && !e.composedPath().includes(pop)) close(); return; } // the row clicked may be re-rendered by now
    if (e.target.closest('[data-rm]')) return;
    if (e.target !== field(b)) { e.preventDefault(); field(b).focus(); }
    open(b);
  });
  document.addEventListener('click', e => {
    const rm = e.target.closest?.('[data-upick] [data-rm]');
    if (!rm) return;
    e.preventDefault();
    const b = rm.closest('[data-upick]');
    remove(b, rm.closest('.upick-chip').dataset.id);
    if (box === b) render();
  });
  document.addEventListener('input', e => {
    const b = e.target.closest?.('[data-upick]');
    if (!b || e.target !== field(b)) return;
    if (box !== b) open(b);
    clearTimeout(timer);
    timer = setTimeout(() => {
      if (state?.tab === 'depts' && state.dept) show('depts', { dept: state.dept }); // search within the department
      else show(field(b).value.trim() ? 'search' : 'recent');
    }, 200);
  });
  document.addEventListener('keydown', e => {
    const b = e.target.closest?.('[data-upick]');
    if (!b || e.target !== field(b)) return;
    if (e.key === 'Backspace' && !field(b).value && !('required' in b.dataset)) {
      const last = [...b.querySelectorAll('.upick-chip')].pop();
      if (last) { remove(b, last.dataset.id); if (box === b) render(); }
      return;
    }
    if (e.key === 'Tab') { close(); return; }
    if (!pop || box !== b) {
      if (e.key === 'ArrowDown') { e.preventDefault(); open(b); }
      return;
    }
    const n = state.items.length;
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      if (n) { state.sel = (state.sel + (e.key === 'ArrowDown' ? 1 : n - 1)) % n; render(); }
    } else if (e.key === 'Enter') {
      e.preventDefault();
      activate(state.items[state.sel]);
    } else if (e.key === 'Escape') {
      e.preventDefault(); e.stopPropagation();
      close();
    }
  });
  addEventListener('resize', place);
  addEventListener('scroll', place, true);

  // chips of people the page could not name (fired, from another list): the server names them
  const missing = [...document.querySelectorAll('[data-upick][data-missing] .upick-chip')].filter(c => !c.textContent.trim());
  if (missing.length) {
    get('/people?ids=' + encodeURIComponent([...new Set(missing.map(c => c.dataset.id))].join(','))).then(list => {
      const byID = new Map(list.map(p => [p.id, p]));
      missing.forEach(c => {
        const p = byID.get(c.dataset.id);
        const name = c.querySelector('.upick-name');
        name.textContent = p ? p.name : c.dataset.id;
        if (p?.guest) name.append(' ', guestMark(c.closest('[data-upick]')));
        if (p && !p.active) c.classList.add('fired');
      });
    });
  }
})();
