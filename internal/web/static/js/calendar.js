// The planner of the meeting form ([data-planner]): the busy time of the organizer and the invited on
// the meeting's day (GET /calendar/busy), who of them is busy at its time, and «Nearest free time», which moves the
// meeting there keeping its length.
(() => {
  const box = document.querySelector('[data-planner]');
  if (!box) return;
  const form = box.closest('form');
  const start = form.querySelector('[name=start]'), end = form.querySelector('[name=end]');
  const state = box.querySelector('.pl-state'), take = box.querySelector('.pl-take'), grid = box.querySelector('.pl-grid');
  const people = () => [box.dataset.owner, ...[...form.querySelectorAll('[data-upick] input[name=invite]')].map(i => i.value)]
    .filter((u, i, all) => u && all.indexOf(u) === i);
  const pad = n => String(n).padStart(2, '0');
  // "2026-10-21T10:00" ↔ a local Date
  const local = d => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
  const parse = s => new Date(s.length === 10 ? s + 'T00:00' : s);
  let timer, seq = 0, free = '';

  const load = async () => {
    if (!start.value || !end.value) return;
    const q = new URLSearchParams();
    people().forEach(u => q.append('user', u));
    q.set('start', start.value.slice(0, 16));
    q.set('end', end.value.slice(0, 16));
    if (box.dataset.except) q.set('except', box.dataset.except);
    const room = form.querySelector('[name=room]')?.value;
    if (room) q.set('room', room);
    const my = ++seq;
    let data;
    try {
      const r = await fetch('/calendar/busy?' + q, { headers: { Accept: 'application/json' } });
      if (!r.ok) return;
      data = await r.json();
    } catch { return; }
    if (my === seq) draw(data);
  };

  // place: the left and the width (in %) of [a, b) on the hour scale of the day
  const place = (d, a, b) => {
    const day = parse(d.Day), h0 = d.Hour0 * 60, span = (d.Hour1 - d.Hour0) * 60;
    const min = t => Math.max(0, Math.min(span, (t - day) / 60000 - h0));
    const l = min(a), r = min(b);
    return r > l ? { left: l / span * 100, width: (r - l) / span * 100 } : null;
  };
  const block = (cls, pos, title) => {
    const el = document.createElement('div');
    el.className = cls;
    el.style.left = pos.left + '%';
    el.style.width = pos.width + '%';
    if (title) el.title = title;
    return el;
  };

  const draw = d => {
    grid.replaceChildren();
    const head = document.createElement('div');
    head.className = 'pl-row pl-hours';
    head.append(document.createElement('span'));
    const scale = document.createElement('div');
    scale.className = 'pl-track';
    for (let h = d.Hour0; h < d.Hour1; h += 2) {
      const t = document.createElement('span');
      t.style.left = (h - d.Hour0) / (d.Hour1 - d.Hour0) * 100 + '%';
      t.textContent = pad(h) + ':00';
      scale.append(t);
    }
    head.append(scale);
    grid.append(head);
    const here = place(d, parse(start.value), parse(end.value));
    const busy = [];
    for (const p of d.People) {
      const row = document.createElement('div');
      row.className = 'pl-row' + (p.Busy ? ' clash' : '');
      const name = document.createElement('span');
      name.className = 'pl-name';
      name.textContent = p.Name;
      const track = document.createElement('div');
      track.className = 'pl-track';
      for (const s of p.Slots) {
        const pos = place(d, parse(s.Start), parse(s.End));
        if (pos) track.append(block('pl-slot ' + s.Kind, pos, s.Title ? `${s.Start.slice(11)}–${s.End.slice(11)} ${s.Title}` : ''));
      }
      if (here) track.append(block('pl-here', here));
      row.append(name, track);
      grid.append(row);
      if (p.Busy) busy.push(p.Name);
    }
    state.classList.toggle('bad', busy.length > 0);
    state.textContent = busy.length ? box.dataset.tBusy.replace('%s', busy.join(', ')) : box.dataset.tFree;
    free = d.Free && !d.FreeIsSame ? d.Free : '';
    take.hidden = !free;
    if (!d.Free && busy.length) state.textContent += ' · ' + box.dataset.tNone;
    if (free) {
      const when = parse(free).toLocaleString(document.documentElement.lang || undefined,
        { weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' });
      take.textContent = box.dataset.tTake.replace('%s', when);
    }
  };

  const later = () => { clearTimeout(timer); timer = setTimeout(load, 250); };
  take.addEventListener('click', () => {
    if (!free) return;
    const length = parse(end.value) - parse(start.value);
    const s = parse(free);
    start.value = local(s);
    end.value = local(new Date(s.getTime() + length));
    load();
  });
  form.addEventListener('change', e => {
    if (e.target === start || e.target === end || e.target.name === 'room' || e.target.closest?.('[data-upick]')) later();
  });
  load();
})();

// Dragging on the calendar ([data-calmove]): in the week and the day a meeting of one's own is moved
// (time and day) or stretched by its lower edge on a quarter-hour grid, and a span chosen on an empty place (or a
// click there) opens a new meeting; in the month a meeting is dragged to another day. A moved occurrence of a repeated
// meeting is taken out of the series alone (POST /calendar/{id}/move).
(() => {
  const pad = n => String(n).padStart(2, '0');
  const local = d => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
  const parse = s => new Date(s.length === 10 ? s + 'T00:00' : s);
  const at = (date, minutes) => { const d = parse(date); d.setMinutes(minutes); return d; };
  const move = async (el, start, end) => {
    const body = new URLSearchParams({ start: local(start), end: local(end) });
    if (el.dataset.at) body.set('at', el.dataset.at);
    try {
      const r = await fetch(`/calendar/${el.dataset.id}/move`, { method: 'POST', headers: { Accept: 'application/json' }, body });
      const j = await r.json().catch(() => ({}));
      if (!r.ok && j.error) alert(j.error);
    } catch { /* the page shows how it is */ }
    location.reload();
  };

  const grid = document.querySelector('.calgrid[data-calmove]:not([data-frozen])');
  if (grid) {
    const h0 = Number(grid.dataset.hour0), px = Number(grid.dataset.hourPx), step = 15;
    const snap = y => Math.round(y / (px * step / 60)) * (px * step / 60); // pixels on the quarter-hour grid
    const minutes = y => h0 * 60 + Math.round(y / px * 60 / step) * step;
    let drag = null, suppress = false;
    grid.querySelectorAll('.cg-ev[data-owner]').forEach(el => el.classList.toggle('movable', el.dataset.owner === grid.dataset.me));
    grid.addEventListener('pointerdown', e => {
      if (e.button !== 0) return;
      const col = e.target.closest('.cg-col');
      if (!col) return;
      const ev = e.target.closest('.cg-ev');
      if (ev) {
        if (ev.dataset.owner !== grid.dataset.me) return;
        const r = ev.getBoundingClientRect();
        drag = { kind: e.clientY > r.bottom - 6 ? 'resize' : 'move', el: ev, col, x0: e.clientX, y0: e.clientY, top: ev.offsetTop, h: ev.offsetHeight };
      } else if (e.target === col) {
        const top = snap(e.clientY - col.getBoundingClientRect().top - px * step / 120);
        drag = { kind: 'new', col, x0: e.clientX, y0: e.clientY, top };
      }
    });
    document.addEventListener('pointermove', e => {
      if (!drag) return;
      const dy = e.clientY - drag.y0;
      if (!drag.moved && Math.abs(dy) < 4 && Math.abs(e.clientX - drag.x0) < 4) return;
      drag.moved = true;
      e.preventDefault();
      if (drag.kind === 'move') {
        const col = document.elementFromPoint?.(e.clientX, e.clientY)?.closest('.cg-col');
        if (col && col !== drag.el.parentElement && grid.contains(col)) col.append(drag.el);
        drag.el.style.top = Math.max(0, drag.top + snap(dy)) + 'px';
      } else if (drag.kind === 'resize') {
        drag.el.style.height = Math.max(snap(px * step / 60), drag.h + snap(dy)) + 'px';
      } else {
        if (!drag.ghost) {
          drag.ghost = document.createElement('div');
          drag.ghost.className = 'calitem cg-ev cg-ghost';
          drag.col.append(drag.ghost);
        }
        const a = Math.min(drag.top, drag.top + snap(dy)), b = Math.max(drag.top, drag.top + snap(dy));
        drag.ghost.style.top = a + 'px';
        drag.ghost.style.height = Math.max(b - a, px * step / 60) + 'px';
      }
    });
    document.addEventListener('pointerup', () => {
      const d = drag;
      drag = null;
      if (!d) return;
      if (d.kind === 'new') {
        const top = d.ghost ? d.ghost.offsetTop : d.top;
        const date = d.col.dataset.date;
        const start = at(date, minutes(top));
        const end = d.ghost ? at(date, minutes(top + d.ghost.offsetHeight)) : new Date(start.getTime() + 3600000);
        const q = new URLSearchParams({ start: local(start), end: local(end) });
        if (grid.dataset.project) q.set('project', grid.dataset.project);
        if (grid.dataset.user) q.set('user', grid.dataset.user); // a colleague's calendar: they are invited
        location.href = '/calendar/new?' + q;
        return;
      }
      if (!d.moved) return; // a click: the meeting opens
      suppress = true;
      const start0 = parse(d.el.dataset.start), end0 = parse(d.el.dataset.end);
      if (d.kind === 'move') {
        const start = at(d.el.parentElement.dataset.date, minutes(d.el.offsetTop));
        move(d.el, start, new Date(start.getTime() + (end0 - start0)));
      } else {
        move(d.el, start0, at(d.el.parentElement.dataset.date, minutes(d.el.offsetTop + d.el.offsetHeight)));
      }
    });
    grid.addEventListener('click', e => { if (suppress) { e.preventDefault(); suppress = false; } }, true);
  }

  const month = document.querySelector('.cal[data-calmove]:not([data-frozen])');
  if (month) {
    let dragged = null;
    month.querySelectorAll('.calitem[data-id][data-owner]').forEach(el => {
      el.draggable = el.dataset.owner === month.dataset.me; // a link is draggable by default
    });
    month.addEventListener('dragstart', e => {
      dragged = e.target.closest?.('.calitem[data-owner]');
      if (dragged && !dragged.draggable) dragged = null;
      if (dragged) e.dataTransfer.setData('text/plain', dragged.dataset.id);
    });
    month.addEventListener('dragover', e => {
      if (!dragged) return;
      const day = e.target.closest?.('.calday');
      if (!day) return;
      e.preventDefault();
      month.querySelectorAll('.calday.drop').forEach(x => x !== day && x.classList.remove('drop'));
      day.classList.add('drop');
    });
    month.addEventListener('drop', e => {
      const day = e.target.closest?.('.calday');
      if (!dragged || !day) return;
      e.preventDefault();
      const start0 = parse(dragged.dataset.start), end0 = parse(dragged.dataset.end);
      const start = parse(day.dataset.date);
      start.setHours(start0.getHours(), start0.getMinutes());
      const el = dragged;
      dragged = null;
      if (start.getTime() !== start0.getTime()) move(el, start, new Date(start.getTime() + (end0 - start0)));
    });
    month.addEventListener('dragend', () => {
      dragged = null;
      month.querySelectorAll('.calday.drop').forEach(x => x.classList.remove('drop'));
    });
  }
})();

// The panel «Календари» ([data-calcals]): the main and one's own calendars, the company calendar, each
// project's one, task deadlines, absences and birthdays are shown or hidden; the choice is remembered in this browser.
(() => {
  const panel = document.querySelector('[data-calcals]');
  if (!panel) return;
  const key = 'go24.calendar.hidden';
  let hidden = [];
  try { hidden = JSON.parse(localStorage.getItem(key) || '[]'); } catch { hidden = []; }
  const seen = new Map();
  document.querySelectorAll('[data-cal^="p:"]').forEach(el => seen.set(el.dataset.cal, el.dataset.calName || el.dataset.cal.slice(2)));
  const add = panel.querySelector('.calsec-add');
  for (const [cal, name] of [...seen].sort((a, b) => a[1].localeCompare(b[1]))) {
    const label = document.createElement('label');
    label.className = 'check';
    const box = document.createElement('input');
    box.type = 'checkbox';
    box.value = cal;
    label.append(box, ' ', name);
    panel.insertBefore(label, add);
  }
  const apply = () => {
    panel.querySelectorAll('input[type=checkbox]').forEach(b => { b.checked = !hidden.includes(b.value); });
    document.querySelectorAll('[data-cal]').forEach(el => {
      const off = hidden.includes(el.dataset.cal);
      el.hidden = off;
      el.closest('.cl-item')?.toggleAttribute('hidden', off);
    });
    document.querySelectorAll('.cl-day').forEach(d => { d.hidden = !d.querySelector('.cl-item:not([hidden])'); });
  };
  panel.addEventListener('change', e => {
    if (e.target.type !== 'checkbox') return;
    const v = e.target.value;
    hidden = e.target.checked ? hidden.filter(x => x !== v) : [...hidden, v];
    try { localStorage.setItem(key, JSON.stringify(hidden)); } catch { /* private mode: for this page only */ }
    apply();
  });
  apply();
})();

// One's own calendars ([data-cal-edit]), as «Добавить календарь»: the dialog adds one (a name and a
// colour) or changes it, or deletes it with its meetings; the main calendar has only its colour (or that of the theme).
(() => {
  const dlg = document.querySelector('[data-cal-dlg]');
  if (!dlg) return;
  const form = dlg.querySelector('form'), del = dlg.querySelector('#cal-sec-del');
  document.addEventListener('click', e => {
    const b = e.target.closest('[data-cal-edit]');
    if (!b) return;
    const id = b.dataset.calEdit, main = id === 'main';
    form.elements.id.value = id;
    form.elements.back.value = location.pathname + location.search;
    form.elements.name.value = b.dataset.name || '';
    form.elements.name.hidden = main;
    form.elements.name.required = !main;
    dlg.querySelector('[data-main-only]').hidden = !main;
    const color = b.dataset.color ?? '';
    const boxes = [...form.querySelectorAll('input[name=color]')];
    (boxes.find(x => x.value === color && (main || x.value)) || boxes.find(x => x.value)).checked = true;
    dlg.querySelector('[data-cal-dlg-title]').textContent = main ? dlg.dataset.tMain : id ? dlg.dataset.tEdit : dlg.dataset.tNew;
    dlg.querySelector('[data-cal-del]').hidden = main || !id;
    del.action = '/calendar/sections/' + encodeURIComponent(id) + '/delete';
    dlg.showModal();
    if (!main) form.elements.name.focus();
  });
  dlg.querySelector('[data-cal-close]').addEventListener('click', () => dlg.close());
})();

// Calendars of colleagues ([data-cal-people]), as «Календари сотрудников»: the dialog adds one to the panel
// (a colleague and a colour) and says what colleagues see in one's own calendar; the forms come back to this page.
(() => {
  document.querySelectorAll('[data-cal-back]').forEach(i => { i.value = location.pathname + location.search; });
  const dlg = document.querySelector('[data-cal-people-dlg]');
  if (!dlg) return;
  document.querySelector('[data-cal-people]')?.addEventListener('click', () => dlg.showModal());
  dlg.querySelector('[data-cal-close]').addEventListener('click', () => dlg.close());
})();

// Reminders of the meeting form ([data-reminds]): «Добавить напоминание» adds one more (15 minutes
// before by default), the cross removes one; none — no reminder.
(() => {
  const box = document.querySelector('[data-reminds]');
  if (!box) return;
  const row = box.querySelector('[data-remind-row]'), add = box.querySelector('[data-remind-add]');
  add.addEventListener('click', () => {
    add.before(row.content.cloneNode(true));
    add.previousElementSibling.querySelector('select').focus();
  });
  box.addEventListener('click', e => e.target.closest('[data-remind-del]')?.closest('.remind').remove());
})();

// The meeting's time zone ([data-ev-tz]): a meeting of whole days has none.
(() => {
  const tz = document.querySelector('[data-ev-tz]'), day = tz?.form.querySelector('[name="all_day"]');
  day?.addEventListener('change', () => { tz.hidden = day.checked; });
})();

// «CRM» of the meeting form ([data-ev-crm]): deals, leads, contacts and companies found by typing
// (/mail/crm/search) become chips the form sends (crm=kind:id); the cross unlinks one.
(() => {
  const box = document.querySelector('[data-ev-crm]');
  if (!box) return;
  const q = box.querySelector('[data-ev-crm-q]'), found = box.querySelector('[data-ev-crm-res]'), chips = box.querySelector('[data-ev-crm-chips]');
  let timer = 0, seq = 0;
  const search = async () => {
    const text = q.value.trim(), n = ++seq;
    if (text.length < 2) { found.replaceChildren(); return; }
    let list = [];
    try {
      const res = await fetch('/mail/crm/search?q=' + encodeURIComponent(text), { headers: { Accept: 'application/json' } });
      if (res.ok) list = await res.json();
    } catch { list = []; }
    if (n !== seq) return; // a later search is on its way
    found.replaceChildren(...list.map(it => {
      const b = document.createElement('button');
      b.type = 'button';
      b.className = 'ghost small';
      b.setAttribute('role', 'option');
      b.dataset.value = it.kind + ':' + it.path.split('/').pop();
      b.dataset.path = it.path;
      b.dataset.label = it.kind_name + ': ' + it.title;
      b.textContent = b.dataset.label + (it.sub ? ' · ' + it.sub : '');
      return b;
    }));
  };
  q.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(search, 250); });
  q.addEventListener('keydown', e => { // Enter picks the first card found, not sends the form
    if (e.key !== 'Enter') return;
    e.preventDefault();
    found.querySelector('button')?.click();
  });
  found.addEventListener('click', e => {
    const b = e.target.closest('button[data-value]');
    if (!b) return;
    if (![...chips.querySelectorAll('input')].some(i => i.value === b.dataset.value)) {
      const chip = document.createElement('span');
      chip.className = 'pill bk-chip';
      const v = document.createElement('input');
      v.type = 'hidden';
      v.name = 'crm';
      v.value = b.dataset.value;
      const a = document.createElement('a');
      a.href = b.dataset.path;
      a.target = '_blank';
      a.textContent = b.dataset.label;
      const x = document.createElement('button');
      x.type = 'button';
      x.className = 'ghost small';
      x.dataset.unbind = '';
      x.title = box.dataset.unbind;
      x.textContent = '×';
      chip.append(v, a, x);
      chips.append(chip);
    }
    q.value = '';
    found.replaceChildren();
  });
  chips.addEventListener('click', e => e.target.closest('[data-unbind]')?.closest('.bk-chip').remove());
})();

// The meeting's repetition: the days of a weekly one, its end — never, after N meetings, on a day.
(() => {
  const box = document.querySelector('[data-ev-repeat]');
  if (!box) return;
  const repeat = box.closest('form').querySelector('[name="repeat"]'), ends = box.querySelector('[data-ev-ends]');
  const show = () => {
    box.hidden = !repeat.value;
    box.querySelector('[data-ev-days]').hidden = repeat.value !== 'weekly';
    box.querySelectorAll('[data-ev-end]').forEach(x => { x.hidden = x.dataset.evEnd !== ends.value; });
  };
  repeat.addEventListener('change', show);
  ends.addEventListener('change', show);
})();
