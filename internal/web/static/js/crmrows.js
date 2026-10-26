// CRM «Товары»: the product rows of a deal or a lead. Picking a catalog product fills the row; «Добавить строку»
// adds an empty row (a copy of the last one, cleared; a deal's warehouse — the default one); × removes a row. Saving sends the whole table.
(() => {
  const clear = (tr) => {
    tr.querySelectorAll('input').forEach(i => { i.value = i.name === 'quantity' ? '1' : ''; });
    tr.querySelectorAll('select').forEach(s => { if (s.dataset.default) s.value = s.dataset.default; else s.selectedIndex = 0; });
    const sum = tr.querySelector('td.nowrap');
    if (sum) sum.textContent = '';
    tr.querySelectorAll('[data-free]').forEach(td => { td.textContent = ''; });
    tr.removeAttribute('data-service');
  };
  document.addEventListener('change', e => {
    const pick = e.target.closest?.('[data-crmrows] [data-pick]');
    if (!pick) return;
    const opt = pick.selectedOptions[0], tr = pick.closest('tr');
    const set = (name, v) => { const el = tr.querySelector(`[name="${name}"]`); if (el) el.value = v; };
    set('product', opt.value);
    tr.toggleAttribute('data-service', !!opt.dataset.service); // a service isn't kept on a warehouse: no reserve
    if (!opt.value) return;
    set('name', opt.dataset.name);
    set('price', opt.dataset.price);
    set('unit', opt.dataset.unit);
    set('vat', opt.dataset.vat);
    set('vat_included', opt.dataset.inc);
  });
  document.addEventListener('input', e => { // a name typed by hand is a free line, not the catalog product
    if (e.target.name !== 'name' || !e.target.closest('[data-crmrows]')) return;
    const tr = e.target.closest('tr'), product = tr.querySelector('[name="product"]'), pick = tr.querySelector('[data-pick]');
    if (pick && product.value && pick.selectedOptions[0]?.dataset.name !== e.target.value) { product.value = ''; pick.selectedIndex = 0; tr.removeAttribute('data-service'); }
  });
  // a barcode typed or scanned (a scanner types it and presses Enter): the product's row gets one more, or the empty
  // row (a new one, if none) takes the product
  document.addEventListener('keydown', e => {
    const scan = e.target.closest?.('[data-crmrows] [data-barcode]');
    if (!scan || e.key !== 'Enter') return;
    e.preventDefault();
    const form = scan.closest('[data-crmrows]'), code = scan.value.trim();
    if (!code) return;
    const opt = [...form.querySelector('[data-pick]')?.options || []].find(o => o.dataset.barcodes?.split(' ').includes(code));
    scan.setCustomValidity(opt ? '' : scan.dataset.unknown);
    if (!opt) { scan.reportValidity(); return; }
    const rows = () => [...form.querySelectorAll('tr[data-row]')];
    let tr = rows().find(r => r.querySelector('[name="product"]').value === opt.value);
    if (tr) {
      const q = tr.querySelector('[name="quantity"]');
      q.value = String((parseFloat(q.value.replace(/\s/g, '').replace(',', '.')) || 0) + 1);
    } else {
      tr = rows().find(r => !r.querySelector('[name="product"]').value && r.querySelector('[data-pick]'));
      if (!tr) { form.querySelector('[data-addrow]').click(); tr = rows().pop(); }
      const pick = tr.querySelector('[data-pick]');
      pick.value = opt.value;
      pick.dispatchEvent(new Event('change', { bubbles: true }));
    }
    scan.value = '';
    scan.focus();
  });
  document.addEventListener('input', e => { if (e.target.matches?.('[data-barcode]')) e.target.setCustomValidity(''); });
  document.addEventListener('click', e => {
    const form = e.target.closest?.('[data-crmrows]');
    if (!form) return;
    if (e.target.closest('[data-addrow]')) {
      const rows = form.querySelectorAll('tr[data-row]'), last = rows[rows.length - 1];
      const tr = last.cloneNode(true);
      clear(tr);
      last.after(tr);
      tr.querySelector('[name="name"]').focus();
    } else if (e.target.closest('[data-delrow]')) {
      const tr = e.target.closest('tr');
      if (form.querySelectorAll('tr[data-row]').length > 1) tr.remove(); else clear(tr);
    }
  });
})();

// «Контакты» of a deal's form: «Добавить контакт» adds one more empty contact choice.
document.addEventListener('click', e => {
  const add = e.target.closest?.('[data-crmcontacts] [data-addcontact]');
  if (!add) return;
  const sel = add.parentElement.querySelector('select').cloneNode(true);
  sel.selectedIndex = 0;
  add.before(sel);
  sel.focus();
});

// The CRM list: checking rows (or all), the group action with its value (a stage or a responsible).
(() => {
  const sync = (form) => {
    const n = form.querySelectorAll('tbody input[name="id"]:checked').length;
    form.querySelector('[data-bulkcount]').textContent = n;
    const op = form.querySelector('[data-bulkop]').value;
    form.querySelectorAll('[data-bulkfor]').forEach(s => { s.disabled = s.dataset.bulkfor !== op; s.hidden = s.disabled; });
  };
  document.addEventListener('change', e => {
    const form = e.target.closest?.('[data-crmbulk]');
    if (!form) return;
    if (e.target.matches('[data-checkall]')) form.querySelectorAll('tbody input[name="id"]').forEach(c => { c.checked = e.target.checked; });
    sync(form);
  });
  document.addEventListener('submit', e => {
    const form = e.target.closest?.('[data-crmbulk]');
    if (!form) return;
    const btn = form.querySelector('[data-confirmdel]');
    if (!form.querySelector('tbody input[name="id"]:checked')) e.preventDefault();
    else if (form.querySelector('[data-bulkop]').value === 'delete' && !confirm(btn.dataset.confirmdel)) e.preventDefault();
  });
  document.querySelectorAll('[data-crmbulk]').forEach(f => f.querySelector('[data-bulkop]') && sync(f));
})();

// A quote's or an invoice's print page prints itself.
if (document.body?.hasAttribute('data-crm-print')) {
  document.querySelector('[data-print-now]')?.addEventListener('click', () => print());
  addEventListener('load', () => setTimeout(() => print(), 300));
}
