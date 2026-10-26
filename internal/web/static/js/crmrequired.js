// «Обязательные поля» of the CRM: moving a deal or a lead to a stage where an own field it lacks is
// required, the server refuses and names those fields; the page asks for them in a dialog and moves the card again
// with them. window.crmRequired(fields) gives the values typed (field id → value), null when cancelled.
// The card's stage bar (form[data-crmstage]) is sent here in the background; the kanban (boards.js) asks the same way.
(() => {
  const l = (name) => document.querySelector('[data-crmreq]')?.dataset[name] || '';

  const input = (f) => {
    let el;
    if (f.kind === 'list') {
      el = document.createElement('select');
      el.append(new Option('', ''), ...f.options.map(o => new Option(o, o, false, o === f.value)));
    } else {
      el = document.createElement('input');
      el.type = { date: 'date', url: 'url' }[f.kind] || 'text';
      if (f.kind === 'number') el.inputMode = 'decimal';
      el.maxLength = 2000;
      el.value = f.value || '';
    }
    el.name = 'f_' + f.id; el.required = true;
    return el;
  };

  window.crmRequired = (fields) => new Promise(resolve => {
    const dlg = document.createElement('dialog');
    dlg.className = 'crm-dlg';
    const form = document.createElement('form');
    form.method = 'dialog'; form.dataset.native = '';
    const h = document.createElement('h3');
    h.textContent = l('title');
    form.append(h);
    fields.forEach(f => {
      const lab = document.createElement('label');
      lab.textContent = f.name + ' ';
      const req = document.createElement('span');
      req.className = 'req'; req.textContent = '*';
      lab.append(req);
      form.append(lab, input(f));
    });
    const row = document.createElement('p');
    row.className = 'row';
    const ok = document.createElement('button');
    ok.className = 'primary'; ok.textContent = l('save');
    const cancel = document.createElement('button');
    cancel.type = 'button'; cancel.className = 'ghost'; cancel.textContent = l('cancel');
    row.append(ok, cancel);
    form.append(row);
    dlg.append(form);
    document.body.append(dlg);

    let values = null;
    form.addEventListener('submit', () => { values = Object.fromEntries(new FormData(form)); });
    cancel.addEventListener('click', () => dlg.close());
    dlg.addEventListener('close', () => { dlg.remove(); resolve(values); });
    dlg.showModal();
    form.querySelector('input, select')?.focus();
  });

  const post = async (url, params) => {
    try {
      const res = await fetch(url, { method: 'POST', headers: { Accept: 'application/json' }, body: new URLSearchParams(params) });
      return { ok: res.ok, data: res.status === 204 ? {} : await res.json().catch(() => ({})) };
    } catch {
      return { ok: false, data: {} };
    }
  };

  const fail = (form, text) => {
    let el = document.querySelector('.content .flash');
    if (!el) {
      el = document.createElement('div');
      el.className = 'flash';
      document.querySelector('.content').prepend(el);
    }
    el.textContent = text || form.closest('[data-failed]')?.dataset.failed || document.documentElement.dataset.failed || 'Error';
  };

  document.addEventListener('submit', async e => {
    const form = e.target.closest?.('form[data-crmstage]');
    if (!form) return;
    e.preventDefault();
    const params = Object.fromEntries(new FormData(form));
    let { ok, data } = await post(form.action, params);
    if (!ok && data.fields) {
      const values = await window.crmRequired(data.fields);
      if (!values) return;
      ({ ok, data } = await post(form.action, { ...params, ...values }));
    }
    if (ok) location.reload(); else fail(form, data.error);
  });
})();
