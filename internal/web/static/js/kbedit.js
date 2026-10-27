// «База знаний»: the page's editor. The panel over the field (.kbtools) puts Markdown around the selection or at the
// start of its lines — bold, italic, struck, code, a link, headings, lists, a checklist, a quote, a code block, a
// table, a line; Ctrl+B, Ctrl+I and Ctrl+K do the same, Ctrl+S saves. «Предпросмотр» shows the text as the page will
// (rendered by the server, POST data-kb-preview); leaving the page with unsaved changes asks first (data-kb-leave).
(() => {
  const fieldOf = el => el.closest('form')?.querySelector('textarea[name="body"]');

  const set = (ta, value, start, end) => {
    ta.value = value;
    ta.focus();
    ta.selectionStart = start;
    ta.selectionEnd = end;
    ta.dispatchEvent(new Event('input', { bubbles: true }));
  };

  // wrap puts marks around the selection (or around «placeholder» when nothing is selected) and selects the text.
  const wrap = (ta, left, right, placeholder = '') => {
    const a = ta.selectionStart, b = ta.selectionEnd, v = ta.value;
    const text = v.slice(a, b) || placeholder;
    set(ta, v.slice(0, a) + left + text + right + v.slice(b), a + left.length, a + left.length + text.length);
  };

  // lines puts a mark at the start of each selected line; «1. » numbers them.
  const lines = (ta, mark) => {
    const v = ta.value;
    const a = v.lastIndexOf('\n', ta.selectionStart - 1) + 1;
    let b = v.indexOf('\n', ta.selectionEnd);
    if (b < 0) b = v.length;
    if (ta.selectionEnd > ta.selectionStart && v[ta.selectionEnd - 1] === '\n') b = ta.selectionEnd - 1; // not the line after
    const block = v.slice(a, b).split('\n').map((l, i) => (mark === '1. ' ? (i + 1) + '. ' : mark) + l).join('\n');
    set(ta, v.slice(0, a) + block + v.slice(b), a + block.length, a + block.length);
  };

  // block puts text on lines of its own at the cursor.
  const block = (ta, text, select = 0) => {
    const a = ta.selectionStart, b = ta.selectionEnd, v = ta.value;
    const before = v.slice(0, a), after = v.slice(b);
    const pre = before && !before.endsWith('\n') ? '\n' : '', post = after.startsWith('\n') ? '' : '\n';
    const at = (before + pre).length + select;
    set(ta, before + pre + text + post + after, at, at);
  };

  const fmt = (ta, kind, btn) => {
    switch (kind) {
      case 'bold': return wrap(ta, '**', '**');
      case 'italic': return wrap(ta, '*', '*');
      case 'strike': return wrap(ta, '~~', '~~');
      case 'code': return wrap(ta, '`', '`');
      case 'link': {
        const a = ta.selectionStart, b = ta.selectionEnd, v = ta.value;
        const text = v.slice(a, b) || btn?.dataset.kbLinkText || 'link';
        const out = '[' + text + '](https://)';
        const at = a + text.length + 3;
        return set(ta, v.slice(0, a) + out + v.slice(b), at, at + 8);
      }
      case 'h2': return lines(ta, '# ');
      case 'h3': return lines(ta, '## ');
      case 'ul': return lines(ta, '- ');
      case 'ol': return lines(ta, '1. ');
      case 'check': return lines(ta, '- [ ] ');
      case 'quote': return lines(ta, '> ');
      case 'pre': {
        const sel = ta.value.slice(ta.selectionStart, ta.selectionEnd);
        return block(ta, '```\n' + sel + '\n```', 4);
      }
      case 'table': {
        const [c1, c2] = (btn?.dataset.kbTable || 'Column 1|Column 2').split('|');
        return block(ta, `| ${c1} | ${c2} |\n|---|---|\n|  |  |`, 2);
      }
      case 'hr': return block(ta, '---');
    }
  };

  const preview = async (tools, ta) => {
    let box = tools.parentElement.querySelector('.kbpreview');
    if (!box) {
      box = document.createElement('div');
      box.className = 'kbpage kbpreview';
      ta.insertAdjacentElement('afterend', box);
    }
    box.innerHTML = '';
    const body = new FormData();
    body.append('body', ta.value);
    try {
      const res = await fetch(tools.dataset.kbPreview, { method: 'POST', headers: { Accept: 'application/json' }, body });
      const data = await res.json();
      box.innerHTML = res.ok ? data.html || '' : '';
    } catch (e) { box.textContent = e.message || ''; }
  };

  const tab = (tools, which) => {
    const ta = fieldOf(tools);
    if (!ta) return;
    tools.querySelectorAll('[data-kb-tab]').forEach(b => b.classList.toggle('on', b.dataset.kbTab === which));
    const on = which === 'preview';
    ta.hidden = on;
    tools.querySelectorAll('[data-kb-fmt]').forEach(b => { b.disabled = on; });
    if (on) preview(tools, ta);
    else {
      tools.parentElement.querySelector('.kbpreview')?.remove();
      ta.focus();
    }
  };

  document.addEventListener('click', e => {
    const t = e.target.closest?.('.kbtools [data-kb-tab]');
    if (t) return tab(t.closest('.kbtools'), t.dataset.kbTab);
    const b = e.target.closest?.('.kbtools [data-kb-fmt]');
    const ta = b && fieldOf(b);
    if (ta) fmt(ta, b.dataset.kbFmt, b);
  });

  const keys = { b: 'bold', i: 'italic', k: 'link' };
  document.addEventListener('keydown', e => {
    const ta = e.target.closest?.('textarea[name="body"][data-kb-upload]');
    if (!ta || !(e.ctrlKey || e.metaKey) || e.altKey) return;
    const k = e.key.toLowerCase();
    if (k === 's') {
      e.preventDefault();
      ta.form.requestSubmit ? ta.form.requestSubmit() : ta.form.submit();
    } else if (keys[k] && !e.shiftKey) {
      e.preventDefault();
      fmt(ta, keys[k], ta.form.querySelector(`[data-kb-fmt="${keys[k]}"]`));
    }
  });

  // unsaved changes: the text or the title differs from what the page had when opened
  const forms = new Map();
  document.querySelectorAll('textarea[data-kb-leave]').forEach(ta => {
    const f = ta.form, snap = () => new FormData(f).get('body') + '\0' + (new FormData(f).get('title') || '');
    forms.set(f, { ta, was: snap(), snap, sent: false });
    f.addEventListener('submit', () => { forms.get(f).sent = true; });
  });
  window.addEventListener('beforeunload', e => {
    for (const s of forms.values()) {
      if (!s.sent && s.snap() !== s.was && !s.ta.form.kbDraftSent?.()) { // a text kept as a draft (kbdraft.js) is not lost
        e.preventDefault();
        e.returnValue = s.ta.dataset.kbLeave;
        return s.ta.dataset.kbLeave;
      }
    }
  });
})();
