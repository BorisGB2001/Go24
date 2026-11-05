// «Вставить свободное время» of the editor, as Outlook's «Share availability» (mail.html «mavaildlg», web/mailavail.go):
// the free spans of the user's calendar on the nearest working days (/mail/availability) — from a date, how many
// days, the shortest span — each day with its spans ticked; «Вставить в письмо» puts the block of the ticked ones
// into the text.
(() => {
  const headers = { Accept: 'application/json', 'X-Go24-Form': '1' };
  const json = async res => {
    const data = await res.json().catch(() => null);
    if (!res.ok) throw new Error(data?.error || res.statusText || String(res.status));
    return data || {};
  };

  document.addEventListener('go24:availpick', e => {
    const { form } = e.detail;
    const dlg = form.querySelector('[data-avail-dlg]') || form.availDialog;
    if (!dlg) return;
    form.availDialog = dlg;
    document.body.append(dlg); // out of the letter's form: its fields are not the letter's
    const tpl = dlg.querySelector('template[data-avail-labels]');
    const label = k => tpl?.content.querySelector(`[data-l="${k}"]`)?.textContent || k;
    const f = k => dlg.querySelector(`[data-a="${k}"]`);
    const days = dlg.querySelector('[data-avail-days]');
    const err = dlg.querySelector('[data-avail-error]');
    const tz = dlg.querySelector('[data-avail-tz]');
    const showErr = m => { err.textContent = m || ''; err.hidden = !m; };
    showErr('');

    let seq = 0;
    const load = async () => {
      const n = ++seq;
      const q = new URLSearchParams({ days: f('days').value, length: f('length').value });
      if (f('from').value) q.set('from', f('from').value);
      let res;
      try { res = await json(await fetch('/mail/availability?' + q, { headers })); } catch (x) { if (n === seq) showErr(x.message); return; }
      if (n !== seq) return; // a later choice is on its way
      showErr('');
      tz.textContent = label('tz').replace('%s', res.tz || '');
      days.replaceChildren();
      (res.days || []).forEach((d, i) => {
        const box = document.createElement('fieldset');
        box.className = 'mavail-day';
        const head = document.createElement('legend');
        head.textContent = res.labels?.[i] || d.date;
        box.append(head);
        for (const s of d.spans) {
          const l = document.createElement('label');
          l.className = 'check mavail-span';
          const c = document.createElement('input');
          c.type = 'checkbox';
          c.checked = true;
          c.value = `${d.date}T${s.start}/${s.end}`;
          l.append(c, ` ${s.start}–${s.end}`);
          box.append(l);
        }
        days.append(box);
      });
      if (!days.children.length) {
        const p = document.createElement('p');
        p.className = 'small muted';
        p.textContent = label('none');
        days.append(p);
      }
    };
    if (!dlg.dataset.wired) {
      dlg.dataset.wired = '1';
      for (const k of ['from', 'days', 'length']) f(k).addEventListener('change', () => dlg.availLoad());
      dlg.querySelector('[data-avail-close]').addEventListener('click', () => dlg.close());
      dlg.querySelector('[data-avail-insert]').addEventListener('click', async () => {
        showErr('');
        const spans = [...days.querySelectorAll('input[type="checkbox"]:checked')].map(c => c.value);
        if (!spans.length) { showErr(label('no_spans')); return; }
        const body = new URLSearchParams();
        spans.forEach(s => body.append('span', s));
        const btn = dlg.querySelector('[data-avail-insert]');
        btn.disabled = true;
        try {
          const res = await json(await fetch('/mail/availability', { method: 'POST', body, headers }));
          dlg.close();
          dlg.availForm.dispatchEvent(new CustomEvent('go24:insert', { detail: { html: res.html } }));
        } catch (x) { showErr(x.message); } finally { btn.disabled = false; }
      });
    }
    // the handlers wired once work with the letter of this opening
    dlg.availForm = form;
    dlg.availLoad = load;
    dlg.showModal ? dlg.showModal() : dlg.setAttribute('open', '');
    load();
  });
})();
