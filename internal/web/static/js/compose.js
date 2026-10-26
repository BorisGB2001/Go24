// Writing a letter as in Outlook ([data-compose], mail.html «mcompose»): the text editor (contenteditable — fonts,
// sizes, colours, lists, alignment, links, tables, pictures pasted or chosen; pasted HTML is cleaned), the addresses
// as chips with suggestions from the people the user wrote with, go24's employees and the CRM; Копия and СК; the
// draft kept by itself a few seconds after each change; files taken at once; the mailbox's signature and «Мои
// шаблоны»; importance, receipts; «Отправить» with the seconds of «Отменить» (kept across pages and tabs in
// localStorage), «Отправить позже»; a reply opens right under the letter. [data-snippet-editor]: the same editor
// for a signature or a template in the settings.
(() => {
  const label = (form, k) => form?.querySelector('template[data-compose-labels]')?.content.querySelector(`[data-l="${k}"]`)?.textContent || k;
  const headers = { Accept: 'application/json', 'X-Go24-Form': '1' };
  const post = async (url, body) => {
    const res = await fetch(url, { method: 'POST', body, headers });
    const data = await res.json().catch(() => null);
    if (!res.ok) throw new Error(data?.error || res.statusText || String(res.status));
    return data || {};
  };
  const busy = on => document.documentElement.classList.toggle('go24-busy', on);

  // ---------- pasted HTML: only formatted text stays (the server cleans it again) ----------
  const KEEP = new Set(('A B I U S EM STRONG P BR DIV SPAN FONT UL OL LI BLOCKQUOTE PRE CODE H1 H2 H3 H4 H5 H6 TABLE THEAD TBODY ' +
    'TFOOT TR TD TH CAPTION COL COLGROUP IMG HR SUB SUP SMALL BIG STRIKE DEL INS MARK CENTER DL DT DD').split(' '));
  const DROP = new Set('SCRIPT STYLE META LINK TITLE HEAD IFRAME OBJECT EMBED SVG MATH FORM INPUT BUTTON SELECT TEXTAREA NOSCRIPT TEMPLATE XML'.split(' '));
  const ATTRS = new Set(['style', 'href', 'src', 'alt', 'colspan', 'rowspan', 'align', 'valign', 'width', 'height', 'color', 'face', 'size',
    'border', 'cellpadding', 'cellspacing', 'bgcolor', 'start', 'type']);
  const clean = html => {
    const t = document.createElement('template');
    t.innerHTML = html;
    const walk = el => {
      for (const n of [...el.childNodes]) {
        if (n.nodeType === 8) { n.remove(); continue; } // comments, Word's conditional ones too
        if (n.nodeType !== 1) continue;
        if (DROP.has(n.tagName)) { n.remove(); continue; }
        walk(n);
        if (!KEEP.has(n.tagName)) { n.replaceWith(...n.childNodes); continue; }
        for (const a of [...n.attributes]) {
          const k = a.name.toLowerCase(), v = a.value.trim();
          if (!ATTRS.has(k) || (k === 'href' && !/^(https?:|mailto:|tel:)/i.test(v)) ||
              (k === 'src' && !/^(https?:|data:image\/(png|gif|jpeg|webp);|\/files\/|cid:)/i.test(v)) ||
              (k === 'style' && /url\(|expression|javascript|position|@import|\\/i.test(v))) n.removeAttribute(a.name);
        }
      }
    };
    walk(t.content);
    return t.innerHTML;
  };

  // ---------- the text editor ----------
  const editor = (form, pictures) => {
    const ed = form.querySelector('[data-editor]');
    const ta = form.querySelector('textarea[data-html]');
    if (!ed || !ta) return null;
    ed.innerHTML = ta.value; // the server's, cleaned
    const sync = () => { ta.value = ed.innerHTML; };
    let range = null; // the selection in the editor, kept while a toolbar control has the focus
    const keep = () => {
      const s = document.getSelection?.();
      if (s && s.rangeCount && ed.contains(s.getRangeAt(0).commonAncestorContainer)) range = s.getRangeAt(0).cloneRange();
    };
    const restore = () => {
      ed.focus();
      const s = document.getSelection?.();
      if (range && s) { s.removeAllRanges(); s.addRange(range); }
    };
    document.addEventListener('selectionchange', () => { keep(); states(); });
    const exec = (cmd, value) => {
      restore();
      try {
        document.execCommand('styleWithCSS', false, cmd !== 'fontSize');
        if (cmd === 'fontSize') { // execCommand knows 1–7 only: size 7 is put on, then made the size chosen
          document.execCommand('fontSize', false, '7');
          ed.querySelectorAll('font[size="7"]').forEach(f => {
            const s = document.createElement('span');
            s.style.fontSize = value;
            s.append(...f.childNodes);
            f.replaceWith(s);
          });
          ed.querySelectorAll('[style*="xxx-large"]').forEach(el => { el.style.fontSize = value; });
        } else if (cmd === 'hiliteColor') {
          if (!document.execCommand('hiliteColor', false, value)) document.execCommand('backColor', false, value);
        } else if (cmd === 'formatBlock') {
          document.execCommand('formatBlock', false, `<${value}>`);
        } else {
          document.execCommand(cmd, false, value);
        }
      } catch { /* an old browser without the command */ }
      keep();
      ed.dispatchEvent(new Event('input', { bubbles: true }));
    };
    const insert = html => exec('insertHTML', html);
    const link = () => {
      restore();
      let url = prompt(label(form, 'link'), 'https://');
      if (!url || url === 'https://') return;
      url = url.trim();
      if (!/^(https?:|mailto:|tel:)/i.test(url)) url = (/^[^\s/]+@[^\s/]+$/.test(url) ? 'mailto:' : 'https://') + url;
      const s = document.getSelection?.();
      if (!s || s.isCollapsed) {
        const a = document.createElement('a');
        a.href = url;
        a.textContent = url.replace(/^mailto:/i, '');
        insert(a.outerHTML);
      } else exec('createLink', url);
    };
    const states = () => {
      if (!form.isConnected) return;
      form.querySelectorAll('[data-tools] button[data-cmd]').forEach(b => {
        let on = false;
        try { on = ['bold', 'italic', 'underline', 'strikeThrough', 'insertUnorderedList', 'insertOrderedList', 'justifyCenter', 'justifyRight']
          .includes(b.dataset.cmd) && document.queryCommandState(b.dataset.cmd); } catch { /* not supported */ }
        b.classList.toggle('on', !!on);
      });
    };
    const tools = form.querySelector('[data-tools]');
    tools?.addEventListener('mousedown', e => { if (e.target.closest('button')) e.preventDefault(); }); // the selection stays
    tools?.addEventListener('click', e => {
      const b = e.target.closest('button[data-cmd]');
      if (!b) return;
      if (b.dataset.cmd === 'createLink') link(); else exec(b.dataset.cmd);
    });
    tools?.addEventListener('change', e => {
      const c = e.target.closest('[data-cmd]');
      if (!c || !c.value) return;
      exec(c.dataset.cmd, c.value);
      if (c.matches('select')) c.value = '';
    });
    // a table: the size picked on a grid, as Outlook's
    const grid = form.querySelector('[data-table-grid]');
    if (grid) {
      for (let r = 1; r <= 6; r++) for (let c = 1; c <= 8; c++) {
        const cell = document.createElement('button');
        cell.type = 'button';
        cell.dataset.r = r;
        cell.dataset.c = c;
        cell.title = `${r} × ${c}`;
        grid.append(cell);
      }
      grid.addEventListener('mouseover', e => {
        const h = e.target.closest('button[data-r]');
        if (h) grid.querySelectorAll('button').forEach(b => b.classList.toggle('on', +b.dataset.r <= +h.dataset.r && +b.dataset.c <= +h.dataset.c));
      });
      grid.addEventListener('mousedown', e => e.preventDefault());
      grid.addEventListener('click', e => {
        const h = e.target.closest('button[data-r]');
        if (!h) return;
        const td = '<td style="border: 1px solid #a6a6a6; padding: 4px 6px; min-width: 40px">&nbsp;</td>';
        const row = `<tr>${td.repeat(+h.dataset.c)}</tr>`;
        insert(`<table style="border-collapse: collapse; width: 100%"><tbody>${row.repeat(+h.dataset.r)}</tbody></table><p><br></p>`);
        grid.closest('details')?.removeAttribute('open');
      });
    }
    ed.addEventListener('keydown', e => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); keep(); link(); }
    });
    ed.addEventListener('paste', e => {
      const files = [...(e.clipboardData?.files || [])].filter(f => f.type.startsWith('image/'));
      if (files.length && pictures) { e.preventDefault(); keep(); files.forEach(f => pictures(f)); return; }
      const html = e.clipboardData?.getData('text/html');
      if (html) { e.preventDefault(); insert(clean(html)); }
    });
    ed.addEventListener('drop', e => {
      const files = [...(e.dataTransfer?.files || [])].filter(f => f.type.startsWith('image/'));
      if (!files.length || !pictures) return;
      e.preventDefault();
      e.stopPropagation();
      keep();
      files.forEach(f => pictures(f));
    });
    resizer(ed, sync);
    return { ed, sync, insert, keep, restore, exec };
  };

  // a picture of the text is resized as in Outlook: a click shows a frame with eight handles — the corners keep
  // its proportions, the edges change one side; the size goes into the letter (width, height); Delete removes it
  const resizer = (ed, sync) => {
    const host = ed.parentElement;
    if (!host) return;
    if (getComputedStyle(host).position === 'static') host.style.position = 'relative';
    const box = document.createElement('div');
    box.className = 'mresize';
    box.hidden = true;
    for (const h of ['nw', 'n', 'ne', 'e', 'se', 's', 'sw', 'w']) {
      const d = document.createElement('span');
      d.dataset.h = h;
      d.className = 'mresize-h mresize-' + h;
      box.append(d);
    }
    const size = document.createElement('span');
    size.className = 'mresize-size';
    box.append(size);
    host.append(box);
    let img = null;
    const place = () => {
      if (!img || !ed.contains(img)) { hide(); return; }
      const r = img.getBoundingClientRect(), o = host.getBoundingClientRect();
      Object.assign(box.style, { left: `${r.left - o.left + host.scrollLeft}px`, top: `${r.top - o.top + host.scrollTop}px`,
        width: `${r.width}px`, height: `${r.height}px` });
      size.textContent = `${Math.round(r.width)} × ${Math.round(r.height)}`;
    };
    const show = el => { img = el; box.hidden = false; place(); };
    const hide = () => { img = null; box.hidden = true; };
    ed.addEventListener('click', e => { if (e.target.tagName === 'IMG') show(e.target); else hide(); });
    ed.addEventListener('input', () => (window.requestAnimationFrame ? requestAnimationFrame(place) : place()));
    ed.addEventListener('scroll', place);
    window.addEventListener('resize', place);
    document.addEventListener('pointerdown', e => { if (img && !box.contains(e.target) && e.target !== img) hide(); });
    ed.addEventListener('keydown', e => {
      if (!img) return;
      if (e.key === 'Delete' || e.key === 'Backspace') { e.preventDefault(); img.remove(); hide(); sync(); }
      else if (e.key === 'Escape') hide();
    });
    box.addEventListener('pointerdown', e => {
      const h = e.target.dataset?.h;
      if (!h || !img) return;
      e.preventDefault();
      const r = img.getBoundingClientRect();
      const x0 = e.clientX, y0 = e.clientY, w0 = r.width || img.width, h0 = r.height || img.height, ratio = w0 / (h0 || 1);
      const max = ed.clientWidth || 2000;
      e.target.setPointerCapture?.(e.pointerId);
      const move = ev => {
        const dx = (ev.clientX - x0) * (h.includes('w') ? -1 : 1), dy = (ev.clientY - y0) * (h.includes('n') ? -1 : 1);
        let w = w0, hh = h0;
        if (h.length === 2) { w = Math.max(16, Math.min(max, w0 + dx)); hh = w / ratio; } // a corner: proportional
        else if (h === 'e' || h === 'w') w = Math.max(16, Math.min(max, w0 + dx));
        else hh = Math.max(16, h0 + dy);
        img.setAttribute('width', Math.round(w));
        img.setAttribute('height', Math.round(hh));
        img.style.width = `${Math.round(w)}px`;
        img.style.height = `${Math.round(hh)}px`;
        place();
      };
      const up = () => {
        e.target.removeEventListener('pointermove', move);
        e.target.removeEventListener('pointerup', up);
        sync();
      };
      e.target.addEventListener('pointermove', move);
      e.target.addEventListener('pointerup', up);
    });
  };

  // ---------- the addresses: chips, suggestions as they are typed ----------
  const EMAIL = /^[^\s@<>,;"]+@[^\s@<>,;"]+\.[^\s@<>,;"]+$/;
  // split splits an address list at commas and semicolons outside quotes and angle brackets
  const split = list => {
    const out = [];
    let cur = '', q = false, a = false;
    for (const ch of list || '') {
      if (ch === '"') q = !q;
      else if (ch === '<' && !q) a = true;
      else if (ch === '>' && !q) a = false;
      if ((ch === ',' || ch === ';' || ch === '\n') && !q && !a) { if (cur.trim()) out.push(cur.trim()); cur = ''; continue; }
      cur += ch;
    }
    if (cur.trim()) out.push(cur.trim());
    return out.map(s => {
      const m = s.match(/^"?(.*?)"?\s*<([^<>]+)>$/);
      return m ? { name: m[1].replace(/\\(["\\])/g, '$1').trim(), email: m[2].trim() } : { name: '', email: s.replace(/^mailto:/i, '') };
    });
  };
  const format = c => c.name ? `"${c.name.replace(/(["\\])/g, '\\$1')}" <${c.email}>` : c.email;
  // kindIcon is the icon of a recipient's source (employee, contact, crm_contact, …: mailbook.go)
  const kindIcon = (form, k) => form?.querySelector('template[data-compose-labels]')?.content.querySelector(`[data-i="${k}"] > .i`)?.cloneNode(true) ||
    document.createTextNode('');

  const recipients = (box, form, changed) => {
    const hidden = box.querySelector('input[name]');
    const input = document.createElement('input');
    input.type = 'text';
    input.className = 'mc-rcpt-in';
    input.autocomplete = 'off';
    input.placeholder = hidden.placeholder || '';
    if (hidden.id) { input.id = hidden.id; hidden.removeAttribute('id'); }
    if (hidden.hasAttribute('aria-label')) input.setAttribute('aria-label', hidden.getAttribute('aria-label'));
    if ('focus' in hidden.dataset) input.dataset.focus = '';
    hidden.type = 'hidden';
    box.append(input);
    const list = [];
    const sync = () => {
      hidden.value = list.map(format).join(', ');
      box.querySelectorAll('.mc-chip').forEach(c => c.remove());
      for (const [i, c] of list.entries()) {
        const chip = document.createElement('span');
        chip.className = 'mc-chip' + (EMAIL.test(c.email) ? '' : ' bad');
        chip.title = c.kind ? `${label(form, c.kind)}: ${c.email}` : c.email;
        chip.textContent = c.name || c.email;
        if (c.kind) { // the source the address was picked from, as in its suggestion
          chip.dataset.kind = c.kind;
          const k = document.createElement('span');
          k.className = 'mc-kind';
          k.append(kindIcon(form, c.kind));
          chip.prepend(k);
        }
        const x = document.createElement('button');
        x.type = 'button';
        x.textContent = '×';
        x.setAttribute('aria-label', label(form, 'delete'));
        x.addEventListener('click', () => { list.splice(i, 1); sync(); changed(); input.focus(); });
        chip.append(x);
        chip.addEventListener('dblclick', () => { list.splice(i, 1); sync(); input.value = format(c); input.focus(); });
        input.before(chip);
      }
    };
    const add = (text, kind) => {
      const got = split(text);
      if (!got.length) return false;
      for (const c of got) if (!list.some(x => x.email.toLowerCase() === c.email.toLowerCase())) list.push(kind ? { ...c, kind } : c);
      sync();
      changed();
      return true;
    };
    list.push(...split(hidden.value));
    sync();
    // suggestions
    let pop = null, items = [], at = -1, timer = 0, asked = '';
    const close = () => { pop?.remove(); pop = null; items = []; at = -1; };
    const mark = () => pop?.querySelectorAll('.mc-sugg-item').forEach((el, i) => el.classList.toggle('on', i === at));
    const pick = i => {
      const it = items[i];
      if (!it) return;
      input.value = '';
      // a contact list or a department turns into its people
      add(it.members ? it.members.map(format).join(', ') : format({ name: it.name, email: it.email }), it.members ? '' : it.kind);
      close();
      input.focus();
    };
    const suggest = async () => {
      const q = input.value.trim();
      if (!q) { close(); return; }
      asked = q;
      let found = [];
      try {
        const res = await fetch('/mail/rcpt?q=' + encodeURIComponent(q), { headers: { Accept: 'application/json' } });
        if (res.ok) found = await res.json();
      } catch { /* no suggestions */ }
      if (asked !== q || input.value.trim() !== q) return;
      found = found.filter(f => !list.some(x => x.email.toLowerCase() === f.email.toLowerCase()));
      close();
      if (!found.length) return;
      items = found;
      pop = document.createElement('div');
      pop.className = 'card mc-sugg';
      pop.setAttribute('role', 'listbox');
      for (const [i, f] of found.entries()) {
        const el = document.createElement('div');
        el.className = 'mc-sugg-item';
        el.setAttribute('role', 'option');
        el.dataset.kind = f.kind;
        const ic = document.createElement('span');
        ic.className = 'mc-kind';
        ic.append(kindIcon(form, f.kind));
        const n = document.createElement('b');
        n.textContent = f.name || f.email;
        const m = document.createElement('span');
        m.className = 'small muted';
        m.textContent = f.name ? f.email : '';
        if (f.members) m.textContent = f.members.slice(0, 3).map(x => x.name || x.email).join(', ') + (f.members.length > 3 ? ` +${f.members.length - 3}` : '');
        const k = document.createElement('span');
        k.className = 'tag small mc-kind-tag';
        k.dataset.kind = f.kind;
        k.textContent = label(form, f.kind);
        el.append(ic, n, ' ', m, ' ', k);
        el.addEventListener('mousedown', e => { e.preventDefault(); pick(i); });
        pop.append(el);
      }
      box.append(pop);
    };
    input.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(suggest, 150); });
    input.addEventListener('keydown', e => {
      if (pop && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) {
        e.preventDefault();
        at = (at + (e.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length;
        mark();
        return;
      }
      if (e.key === 'Escape' && pop) { e.preventDefault(); e.stopPropagation(); close(); return; }
      if ((e.key === 'Enter' || e.key === 'Tab') && pop && at >= 0) { e.preventDefault(); pick(at); return; }
      if ((e.key === 'Enter' && !(e.ctrlKey || e.metaKey)) || e.key === ',' || e.key === ';') {
        if (input.value.trim()) { e.preventDefault(); if (add(input.value)) input.value = ''; close(); }
        else if (e.key === 'Enter') e.preventDefault(); // Enter in an address never sends the letter
        return;
      }
      if (e.key === 'Tab' && input.value.trim()) { add(input.value); input.value = ''; close(); return; }
      if (e.key === 'Backspace' && !input.value && list.length) { list.pop(); sync(); changed(); }
    });
    input.addEventListener('paste', e => {
      const text = e.clipboardData?.getData('text') || '';
      if (/[,;\n]/.test(text) || (text.match(/@/g) || []).length > 1) { e.preventDefault(); add(input.value + text); input.value = ''; }
    });
    input.addEventListener('blur', () => setTimeout(() => {
      if (input.value.trim() && document.activeElement !== input) { add(input.value); input.value = ''; }
      close();
    }, 150));
    box.addEventListener('click', e => { if (e.target === box) input.focus(); });
    return {
      commit: () => { if (input.value.trim()) { add(input.value); input.value = ''; } }, count: () => list.length, input, name: hidden.name,
      has: email => list.some(x => x.email.toLowerCase() === email.toLowerCase()), add: c => add(format(c), c.kind), all: () => list.map(format),
    };
  };

  // ---------- the address book, as Outlook's: a tab per source; the people picked go to Кому, Копия or Скрытая ----------
  const BOOK_SOURCES = ['', 'employee', 'contact', 'crm', 'history', 'list'];
  const addressBook = (form, rc, field) => {
    const L = k => label(form, k);
    const el = (tag, cls, text) => {
      const e = document.createElement(tag);
      if (cls) e.className = cls;
      if (text != null) e.textContent = text;
      return e;
    };
    const btn = (cls, text, icon, title) => {
      const b = el('button', cls, text);
      b.type = 'button';
      if (icon) b.prepend(kindIcon(form, icon));
      if (title) { b.title = title; b.setAttribute('aria-label', title); }
      return b;
    };
    const dlg = el('dialog', 'mc-book');
    dlg.setAttribute('aria-label', L('book_title'));
    const head = el('div', 'mc-book-head');
    const add = btn('ghost small', L('book_new'), 'plus');
    const shut = btn('ghost small', '', 'x', L('book_done'));
    head.append(el('h3', '', L('book_title')), add, shut);
    const search = el('input');
    search.type = 'search';
    search.placeholder = L('book_search');
    search.setAttribute('aria-label', L('book_search'));
    const tabs = el('div', 'mc-book-tabs');
    tabs.setAttribute('role', 'tablist');
    for (const src of BOOK_SOURCES) {
      const t = btn('ghost small', L('book_' + (src || 'all')));
      t.dataset.src = src;
      t.setAttribute('role', 'tab');
      tabs.append(t);
    }
    const list = el('div', 'mc-book-list');
    list.setAttribute('role', 'listbox');
    list.setAttribute('aria-multiselectable', 'true');
    const edit = el('div', 'mc-book-edit');
    edit.hidden = true;
    const status = el('div', 'small muted mc-book-status', L('book_add_hint'));
    status.setAttribute('aria-live', 'polite');
    const foot = el('div', 'mc-book-foot');
    for (const f of ['to', 'cc', 'bcc']) {
      if (!rc[f]) continue;
      const b = btn(f === field ? 'primary small' : 'small', L(f) + ' →');
      b.dataset.to = f;
      foot.append(b);
    }
    const done = btn('small', L('book_done'));
    foot.append(done);
    dlg.append(head, search, tabs, edit, list, status, foot);

    let src = '', rows = [], asked = 0, timer = 0;
    const chosen = new Map();
    const keyOf = r => r.email ? r.email.toLowerCase() : `${r.kind}:${r.name}`;
    const load = async () => {
      const n = ++asked;
      let got = [];
      try {
        const res = await fetch(`/mail/book?src=${encodeURIComponent(src)}&q=${encodeURIComponent(search.value.trim())}`, { headers: { Accept: 'application/json' } });
        if (res.ok) got = await res.json();
      } catch { /* nobody */ }
      if (n !== asked) return;
      rows = got;
      render();
    };
    const toggle = (row, r) => {
      const k = keyOf(r);
      if (chosen.has(k)) chosen.delete(k); else chosen.set(k, r);
      row.setAttribute('aria-selected', String(chosen.has(k)));
    };
    // put adds the people picked to the field f (a list or a department — its people) and shows the field
    const put = f => {
      if (!chosen.size || !rc[f]) return;
      const names = [];
      for (const r of chosen.values()) {
        if (r.members) r.members.forEach(m => rc[f].add(m)); else rc[f].add({ name: r.name, email: r.email, kind: r.kind });
        names.push(r.name || r.email);
      }
      const box = form.querySelector(`[data-field="${f}"]`);
      if (box?.hidden) { box.hidden = false; form.querySelector(`[data-show="${f}"]`)?.setAttribute('hidden', ''); }
      chosen.clear();
      status.textContent = `${L(f)}: ${names.join(', ')}`;
      render();
    };
    // contactForm adds a personal contact (c null) or changes one
    const contactForm = c => {
      edit.replaceChildren();
      const inputs = {};
      for (const k of ['name', 'email', 'company', 'phone']) {
        const lab = el('label', '', L('book_' + k));
        const i = el('input');
        i.type = k === 'email' ? 'email' : 'text';
        i.value = c?.[k] || '';
        inputs[k] = i;
        lab.append(i);
        edit.append(lab);
      }
      const save = btn('primary small', L('save')), cancel = btn('small', L('cancel'));
      const row = el('div', 'row');
      row.append(save, cancel);
      edit.append(row);
      edit.hidden = false;
      inputs.name.focus();
      cancel.addEventListener('click', () => { edit.hidden = true; });
      const send = async () => {
        const body = new FormData();
        for (const k in inputs) body.set(k, inputs[k].value);
        try {
          const got = await post(c?.id ? `/mail/contacts/${encodeURIComponent(c.id)}/save` : '/mail/contacts', body);
          edit.hidden = true;
          status.textContent = L('book_kept').replace('%s', got.name || got.email);
          load();
        } catch (err) { status.textContent = err.message; }
      };
      save.addEventListener('click', send);
      edit.onkeydown = e => { if (e.key === 'Enter') { e.preventDefault(); send(); } };
    };
    const render = () => {
      list.replaceChildren();
      tabs.querySelectorAll('[data-src]').forEach(t => t.setAttribute('aria-selected', String(t.dataset.src === src)));
      if (!rows.length) list.append(el('div', 'muted small mc-book-empty', L('book_empty')));
      for (const r of rows) {
        const row = el('div', 'mc-book-row');
        row.dataset.kind = r.kind;
        row.tabIndex = 0;
        row.setAttribute('role', 'option');
        row.setAttribute('aria-selected', String(chosen.has(keyOf(r))));
        const ic = el('span', 'mc-kind');
        ic.append(kindIcon(form, r.kind));
        const who = el('span', 'mc-book-who');
        who.append(el('b', '', r.name || r.email));
        const more = r.members ? r.members.slice(0, 3).map(x => x.name || x.email).join(', ') + (r.members.length > 3 ? ` +${r.members.length - 3}` : '')
          : [r.name ? r.email : '', r.company, r.phone].filter(Boolean).join(' · ');
        if (more) who.append(el('span', 'small muted', more));
        const tag = el('span', 'tag small mc-kind-tag', L(r.kind));
        tag.dataset.kind = r.kind;
        row.append(ic, who, tag);
        if (r.kind === 'contact') {
          const ed = btn('ghost small', '', 'edit', L('book_edit')), del = btn('ghost small', '', 'trash', L('delete'));
          ed.addEventListener('click', () => contactForm(r));
          del.addEventListener('click', async () => {
            if (!confirm(L('book_delete_ask').replace('%s', r.name || r.email))) return;
            try { await post(`/mail/contacts/${encodeURIComponent(r.id)}/delete`, new FormData()); load(); } catch (err) { status.textContent = err.message; }
          });
          row.append(ed, del);
        } else if (r.email) { // someone not in the personal contacts yet
          const keep = btn('ghost small', '', 'plus', L('book_keep'));
          keep.addEventListener('click', async () => {
            const body = new FormData();
            body.set('name', r.name || '');
            body.set('email', r.email);
            if (r.kind === 'crm_company') body.set('company', r.name || '');
            try { const got = await post('/mail/contacts', body); status.textContent = L('book_kept').replace('%s', got.name || got.email); } catch (err) { status.textContent = err.message; }
          });
          row.append(keep);
        }
        row.addEventListener('click', e => { if (!e.target.closest('button')) toggle(row, r); });
        row.addEventListener('dblclick', e => { if (e.target.closest('button')) return; chosen.set(keyOf(r), r); put(field); });
        row.addEventListener('keydown', e => {
          if (e.target !== row) return;
          if (e.key === ' ') { e.preventDefault(); toggle(row, r); }
          if (e.key === 'Enter') { e.preventDefault(); chosen.set(keyOf(r), r); put(field); }
        });
        list.append(row);
      }
    };
    const close = () => { clearTimeout(timer); if (dlg.open && dlg.close) dlg.close(); dlg.remove(); rc[field]?.input.focus(); };
    tabs.addEventListener('click', e => {
      const t = e.target.closest('[data-src]');
      if (!t) return;
      src = t.dataset.src;
      load();
    });
    search.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(load, 200); });
    search.addEventListener('keydown', e => { if (e.key === 'Enter') e.preventDefault(); });
    foot.addEventListener('click', e => { const b = e.target.closest('[data-to]'); if (b) put(b.dataset.to); });
    add.addEventListener('click', () => contactForm(null));
    done.addEventListener('click', close);
    shut.addEventListener('click', close);
    dlg.addEventListener('cancel', e => { e.preventDefault(); close(); });
    document.body.append(dlg);
    if (dlg.showModal) dlg.showModal(); else dlg.setAttribute('open', '');
    search.focus();
    load();
    return dlg;
  };

  // ---------- @mentions in the text ----------
  const mentions = (form, ed, added) => {
    let pop = null, items = [], at = -1, timer = 0, range = null;
    const close = () => { pop?.remove(); pop = null; items = []; at = -1; };
    // the «@word» just before the caret, as a range over it
    const typed = () => {
      const sel = document.getSelection();
      if (!sel?.isCollapsed || !sel.rangeCount || !ed.contains(sel.anchorNode) || sel.anchorNode.nodeType !== Node.TEXT_NODE) return null;
      const node = sel.anchorNode, before = node.data.slice(0, sel.anchorOffset);
      const m = before.match(/(^|[\s(\u00a0])@([\p{L}\p{N}._-]{1,40})$/u);
      if (!m || node.parentElement?.closest('a')) return null;
      const r = document.createRange();
      r.setStart(node, sel.anchorOffset - m[2].length - 1);
      r.setEnd(node, sel.anchorOffset);
      return { q: m[2], r };
    };
    const pick = i => {
      const it = items[i];
      if (!it || !range) return;
      const a = document.createElement('a');
      a.href = 'mailto:' + it.email;
      a.dataset.mention = '';
      a.textContent = '@' + (it.name || it.email);
      range.deleteContents();
      range.insertNode(a);
      const space = document.createTextNode('\u00a0');
      a.after(space);
      const sel = document.getSelection();
      sel.removeAllRanges();
      const r = document.createRange();
      r.setStartAfter(space);
      sel.addRange(r);
      close();
      added({ name: it.name, email: it.email });
      ed.dispatchEvent(new Event('input', { bubbles: true }));
    };
    const suggest = async () => {
      const t = typed();
      if (!t) { close(); return; }
      let found = [];
      try {
        const res = await fetch('/mail/rcpt?people=1&q=' + encodeURIComponent(t.q), { headers: { Accept: 'application/json' } });
        if (res.ok) found = (await res.json()).filter(f => f.email);
      } catch { /* no suggestions */ }
      const now = typed();
      if (!now || now.q !== t.q) return;
      close();
      if (!found.length) return;
      range = now.r;
      items = found;
      at = 0;
      pop = document.createElement('div');
      pop.className = 'card mc-sugg mc-mention';
      pop.setAttribute('role', 'listbox');
      for (const [i, f] of found.entries()) {
        const el = document.createElement('div');
        el.className = 'mc-sugg-item' + (i === 0 ? ' on' : '');
        el.setAttribute('role', 'option');
        const n = document.createElement('b');
        n.textContent = f.name || f.email;
        const m = document.createElement('span');
        m.className = 'small muted';
        m.textContent = f.name ? f.email : '';
        el.append(n, ' ', m);
        el.addEventListener('mousedown', e => { e.preventDefault(); pick(i); });
        pop.append(el);
      }
      const box = range.getBoundingClientRect?.() || { bottom: 0, left: 0 };
      pop.style.position = 'fixed';
      pop.style.top = `${box.bottom + 4}px`;
      pop.style.left = `${Math.max(8, Math.min(box.left, window.innerWidth - 340))}px`;
      document.body.append(pop);
    };
    ed.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(suggest, 150); });
    ed.addEventListener('keydown', e => {
      if (!pop) return;
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault();
        at = (at + (e.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length;
        pop.querySelectorAll('.mc-sugg-item').forEach((el, i) => el.classList.toggle('on', i === at));
      } else if (e.key === 'Enter' || e.key === 'Tab') {
        e.preventDefault();
        e.stopImmediatePropagation();
        pick(at);
      } else if (e.key === 'Escape') {
        e.preventDefault();
        e.stopImmediatePropagation();
        close();
      }
    }, true);
    ed.addEventListener('blur', () => setTimeout(close, 150));
  };

  // ---------- «Отменить» after «Отправить»: kept in localStorage, shown on every mail page and tab ----------
  const KEY = 'go24-mail-undo';
  const toastEl = () => document.querySelector('[data-undo-toast]');
  const tlabel = k => toastEl()?.querySelector(`template[data-undo-labels]`)?.content.querySelector(`[data-l="${k}"]`)?.textContent || k;
  let toastTimer = 0;
  const flash = (text, ms) => {
    const t = toastEl();
    if (!t) return;
    clearTimeout(toastTimer);
    t.querySelector('[data-undo-text]').textContent = text;
    t.querySelector('[data-undo-cancel]').hidden = true;
    t.hidden = false;
    toastTimer = setTimeout(() => { t.hidden = true; }, ms);
  };
  const toast = () => {
    const t = toastEl();
    let u = null;
    try { u = JSON.parse(localStorage.getItem(KEY) || 'null'); } catch { /* no storage */ }
    if (!t || !u) return;
    const left = u.until - Date.now();
    if (left <= 0) { try { localStorage.removeItem(KEY); } catch { /* no storage */ } return; }
    clearTimeout(toastTimer);
    t.querySelector('[data-undo-text]').textContent = tlabel('sending');
    const b = t.querySelector('[data-undo-cancel]');
    b.hidden = false;
    b.onclick = async () => {
      b.disabled = true;
      try {
        const data = await post(`/mail/drafts/${encodeURIComponent(u.id)}/cancel`, new URLSearchParams());
        try { localStorage.removeItem(KEY); } catch { /* no storage */ }
        location.href = data.href;
      } catch (err) { flash(err.message, 4000); }
      b.disabled = false;
    };
    t.hidden = false;
    toastTimer = setTimeout(() => {
      try { localStorage.removeItem(KEY); } catch { /* no storage */ }
      flash(tlabel('sent'), 2500);
    }, left);
  };
  window.addEventListener('storage', e => { if (e.key === KEY) toast(); });

  // ---------- the letter's editor ----------
  const init = form => {
    if (form.dataset.ready) return;
    form.dataset.ready = '1';
    const status = form.querySelector('[data-status]');
    const draft = form.querySelector('input[name=draft]');
    const files = form.querySelector('[data-files]');
    let dirty = false, timer = 0, saving = null;
    const changed = () => { dirty = true; clearTimeout(timer); timer = setTimeout(() => save(), 3000); };
    const error = text => {
      let box = form.querySelector('[data-error]');
      if (!box) { box = document.createElement('div'); box.className = 'form-error'; box.dataset.error = ''; form.prepend(box); }
      box.textContent = text;
      box.hidden = !text;
      if (text) box.scrollIntoView?.({ block: 'nearest' });
    };
    // pictures of the text go to the draft at once and show where the cursor is
    const picture = async f => {
      try {
        const got = await upload(f, true);
        ed.restore();
        const img = document.createElement('img');
        img.src = got.url;
        img.alt = f.name;
        img.style.maxWidth = '100%';
        ed.insert(img.outerHTML);
      } catch (err) { error(err.message); }
    };
    const ed = editor(form, picture);
    const rc = [...form.querySelectorAll('[data-rcpt]')].map(b => recipients(b, form, changed));
    const fields = () => { ed?.sync(); rc.forEach(r => r.commit()); return new FormData(form); };
    // «Сохранить адресатов как список»: To, Cc and Bcc become a contact list of the user
    form.querySelector('[data-list-save]')?.addEventListener('click', async () => {
      rc.forEach(r => r.commit());
      const all = rc.flatMap(r => r.all());
      if (!all.length) { error(label(form, 'no_rcpt')); return; }
      const name = prompt(label(form, 'list_name'));
      if (!name?.trim()) return;
      try {
        await post('/mail/lists', new URLSearchParams({ name: name.trim(), members: all.join('\n') }));
        if (status) status.textContent = label(form, 'list_saved').replace('%s', name.trim());
      } catch (err) { error(err.message); }
    });
    // @mentions, as Outlook's: «@» and a name in the text offer people; the one picked is a link in the text and
    // goes to «Кому» unless the letter is addressed to them already
    if (ed && rc[0]) mentions(form, ed.ed, person => {
      if (rc.some(r => r.has(person.email))) return;
      rc[0].add(person);
      changed();
      if (status) status.textContent = label(form, 'mention').replace('%s', person.name || person.email);
    });
    // the draft is kept: a few seconds after a change, before a file, before sending; one save at a time
    const save = async force => {
      if (saving) await saving.catch(() => null);
      if (!dirty && !(force && !draft.value)) return draft.value;
      dirty = false;
      clearTimeout(timer);
      if (status) status.textContent = label(form, 'saving');
      saving = (async () => {
        const body = new URLSearchParams(fields());
        body.delete('send_at');
        const data = await post('/mail/drafts/save', body);
        draft.value = data.id;
        if (status) status.textContent = label(form, 'saved').replace('{t}', data.saved || '');
        return data.id;
      })();
      try { return await saving; } catch (err) { dirty = true; if (status) status.textContent = label(form, 'failed'); throw err; } finally { saving = null; }
    };
    form.addEventListener('input', e => { if (!e.target.closest('[data-tools]') && !e.target.matches('[data-send-at]')) changed(); });
    form.addEventListener('change', e => { if (e.target.matches('input[type=checkbox], select[name=box]')) changed(); });
    // files: taken at once (a chip with ×)
    const chip = f => {
      const c = document.createElement('span');
      c.className = 'mc-file';
      c.dataset.file = f.id || '';
      const a = document.createElement('a');
      a.textContent = f.name;
      if (f.id) { a.href = `/files/${encodeURIComponent(f.id)}`; a.target = '_blank'; }
      const s = document.createElement('span');
      s.className = 'muted small';
      s.textContent = f.size_text || label(form, 'uploading');
      const x = document.createElement('button');
      x.type = 'button';
      x.className = 'ghost small';
      x.dataset.unattach = '';
      x.textContent = '×';
      x.title = label(form, 'delete');
      c.append(a, ' ', s, x);
      return c;
    };
    const upload = async (f, inline) => {
      const id = await save(true);
      const body = new FormData();
      body.append('file', f, f.name);
      if (inline) body.append('inline', '1');
      busy(true);
      try { return await post(`/mail/drafts/${encodeURIComponent(id)}/files`, body); } finally { busy(false); }
    };
    const attach = async list => {
      for (const f of list) {
        const c = chip({ name: f.name });
        files.append(c);
        try {
          const got = await upload(f, false);
          c.replaceWith(chip(got));
        } catch (err) { c.remove(); error(err.message); }
      }
    };
    form.querySelector('[data-attach]')?.addEventListener('change', e => { attach([...e.target.files]); e.target.value = ''; });
    form.querySelector('[data-picture]')?.addEventListener('change', e => { ed?.keep(); [...e.target.files].forEach(picture); e.target.value = ''; });
    // «С Диска»: the picker (mailx.js) hands a drive's file back — a copy goes with the letter, or a link to it into the text
    form.querySelector('[data-drive-pick]')?.addEventListener('click', () => {
      ed?.keep();
      document.dispatchEvent(new CustomEvent('go24:drivepick', { detail: { form } }));
    });
    form.addEventListener('go24:drive', async e => {
      const f = e.detail;
      if (f.mode === 'link') {
        const a = document.createElement('a');
        a.href = f.url;
        a.textContent = f.name;
        ed?.restore();
        ed?.insert(a.outerHTML + '&nbsp;');
        changed();
        return;
      }
      const c = chip({ name: f.name });
      files.append(c);
      try {
        const id = await save(true);
        busy(true);
        try { c.replaceWith(chip(await post(`/mail/drafts/${encodeURIComponent(id)}/files/drive`, new URLSearchParams({ file: f.id })))); } finally { busy(false); }
      } catch (err) { c.remove(); error(err.message); }
    });
    files?.addEventListener('click', async e => {
      const x = e.target.closest('[data-unattach]');
      if (!x) return;
      const c = x.closest('.mc-file');
      try {
        if (c.dataset.file) await post(`/mail/drafts/${encodeURIComponent(draft.value)}/files/${encodeURIComponent(c.dataset.file)}/delete`, new URLSearchParams());
        c.remove();
      } catch (err) { error(err.message); }
    });
    form.addEventListener('dragover', e => { if (e.dataTransfer?.types?.includes('Files')) { e.preventDefault(); form.classList.add('drop'); } });
    form.addEventListener('dragleave', e => { if (!form.contains(e.relatedTarget)) form.classList.remove('drop'); });
    form.addEventListener('drop', e => {
      form.classList.remove('drop');
      if (!e.dataTransfer?.files?.length) return;
      e.preventDefault();
      attach([...e.dataTransfer.files]);
    });
    // the caption of an address field (Кому, Копия, Скрытая): the address book, adding to that field first
    form.addEventListener('click', e => {
      const b = e.target.closest('[data-book]');
      if (!b) return;
      addressBook(form, Object.fromEntries(rc.map(r => [r.name, r])), b.dataset.book);
    });
    // Копия, Скрытая
    form.addEventListener('click', e => {
      const b = e.target.closest('[data-show]');
      if (!b) return;
      const f = form.querySelector(`[data-field="${b.dataset.show}"]`);
      f.hidden = false;
      b.hidden = true;
      (f.querySelector('.mc-rcpt-in') || f.querySelector('input:not([type=hidden])'))?.focus();
    });
    // importance: high or low, pressed again — normal
    form.addEventListener('click', e => {
      const b = e.target.closest('[data-imp]');
      if (!b) return;
      const imp = form.querySelector('input[name=importance]');
      imp.value = imp.value === b.dataset.imp ? '0' : b.dataset.imp;
      form.querySelectorAll('[data-imp]').forEach(x => { x.classList.toggle('on', x.dataset.imp === imp.value); x.setAttribute('aria-pressed', String(x.dataset.imp === imp.value)); });
      changed();
    });
    // the signature: the chosen one in place of the present one; «От» changed — that mailbox's
    const mode = () => form.querySelector('input[name=mode]')?.value || '';
    const putSig = (id, html) => {
      if (!ed) return;
      const old = ed.ed.querySelector('[data-signature]');
      if (!id) { old?.remove(); changed(); return; }
      const div = document.createElement('div');
      div.dataset.signature = id;
      div.innerHTML = html;
      if (old) old.replaceWith(div);
      else {
        const quote = ed.ed.querySelector('[data-quote]');
        const at = quote?.previousElementSibling?.matches('hr') ? quote.previousElementSibling : quote;
        if (at) at.before(div); else ed.ed.append(div);
      }
      changed();
    };
    form.addEventListener('click', e => {
      const b = e.target.closest('[data-sig]');
      if (!b) return;
      putSig(b.dataset.sig, b.dataset.html || '');
      b.closest('details')?.removeAttribute('open');
    });
    let sigs = {};
    try { sigs = JSON.parse(form.dataset.sigs || '{}'); } catch { /* none */ }
    form.querySelector('select[data-box]')?.addEventListener('change', e => {
      const from = form.querySelector('input[data-from]');
      if (from) from.value = e.target.selectedOptions[0]?.dataset.from || '';
      const sg = sigs[e.target.value] || {};
      const id = mode() ? sg.Reply : sg.New;
      const b = id && form.querySelector(`[data-sig="${CSS.escape(id)}"]`);
      putSig(b ? id : '', b?.dataset.html || '');
    });
    // «Мои шаблоны»: put in where the cursor is; the selection (or the letter's own text) saved as a template
    const tpls = form.querySelector('[data-tpls]');
    tpls?.addEventListener('mousedown', e => { if (e.target.closest('button')) e.preventDefault(); });
    tpls?.addEventListener('click', async e => {
      const b = e.target.closest('[data-tpl], [data-tpl-save]');
      if (!b || !ed) return;
      if ('tpl' in b.dataset) { ed.insert(b.dataset.html || ''); b.closest('details')?.removeAttribute('open'); return; }
      const name = prompt(label(form, 'tpl_name'));
      if (!name?.trim()) return;
      let html = '';
      const s = document.getSelection?.();
      if (s && !s.isCollapsed && ed.ed.contains(s.anchorNode)) {
        const box = document.createElement('div');
        box.append(s.getRangeAt(0).cloneContents());
        html = box.innerHTML;
      } else {
        const copy = ed.ed.cloneNode(true);
        copy.querySelectorAll('[data-signature], [data-quote]').forEach(x => { if (x.matches('[data-quote]')) { let n = x; while (n) { const next = n.nextSibling; n.remove(); n = next; } } else x.remove(); });
        copy.querySelectorAll('hr').forEach(h => { if (!h.nextSibling) h.remove(); });
        html = copy.innerHTML;
      }
      try {
        const got = await post('/mail/snippets/template/new', new URLSearchParams({ name: name.trim(), html }));
        const nb = document.createElement('button');
        nb.type = 'button';
        nb.className = 'ghost small';
        nb.dataset.tpl = '';
        nb.dataset.html = html;
        nb.textContent = got.name || name;
        tpls.querySelector('[data-tpl-none]')?.remove();
        tpls.querySelector('[data-tpl-save]').before(nb);
      } catch (err) { error(err.message); }
    });
    // closing: in the reading pane the reply goes away; a letter of its own — back to the list
    const close = () => {
      clearTimeout(timer);
      if (form.closest('[data-reply-box]')) { form.remove(); return; }
      if (window.opener && new URLSearchParams(location.search).get('pane') === 'off') { window.close(); return; }
      location.href = form.dataset.back || '/mail';
    };
    form.querySelector('[data-discard]')?.addEventListener('click', async () => {
      if ((draft.value || dirty) && !confirm(label(form, 'discard'))) return;
      clearTimeout(timer);
      dirty = false;
      if (saving) await saving.catch(() => null);
      if (draft.value) {
        try { await post(`/mail/drafts/${encodeURIComponent(draft.value)}/delete`, new URLSearchParams()); } catch (err) { error(err.message); return; }
      }
      close();
    });
    form.querySelector('[data-unschedule]')?.addEventListener('click', async () => {
      try { location.href = (await post(`/mail/drafts/${encodeURIComponent(draft.value)}/cancel`, new URLSearchParams())).href; } catch (err) { error(err.message); }
    });
    form.querySelector('[data-popout]')?.addEventListener('click', async e => {
      e.preventDefault();
      try {
        const id = await save(true);
        const box = form.querySelector('[data-box]')?.value || '';
        window.open(`/mail?new=1&pane=off&box=${encodeURIComponent(box)}&draft=${encodeURIComponent(id)}`, '_blank');
        if (form.closest('[data-reply-box]')) form.remove();
      } catch (err) { error(err.message); }
    });
    form.querySelector('[data-send-custom]')?.addEventListener('click', () => {
      const v = form.querySelector('[data-send-at]')?.value;
      if (v) send(v);
    });
    // sending: what Outlook asks first (nobody to send to, no subject, a file mentioned but not attached)
    const send = async sendAt => {
      const body = fields();
      error('');
      if (!rc.some(r => r.count())) { error(label(form, 'no_rcpt')); rc[0]?.input.focus(); return; }
      if (!String(body.get('subject') || '').trim() && !confirm(label(form, 'no_subject'))) return;
      if (ed && !files?.querySelector('.mc-file')) {
        const own = ed.ed.cloneNode(true);
        const q = own.querySelector('[data-quote]');
        if (q) { let n = q.previousElementSibling?.matches('hr') ? q.previousElementSibling : q; while (n) { const next = n.nextSibling; n.remove(); n = next; } }
        own.querySelectorAll('[data-signature]').forEach(x => x.remove());
        const words = label(form, 'words');
        if (words && new RegExp(words, 'i').test(own.textContent || '') && !confirm(label(form, 'forgot'))) return;
      }
      clearTimeout(timer);
      if (saving) await saving.catch(() => null);
      const params = new URLSearchParams(body);
      params.delete('send_at');
      if (sendAt) params.set('send_at', sendAt);
      const btn = form.querySelector('.mc-send > button');
      btn?.classList.add('busy');
      busy(true);
      let data;
      try { data = await post('/mail/drafts/send', params); } catch (err) { error(err.message); return; } finally { btn?.classList.remove('busy'); busy(false); }
      dirty = false;
      draft.value = data.id;
      if (data.undo) {
        try { localStorage.setItem(KEY, JSON.stringify({ id: data.id, until: Date.now() + data.undo * 1000 })); } catch { /* no storage */ }
        toast();
      } else flash(data.scheduled || tlabel('sent'), 4000);
      close();
    };
    form.addEventListener('submit', e => {
      e.preventDefault();
      const b = e.submitter;
      send(b?.name === 'send_at' ? b.value : '');
    });
    form.addEventListener('keydown', e => {
      if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); send(''); }
    });
    window.addEventListener('beforeunload', () => {
      if (!dirty || !form.isConnected) return;
      ed?.sync();
      try { fetch('/mail/drafts/save', { method: 'POST', body: new URLSearchParams(new FormData(form)), headers, keepalive: true }); } catch { /* the page goes */ }
    });
    (form.querySelector('[data-focus]') || ed?.ed)?.focus?.();
    form._go24 = { save, send, changed };
  };

  // a signature or a template in the settings: the editor's text goes with the form
  const snippet = form => {
    if (form.dataset.ready) return;
    form.dataset.ready = '1';
    const ed = editor(form, f => { // a picture of a signature is kept in its HTML (small ones only)
      if (f.size > 300 << 10) return;
      const r = new FileReader();
      r.onload = () => { ed.restore(); ed.insert(`<img src="${r.result}" alt="">`); };
      r.readAsDataURL(f);
    });
    form.addEventListener('submit', () => ed?.sync(), true);
  };

  // a reply, reply all, forward of the letter in the reading pane opens right under it
  document.addEventListener('click', async e => {
    const a = e.target.closest?.('a[data-reply]');
    const art = a?.closest('article');
    const box = art?.querySelector('[data-reply-box]');
    if (!box || e.button !== 0 || e.ctrlKey || e.metaKey || e.shiftKey) return;
    e.preventDefault();
    const old = box.querySelector('[data-compose]');
    if (old?._go24) await old._go24.save().catch(() => null);
    const u = new URL(a.href, location.href);
    const q = new URLSearchParams({ letter: u.searchParams.get('letter') || art.dataset.letter, mode: a.dataset.reply, folder: u.searchParams.get('folder') || '', inline: '1' });
    if (u.searchParams.get('vote')) q.set('vote', u.searchParams.get('vote')); // «Изменить ответ перед отправкой»
    busy(true);
    try {
      const res = await fetch('/mail/compose?' + q, { headers: { Accept: 'text/html' } });
      if (!res.ok) throw new Error(String(res.status));
      box.innerHTML = await res.text();
    } catch { location.href = a.href; return; } finally { busy(false); }
    setup(box);
    box.scrollIntoView?.({ block: 'start', behavior: 'smooth' });
  });

  const setup = root => {
    root.querySelectorAll('form[data-compose]').forEach(init);
    root.querySelectorAll('form[data-snippet-editor]').forEach(snippet);
  };
  window.go24Compose = setup;
  setup(document);
  toast();
  window.go24Clean = clean; // the tests
})();
