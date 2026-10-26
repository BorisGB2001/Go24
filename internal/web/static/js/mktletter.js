// «Вставить» of a campaign's letter editor, as the blocks of the letter designer: a picture (uploaded to go24 —
// /marketing/images gives its public address — or by its address), a button (a link drawn as one, as mail clients
// show it: a table cell with a colour), a divider, columns (a table of 2 or 3 cells: mail clients keep no other layout)
// and the company's social networks (only those filled in). The block goes where the caret was in the editor
// (compose.js), or to the letter's end.
(() => {
  const esc = s => String(s).replace(/[&<>"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
  const safeURL = u => {
    u = (u || '').trim();
    if (!u) return '';
    if (!/^(https?:|mailto:|tel:)/i.test(u)) u = 'https://' + u;
    return u;
  };
  const html = {
    image: src => `<p style="text-align:center;margin:12px 0"><img src="${esc(src)}" alt="" style="max-width:100%;height:auto;border:0"></p>`,
    button: (text, href, color) => `<table cellpadding="0" cellspacing="0" border="0" align="center" style="margin:16px auto"><tbody><tr>` +
      `<td style="background-color:${esc(color)};border-radius:6px"><a href="${esc(href)}" style="display:inline-block;padding:12px 28px;` +
      `color:#ffffff;text-decoration:none;font-weight:bold;font-size:16px">${esc(text)}</a></td></tr></tbody></table><p><br></p>`,
    hr: () => '<hr style="border:0;border-top:1px solid #e5e7eb;margin:16px 0"><p><br></p>',
    cols: (n, title, text) => `<table width="100%" cellpadding="0" cellspacing="0" border="0" style="margin:12px 0;table-layout:fixed"><tbody><tr>` +
      Array.from({ length: n }, (_, i) => `<td width="${Math.floor(100 / n)}%" valign="top" style="padding:0 ${i < n - 1 ? 8 : 0}px 0 ${i ? 8 : 0}px">` +
        `<h3 style="margin:0 0 8px;font-size:18px">${esc(title)}</h3><p style="margin:0">${esc(text)}</p></td>`).join('') +
      '</tr></tbody></table><p><br></p>',
    social: links => '<p style="text-align:center;margin:16px 0">' + links.map(([href, label, color]) =>
      `<a href="${esc(href)}" style="display:inline-block;margin:4px;padding:6px 14px;border-radius:16px;background-color:${color};` +
      `color:#ffffff;text-decoration:none;font-size:14px">${esc(label)}</a>`).join(' ') + '</p><p><br></p>',
  };
  // a social network's address from what was typed: an address as is, else its name (@name, a phone for WhatsApp)
  const social = {
    vk: ['#0077ff', v => 'https://vk.com/' + v.replace(/^@/, '')],
    telegram: ['#229ed9', v => 'https://t.me/' + v.replace(/^@/, '')],
    whatsapp: ['#25d366', v => 'https://wa.me/' + v.replace(/\D/g, '')],
    youtube: ['#ff0000', v => 'https://www.youtube.com/@' + v.replace(/^@/, '')],
    site: ['#4b5563', v => safeURL(v)],
  };

  document.querySelectorAll('[data-mkt-blocks]').forEach(bar => {
    const form = bar.closest('form');
    const ed = form?.querySelector('[data-editor]');
    if (!ed) return;
    let range = null; // the caret in the editor before a control took the focus
    document.addEventListener('selectionchange', () => {
      const s = document.getSelection();
      if (s && s.rangeCount && ed.contains(s.getRangeAt(0).commonAncestorContainer)) range = s.getRangeAt(0).cloneRange();
    });
    const insert = h => {
      ed.focus();
      const s = document.getSelection();
      if (range) { s.removeAllRanges(); s.addRange(range); } else { const r = document.createRange(); r.selectNodeContents(ed); r.collapse(false); s.removeAllRanges(); s.addRange(r); }
      // not execCommand('insertHTML'): Chromium rewrites the styles it inserts and drops some (max-width, text-decoration)
      const r = s.getRangeAt(0);
      r.deleteContents();
      const t = document.createElement('template');
      t.innerHTML = h;
      const last = t.content.lastChild;
      r.insertNode(t.content);
      r.setStartAfter(last);
      r.collapse(true);
      s.removeAllRanges();
      s.addRange(r);
      ed.dispatchEvent(new Event('input', { bubbles: true }));
      const ta = form.querySelector('textarea[data-html]');
      if (ta) ta.value = ed.innerHTML;
    };
    const panels = [...bar.parentElement.querySelectorAll('[data-mkt-panel]')].filter(p => p.closest('form') === form);
    const panel = name => panels.find(p => p.dataset.mktPanel === name);
    const show = name => panels.forEach(p => { p.hidden = p.dataset.mktPanel !== name || !p.hidden; });
    bar.addEventListener('mousedown', e => { if (e.target.closest('button')) e.preventDefault(); }); // the caret stays
    bar.addEventListener('click', e => {
      const b = e.target.closest('[data-mkt-add]');
      if (!b) return;
      if (b.dataset.mktAdd === 'hr') { show(''); insert(html.hr()); return; }
      show(b.dataset.mktAdd);
      panel(b.dataset.mktAdd)?.querySelector('input:not([hidden]):not([type=color])')?.focus();
    });
    panels.forEach(p => {
      // Enter in a panel's field inserts, it does not send the campaign's form
      p.addEventListener('keydown', e => { if (e.key === 'Enter') { e.preventDefault(); p.querySelector('[data-mkt-ok]')?.click(); } });
    });

    const img = panel('image');
    const msg = img?.querySelector('[data-mkt-msg]');
    img?.querySelector('[data-mkt-upload]')?.addEventListener('click', () => img.querySelector('[data-mkt-file]').click());
    img?.querySelector('[data-mkt-file]')?.addEventListener('change', async e => {
      const file = e.target.files[0];
      e.target.value = '';
      if (!file) return;
      msg.textContent = '…';
      const body = new FormData();
      body.append('file', file);
      try {
        const res = await fetch('/marketing/images', { method: 'POST', body, headers: { Accept: 'application/json', 'X-Requested-With': 'XMLHttpRequest' } });
        const out = await res.json().catch(() => ({}));
        if (!res.ok || !out.url) throw new Error(out.error || bar.dataset.err);
        msg.textContent = '';
        img.hidden = true;
        insert(html.image(out.url));
      } catch (err) {
        msg.textContent = err.message || bar.dataset.err;
      }
    });
    img?.querySelector('[data-mkt-ok]')?.addEventListener('click', () => {
      const f = img.querySelector('[data-mkt-src]');
      const src = safeURL(f.value);
      if (!/^https?:/i.test(src)) { f.focus(); return; }
      f.value = '';
      img.hidden = true;
      insert(html.image(src));
    });

    const btn = panel('button');
    btn?.querySelector('[data-mkt-ok]')?.addEventListener('click', () => {
      const text = btn.querySelector('[data-mkt-text]'), href = btn.querySelector('[data-mkt-href]');
      if (!text.value.trim()) { text.focus(); return; }
      if (!href.value.trim()) { href.focus(); return; }
      insert(html.button(text.value.trim(), safeURL(href.value), btn.querySelector('[data-mkt-color]').value || '#2f6fed'));
      text.value = href.value = '';
      btn.hidden = true;
    });

    const cols = panel('cols');
    cols?.querySelector('[data-mkt-ok]')?.addEventListener('click', () => {
      cols.hidden = true;
      insert(html.cols(+cols.querySelector('[data-mkt-n]').value || 2, cols.dataset.colTitle, cols.dataset.colText));
    });

    const soc = panel('social');
    soc?.querySelector('[data-mkt-ok]')?.addEventListener('click', () => {
      const fields = [...soc.querySelectorAll('[data-mkt-soc]')];
      const links = fields.filter(f => f.value.trim()).map(f => {
        const v = f.value.trim(), [color, addr] = social[f.dataset.mktSoc];
        return [/^https?:/i.test(v) ? v : addr(v), f.dataset.label, color];
      });
      if (!links.length) { fields[0].focus(); return; }
      soc.hidden = true;
      insert(html.social(links));
    });
  });

  // the A/B test: «Вариант B» shows letter B in the same editor (kept in ab_html meanwhile); sent, the editor holds A
  document.querySelectorAll('[data-ab-tabs]').forEach(tabs => {
    const form = tabs.closest('form');
    const ed = form?.querySelector('[data-editor]');
    const ta = form?.querySelector('textarea[data-html]');
    const b = form?.querySelector('[data-ab-html]');
    if (!ed || !b) return;
    let cur = 'a', a = '';
    const show = v => {
      if (v === cur) return;
      if (v === 'b') { a = ed.innerHTML; ed.innerHTML = b.value; } else { b.value = ed.innerHTML; ed.innerHTML = a; }
      if (ta) ta.value = ed.innerHTML;
      cur = v;
      tabs.querySelectorAll('[data-ab-tab]').forEach(t => t.classList.toggle('on', t.dataset.abTab === v));
      tabs.querySelector('[data-ab-hint]').hidden = v !== 'b';
    };
    tabs.addEventListener('click', e => { const t = e.target.closest('[data-ab-tab]'); if (t) show(t.dataset.abTab); });
    // before compose.js copies the editor into the form
    document.addEventListener('submit', e => { if (e.target === form) show('a'); }, true);
    const on = form.querySelector('[data-ab-on]');
    on?.addEventListener('change', () => {
      form.querySelector('[data-ab-fields]').hidden = !on.checked;
      tabs.hidden = !on.checked;
      if (!on.checked) show('a');
    });
  });
})();
