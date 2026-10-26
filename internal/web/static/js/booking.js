// «Онлайн-запись»: a click on a free time of a resource opens a new booking at that slot, a click on a booking — its
// card in the same dialog (save, change the status, delete). The client's CRM cards (contact, company, deal) are
// found by typing (/mail/crm/search) and shown as chips; «Записать» from a CRM card (?crm=) brings its client to every
// new booking, ?b=id opens that booking. A booking may take other resources too (extra). The waiting list above the
// grid: a client is dragged onto a free time (booked there for a slot of the resource) or booked by «Записать» in
// the dialog; «В лист ожидания» puts a booking's client back. The services of the resource (svc): checking them sets
// the booking's minutes to theirs together and shows the total.
(() => {
  const grid = document.querySelector('[data-booking]');
  const dlg = document.querySelector('[data-booking-dlg]');
  if (!grid || !dlg) return;
  const form = dlg.querySelector('form');
  const f = name => form.elements[name];
  const hour0 = +grid.dataset.hour0, hourPx = +grid.dataset.hourPx;
  const hhmm = m => String(Math.floor(m / 60)).padStart(2, '0') + ':' + String(m % 60).padStart(2, '0');
  const title = dlg.querySelector('h3'), by = dlg.querySelector('[data-booking-by]'), del = dlg.querySelector('[data-booking-delete]');
  const rating = dlg.querySelector('[data-booking-rating]');
  const deal = dlg.querySelector('[data-booking-deal]'), crm = dlg.querySelector('[data-bk-crm]');
  const toWait = dlg.querySelector('[data-booking-towait]'), extras = [...form.querySelectorAll('[name=extra]')];
  // the resource of the booking is not one of its others
  const syncExtras = () => extras.forEach(x => {
    const own = x.value === f('resource').value;
    if (own) x.checked = false;
    x.disabled = own;
    x.closest('label').hidden = own;
  });
  f('resource').addEventListener('change', syncExtras);
  const svcBox = form.querySelector('[data-bk-svcs]'), svcs = [...form.querySelectorAll('[name=svc]')];
  const total = form.querySelector('[data-bk-total]');
  // as the server's money: «3 500», «1 500,50»
  const fmt = { format: v => new Intl.NumberFormat(document.documentElement.lang || undefined,
    { minimumFractionDigits: Number.isInteger(v) ? 0 : 2, maximumFractionDigits: 2 }).format(v) };
  const showTotal = () => {
    const on = svcs.filter(x => x.checked && !x.disabled);
    const sum = on.reduce((a, x) => a + +x.dataset.price, 0);
    total.textContent = on.length ? total.dataset.label + ': ' + fmt.format(sum) + ' ' + on[0].dataset.currency : '';
    return on;
  };
  // only the services of the booking's resource are offered
  const syncSvcs = () => {
    let any = false;
    svcs.forEach(x => {
      const mine = x.closest('[data-svc-res]').dataset.svcRes === f('resource').value;
      if (!mine) x.checked = false;
      x.disabled = !mine;
      x.closest('label').hidden = !mine;
      any ||= mine;
    });
    if (svcBox) svcBox.hidden = !any;
    if (total) showTotal();
  };
  f('resource').addEventListener('change', syncSvcs);
  svcs.forEach(x => x.addEventListener('change', () => {
    const mins = showTotal().reduce((a, y) => a + +y.dataset.minutes, 0);
    if (mins) f('minutes').value = mins;
  }));
  const chips = crm?.querySelector('[data-bk-chips]'), found = crm?.querySelector('[data-bk-crm-res]'), q = crm?.querySelector('[data-bk-crm-q]');
  const kinds = ['contact', 'company', 'deal'];
  const paths = { contacts: 'contact', companies: 'company', deals: 'deal' };
  const names = {};
  const drawChips = () => {
    if (!chips) return;
    chips.replaceChildren(...kinds.filter(k => f(k + '_id').value).map(k => {
      const chip = document.createElement('span');
      chip.className = 'pill bk-chip';
      const a = document.createElement('a');
      a.href = '/crm/' + Object.keys(paths).find(p => paths[p] === k) + '/' + f(k + '_id').value;
      a.target = '_blank';
      a.textContent = crm.dataset['kind' + k[0].toUpperCase() + k.slice(1)] + ': ' + (names[k] || '…');
      const x = document.createElement('button');
      x.type = 'button';
      x.className = 'ghost small';
      x.textContent = '×';
      x.title = crm.dataset.unbind;
      x.dataset.unbind = k;
      chip.append(a, x);
      return chip;
    }));
  };
  // bind sets the cards from a booking's or the preset's dataset (contactId, contact, …)
  const bind = (ds, prefix) => {
    if (!crm) return;
    for (const k of kinds) {
      const key = prefix ? prefix + k[0].toUpperCase() + k.slice(1) : k;
      f(k + '_id').value = ds[key + 'Id'] || '';
      names[k] = ds[key] || '';
    }
    drawChips();
    found.replaceChildren();
  };
  const preset = dlg.dataset.presetBack !== undefined;
  // open: a booking b (its column col), or a new one at minutes of col; w — a client of the waiting list
  const open = (b, col, minutes, w) => {
    form.reset();
    form.querySelector('[data-error]')?.setAttribute('hidden', '');
    const res = b?.dataset.res || w?.dataset.res || col?.dataset.resource || f('resource').value;
    f('resource').value = res;
    const slot = +(col?.dataset.slot || f('resource').selectedOptions[0]?.dataset.slot || 30);
    const extra = (b?.dataset.extras || '').split(',');
    extras.forEach(x => { x.checked = !!b && extra.includes(x.value); });
    syncExtras();
    const own = (b?.dataset.services || '').split(',');
    svcs.forEach(x => { x.checked = !!b && own.includes(x.value); });
    syncSvcs();
    if (f('wait')) {
      f('wait').value = w?.dataset.wait || '';
      f('wait').disabled = !w;
    }
    if (b) {
      form.action = '/booking/item/' + b.dataset.bk;
      f('time').value = b.dataset.time;
      f('minutes').value = b.dataset.minutes;
      f('client_name').value = b.dataset.name;
      f('client_phone').value = b.dataset.phone;
      f('client_email').value = b.dataset.email;
      f('note').value = b.dataset.note;
      f('status').value = b.dataset.status;
      title.textContent = title.dataset.titleEdit;
      by.textContent = b.dataset.by ? by.dataset.label + ': ' + b.dataset.by : '';
      if (rating) { // the client's rating of the visit
        const n = +(b.dataset.rating || 0);
        rating.hidden = !n;
        rating.textContent = n ? rating.dataset.label + ': ' + '★'.repeat(n) + '☆'.repeat(5 - n) + (b.dataset.ratingNote ? ' — ' + b.dataset.ratingNote : '') : '';
      }
      del.hidden = false;
      del.setAttribute('formaction', '/booking/item/' + b.dataset.bk + '/delete');
      if (toWait) {
        toWait.hidden = false;
        toWait.setAttribute('formaction', '/booking/item/' + b.dataset.bk + '/wait');
      }
      bind(b.dataset);
      if (deal) {
        deal.hidden = !!b.dataset.dealId;
        deal.setAttribute('formaction', '/booking/item/' + b.dataset.bk + '/deal');
      }
      if (crm) f('crm_back').disabled = true;
    } else {
      form.action = form.dataset.newAction;
      f('time').value = hhmm(minutes ?? +(col?.dataset.from || 540));
      f('minutes').value = slot;
      title.textContent = title.dataset.titleNew;
      by.textContent = '';
      if (rating) rating.hidden = true;
      del.hidden = true;
      if (toWait) toWait.hidden = true;
      if (deal) deal.hidden = true;
      if (w) {
        f('client_name').value = w.dataset.name;
        f('client_phone').value = w.dataset.phone;
        f('client_email').value = w.dataset.email;
        f('note').value = w.dataset.note;
        bind(w.dataset);
        if (crm) f('crm_back').disabled = true;
      } else if (preset) {
        f('client_name').value = dlg.dataset.presetName;
        f('client_phone').value = dlg.dataset.presetPhone;
        f('client_email').value = dlg.dataset.presetEmail;
        bind(dlg.dataset, 'preset');
        f('crm_back').value = dlg.dataset.presetBack;
        f('crm_back').disabled = false;
      } else {
        bind({});
      }
    }
    dlg.showModal ? dlg.showModal() : dlg.setAttribute('open', '');
    f('client_name').focus();
  };
  // slotAt: the start (minutes of the day) of the slot of col under the pointer, by the resource's own slot size from
  // the start of its work; null — not a working time or its break
  const slotAt = (col, clientY) => {
    if (!col || col.classList.contains('off')) return null;
    const y = clientY - col.getBoundingClientRect().top;
    const from = +col.dataset.from, to = +col.dataset.to, slot = +col.dataset.slot;
    const at = hour0 * 60 + y / hourPx * 60;
    if (at < from || at >= to) return null;
    const m = Math.min(from + Math.floor((at - from) / slot) * slot, to - slot);
    const b0 = +col.dataset.breakFrom, b1 = +col.dataset.breakTo;
    return b0 < b1 && m < b1 && m + slot > b0 ? null : m;
  };
  grid.addEventListener('click', e => {
    const b = e.target.closest('[data-bk]');
    if (b) { e.preventDefault(); open(b, b.closest('[data-resource]')); return; }
    const col = e.target.closest('[data-resource]'), at = slotAt(col, e.clientY);
    if (at !== null) open(null, col, at);
  });
  // the waiting list: dragged onto a free time — booked there; «Записать» — the dialog with the client
  const waitBox = document.querySelector('[data-bk-wait]');
  if (waitBox) {
    const drop = waitBox.querySelector('[data-wait-drop]');
    let dragging = null;
    waitBox.addEventListener('dragstart', e => {
      dragging = e.target.closest?.('[data-wait]');
      if (!dragging) return;
      e.dataTransfer?.setData('text/plain', dragging.dataset.name || dragging.dataset.phone);
      if (e.dataTransfer) e.dataTransfer.effectAllowed = 'move';
      grid.classList.add('bk-dragging');
    });
    waitBox.addEventListener('dragend', () => {
      dragging = null;
      grid.classList.remove('bk-dragging');
      grid.querySelectorAll('.bk-drop').forEach(c => c.classList.remove('bk-drop'));
    });
    grid.addEventListener('dragover', e => {
      const col = e.target.closest('[data-resource]');
      grid.querySelectorAll('.bk-drop').forEach(c => c !== col && c.classList.remove('bk-drop'));
      if (!dragging || slotAt(col, e.clientY) === null) return;
      e.preventDefault(); // a drop is allowed here
      col.classList.add('bk-drop');
    });
    grid.addEventListener('drop', e => {
      const col = e.target.closest('[data-resource]'), at = slotAt(col, e.clientY);
      if (!dragging || at === null) return;
      e.preventDefault();
      drop.action = '/booking/wait/' + dragging.dataset.wait + '/book';
      drop.elements.resource.value = col.dataset.resource;
      drop.elements.time.value = hhmm(at);
      drop.submit();
    });
    waitBox.addEventListener('click', e => {
      const w = e.target.closest('[data-wait]');
      if (w && e.target.closest('[data-wait-book]')) {
        open(null, grid.querySelector('[data-resource="' + CSS.escape(w.dataset.res) + '"]:not(.off)'), undefined, w);
      }
    });
    // a client put on the list or changed
    const wdlg = document.querySelector('[data-wait-dlg]'), wform = wdlg?.querySelector('form');
    if (wform) {
      const wtitle = wform.querySelector('h3');
      const openWait = w => {
        wform.reset();
        wform.action = w ? '/booking/wait/' + w.dataset.wait : wform.dataset.newAction;
        wtitle.textContent = w ? wtitle.dataset.titleEdit : wtitle.dataset.titleNew;
        if (w) {
          for (const [k, v] of [['client_name', 'name'], ['client_phone', 'phone'], ['client_email', 'email'], ['note', 'note'], ['resource', 'res']]) {
            wform.elements[k].value = w.dataset[v] || '';
          }
        }
        wdlg.showModal ? wdlg.showModal() : wdlg.setAttribute('open', '');
        wform.elements.client_name.focus();
      };
      waitBox.querySelector('[data-wait-new]')?.addEventListener('click', () => openWait(null));
      waitBox.addEventListener('click', e => {
        const a = e.target.closest('[data-wait-edit]');
        if (a) { e.preventDefault(); openWait(a.closest('[data-wait]')); }
      });
      wdlg.querySelector('[data-wait-close]').addEventListener('click', () => wdlg.close ? wdlg.close() : wdlg.removeAttribute('open'));
    }
  }
  document.querySelector('[data-booking-new]')?.addEventListener('click', () => open(null, null));
  if (crm) {
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
      found.replaceChildren(...list.filter(it => kinds.includes(it.kind)).map(it => {
        const b = document.createElement('button');
        b.type = 'button';
        b.className = 'ghost small';
        b.setAttribute('role', 'option');
        b.dataset.kind = it.kind;
        b.dataset.id = it.path.split('/').pop();
        b.dataset.title = it.title;
        b.textContent = it.kind_name + ': ' + it.title + (it.sub ? ' · ' + it.sub : '');
        return b;
      }));
    };
    q.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(search, 250); });
    q.addEventListener('keydown', e => { // Enter picks the first card found, not sends the booking
      if (e.key !== 'Enter') return;
      e.preventDefault();
      found.querySelector('button')?.click();
    });
    found.addEventListener('click', e => {
      const b = e.target.closest('button[data-kind]');
      if (!b) return;
      f(b.dataset.kind + '_id').value = b.dataset.id;
      names[b.dataset.kind] = b.dataset.title;
      if (b.dataset.kind === 'contact' && !f('client_name').value) f('client_name').value = b.dataset.title;
      q.value = '';
      found.replaceChildren();
      drawChips();
    });
    chips.addEventListener('click', e => {
      const x = e.target.closest('button[data-unbind]');
      if (!x) return;
      f(x.dataset.unbind + '_id').value = '';
      drawChips();
    });
  }
  if (grid.dataset.open) grid.querySelector('[data-bk="' + CSS.escape(grid.dataset.open) + '"]')?.click();
  dlg.querySelector('[data-booking-close]').addEventListener('click', () => dlg.close ? dlg.close() : dlg.removeAttribute('open'));
})();
