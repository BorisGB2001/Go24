// «База знаний»: pictures and files of a page. In the page's editor (textarea[data-kb-upload]) a file chosen with
// «Картинка или файл» (input[data-kb-file] of its form), pasted from the clipboard or dropped onto the field is
// uploaded to the base and its Markdown — ![name](/files/…) for a picture, [name](/files/…) for a file — goes where
// the cursor is.
(() => {
  const insert = (ta, text) => {
    const at = ta.selectionStart ?? ta.value.length, end = ta.selectionEnd ?? at;
    const before = ta.value.slice(0, at), after = ta.value.slice(end);
    const pre = before && !before.endsWith('\n') ? '\n' : '', post = after.startsWith('\n') ? '' : '\n';
    ta.value = before + pre + text + post + after;
    ta.selectionStart = ta.selectionEnd = (before + pre + text + post).length;
    ta.dispatchEvent(new Event('input', { bubbles: true }));
  };

  const status = (ta, text) => {
    let el = ta.form?.querySelector('[data-kb-status]');
    if (!el) {
      el = document.createElement('span');
      el.className = 'small muted';
      el.dataset.kbStatus = '';
      ta.insertAdjacentElement('afterend', el);
    }
    el.textContent = text;
  };

  const upload = async (ta, files) => {
    for (const f of files) {
      status(ta, (ta.dataset.kbUploading || '%s').replace('%s', f.name));
      const body = new FormData();
      body.append('file', f, f.name);
      try {
        const res = await fetch(ta.dataset.kbUpload, { method: 'POST', headers: { Accept: 'application/json' }, body });
        const data = await res.json();
        if (!res.ok || !data.md) throw new Error(data.error || res.statusText);
        insert(ta, data.md);
        status(ta, '');
      } catch (e) {
        status(ta, (ta.dataset.kbFailed || '%s').replace('%s', f.name) + (e.message ? ': ' + e.message : ''));
        return;
      }
    }
  };

  document.addEventListener('change', e => {
    const input = e.target.closest?.('input[data-kb-file]');
    const ta = input?.form?.querySelector('textarea[data-kb-upload]');
    if (!ta || !input.files.length) return;
    upload(ta, [...input.files]).then(() => { input.value = ''; });
  });

  document.addEventListener('paste', e => {
    const ta = e.target.closest?.('textarea[data-kb-upload]');
    const files = [...(e.clipboardData?.files || [])];
    if (!ta || !files.length) return;
    e.preventDefault();
    upload(ta, files);
  });

  document.addEventListener('dragover', e => {
    if (e.target.closest?.('textarea[data-kb-upload]') && e.dataTransfer?.types?.includes('Files')) e.preventDefault();
  });
  document.addEventListener('drop', e => {
    const ta = e.target.closest?.('textarea[data-kb-upload]');
    const files = [...(e.dataTransfer?.files || [])];
    if (!ta || !files.length) return;
    e.preventDefault();
    upload(ta, files);
  });

})();
