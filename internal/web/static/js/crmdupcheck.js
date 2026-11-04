// «Контроль дубликатов» while a CRM form is filled (form[data-dupcheck="lead|contact|company"],
// data-dup-id — the card being changed, data-dup-only="inn" — a requisite of it): a moment after a phone, an e-mail,
// the name, a company's title or the ИНН is typed, the bar «Найдены совпадения: N» lists the alike cards the viewer may read (/crm/dupcheck); each opens in a new
// tab. Loaded by several parts of a page — it listens once, on the document.
(() => {
  if (window.go24DupCheck) return;
  window.go24DupCheck = true;
  const timers = new WeakMap(), asked = new WeakMap();
  // the fields of each kind of form: a person's name and a company's title
  const named = { lead: { name: 'name', company: 'company_title' }, contact: { name: 'name' }, company: { company: 'title' } };
  const query = form => {
    const entity = form.dataset.dupcheck, q = new URLSearchParams({ entity });
    if (form.dataset.dupId) q.set('id', form.dataset.dupId);
    const inn = form.elements.inn?.value?.trim();
    if (inn) q.set('inn', inn);
    if (form.dataset.dupOnly === 'inn') return q; // a requisite of the card: its ИНН only
    for (const [k, field] of Object.entries(named[entity] || {})) {
      const v = form.elements[field]?.value?.trim();
      if (v) q.set(k, v);
    }
    form.querySelectorAll('input[type=tel], input[name=phone]').forEach(i => i.value.trim() && q.append('phone', i.value.trim()));
    form.querySelectorAll('input[type=email], input[name=email]').forEach(i => i.value.trim() && q.append('email', i.value.trim()));
    return q;
  };
  const bar = form => {
    let box = form.querySelector(':scope .dupcheck');
    if (!box) {
      box = document.createElement('div');
      box.className = 'dupcheck';
      box.hidden = true;
      const submit = [...form.querySelectorAll('button')].filter(b => b.type === 'submit').pop();
      const at = submit && submit.parentElement !== form ? submit.parentElement : submit;
      if (at) at.before(box); else form.append(box);
    }
    return box;
  };
  const show = (form, res) => {
    const box = bar(form);
    box.replaceChildren();
    box.hidden = !res.count;
    if (!res.count) return;
    const head = document.createElement('div');
    head.className = 'dupcheck-head';
    head.textContent = '⚠ ' + res.title;
    const list = document.createElement('ul');
    for (const it of res.items) {
      const li = document.createElement('li'), a = document.createElement('a'), info = document.createElement('span');
      a.href = it.URL;
      a.target = '_blank';
      a.rel = 'noopener';
      a.textContent = it.Title;
      info.className = 'muted small';
      info.textContent = ' ' + [it.Kind, it.By, it.Responsible].filter(Boolean).join(' · ');
      li.append(a, info);
      list.append(li);
    }
    box.append(head, list);
  };
  const check = async form => {
    const q = query(form), key = q.toString();
    if (asked.get(form) === key) return;
    asked.set(form, key);
    if ([...q.keys()].every(k => k === 'entity' || k === 'id')) return show(form, { count: 0 });
    try {
      const r = await fetch('/crm/dupcheck?' + key, { headers: { Accept: 'application/json' } });
      if (!r.ok || asked.get(form) !== key) return; // a later check is on its way
      show(form, await r.json());
    } catch { /* offline: no bar */ }
  };
  const later = e => {
    const form = e.target.closest?.('form[data-dupcheck]');
    if (!form || !e.target.name) return;
    clearTimeout(timers.get(form));
    timers.set(form, setTimeout(() => check(form), e.type === 'change' ? 0 : 500));
  };
  document.addEventListener('input', later);
  document.addEventListener('change', later);
})();
