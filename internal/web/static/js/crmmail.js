// «Письмо» of a CRM card ([data-crmmail], parts/crm.html «crmmail»): a template chosen puts its subject and text,
// filled with the card's fields by the server, into the letter. «Отправить» of a card's document ([data-crmdoc-send])
// opens the letter with the document attached (PDF when the server makes PDFs, or DOCX: doc=ID:pdf|docx).
(() => {
  document.addEventListener('click', e => {
    const x = e.target.closest?.('[data-crmdoc-remove]');
    if (x) {
      const box = x.closest('[data-crmmail-docs]');
      x.closest('.chip').remove();
      box.hidden = !box.children.length;
      return;
    }
    const b = e.target.closest?.('[data-crmdoc-send]');
    const details = document.querySelector('[data-crmmail]');
    if (!b || !details) return;
    const form = details.querySelector('form');
    const box = form.querySelector('[data-crmmail-docs]');
    const id = b.dataset.crmdocSend;
    if (!box.querySelector(`[data-doc="${CSS.escape(id)}"]`)) {
      const chip = document.createElement('span');
      chip.className = 'chip';
      chip.dataset.doc = id;
      const name = b.dataset.name.replace(/\.docx$/i, '');
      const input = Object.assign(document.createElement('input'), { type: 'hidden', name: 'doc', value: `${id}:${'pdf' in b.dataset ? 'pdf' : 'docx'}` });
      chip.append('📎 ', name, input);
      if ('pdf' in b.dataset) {
        const sel = document.createElement('select');
        for (const f of ['pdf', 'docx']) sel.append(new Option(f.toUpperCase(), f));
        sel.addEventListener('change', () => { input.value = `${id}:${sel.value}`; });
        chip.append(' ', sel);
      }
      const rm = Object.assign(document.createElement('button'), { type: 'button', className: 'ghost small', textContent: '×' });
      rm.dataset.crmdocRemove = '';
      chip.append(rm);
      box.append(chip);
      box.hidden = false;
      const subject = form.querySelector('[data-crmmail-subject]');
      if (!subject.value) subject.value = name;
    }
    details.open = true;
    details.scrollIntoView({ block: 'start', behavior: 'smooth' });
  });

  document.addEventListener('change', async e => {
    const sel = e.target.closest?.('[data-crmmail-tpl]');
    const box = sel?.closest('[data-crmmail]');
    if (!box || !sel.value) return;
    const form = box.querySelector('form');
    try {
      const res = await fetch(`${box.dataset.crmmail}/mail-template/${encodeURIComponent(sel.value)}`, { headers: { Accept: 'application/json' } });
      const data = await res.json();
      if (!res.ok) throw new Error(data?.error || String(res.status));
      form.querySelector('[data-crmmail-subject]').value = data.subject || '';
      const ed = form.querySelector('[data-editor]');
      ed.innerHTML = data.html || '';
      form.querySelector('textarea[data-html]').value = ed.innerHTML;
      ed.focus();
    } catch (err) {
      alert(err.message);
    }
  });
})();
