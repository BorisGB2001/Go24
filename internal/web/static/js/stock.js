// «Складской учёт»: the rows of a stock document — picking a product sets its unit and (if none is typed) its
// purchase price; «Добавить строку» adds an empty row (a copy of the last one, cleared); × removes a row.
// [data-autosubmit] fields of the filters send their form at once.
(() => {
  const clear = (tr) => {
    tr.querySelectorAll('input').forEach(i => { i.value = i.name === 'quantity' ? '1' : ''; });
    tr.querySelectorAll('select[name="product"]').forEach(s => { s.selectedIndex = 0; });
    tr.querySelectorAll('[data-unit], [data-sum]').forEach(el => { el.textContent = ''; });
  };
  document.addEventListener('change', e => {
    if (e.target.matches?.('[data-autosubmit]')) { e.target.form.submit(); return; }
    const pick = e.target.closest?.('[data-stockdoc] [data-pick]');
    if (!pick) return;
    const opt = pick.selectedOptions[0], tr = pick.closest('tr');
    const unit = tr.querySelector('[data-unit]'), price = tr.querySelector('[name="price"]');
    if (unit) unit.textContent = opt.value ? opt.dataset.unit : '';
    if (price && opt.value && !price.value) price.value = opt.dataset.price || '';
  });
  // a barcode typed or scanned (a scanner types it and presses Enter): the product's row gets one more, or the empty
  // row (a new one, if none) takes the product
  document.addEventListener('keydown', e => {
    const scan = e.target.closest?.('[data-stockdoc] [data-barcode]');
    if (!scan || e.key !== 'Enter') return;
    e.preventDefault();
    const form = scan.closest('[data-stockdoc]'), code = scan.value.trim();
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
    const form = e.target.closest?.('[data-stockdoc]');
    if (!form) return;
    if (e.target.closest('[data-addrow]')) {
      const rows = form.querySelectorAll('tr[data-row]'), last = rows[rows.length - 1];
      const tr = last.cloneNode(true);
      clear(tr);
      tr.querySelectorAll('select:not([name="product"])').forEach((s, i) => { s.value = last.querySelectorAll('select:not([name="product"])')[i].value; });
      last.after(tr);
      tr.querySelector('[name="product"]').focus();
    } else if (e.target.closest('[data-delrow]')) {
      const tr = e.target.closest('tr');
      if (form.querySelectorAll('tr[data-row]').length > 1) tr.remove(); else clear(tr);
    }
  });
})();
