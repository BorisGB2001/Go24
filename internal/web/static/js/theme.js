// «Тема оформления»: the button in one's own card opens the dialog (layout.html, "themedlg"); the
// mode and the background picked show on the page at once, «Отмена» (or Esc) puts the page back as it was.
(() => {
  const dlg = document.querySelector('[data-theme-dlg]');
  if (!dlg) return;
  const root = document.documentElement, form = dlg.querySelector('form');
  const file = dlg.querySelector('[data-theme-file]'), pic = dlg.querySelector('[data-theme-custom-pic]');
  let saved = null, blob = '';
  const set = (name, v) => v ? root.setAttribute(name, v) : root.removeAttribute(name);
  const show = () => {
    set('data-theme', form.elements.mode.value);
    const bg = form.elements.bg.value;
    set('data-bg', bg);
    const img = blob || dlg.dataset.image || '';
    if (bg === 'custom' && img) root.style.setProperty('--bg-image', `url("${img}")`);
  };
  const restore = () => {
    if (!saved) return;
    set('data-theme', saved.mode); set('data-bg', saved.bg);
    root.setAttribute('style', saved.style); if (!saved.style) root.removeAttribute('style');
    form.reset(); file.value = '';
    if (blob) { URL.revokeObjectURL(blob); blob = ''; }
    pic.style.backgroundImage = dlg.dataset.image ? `url("${dlg.dataset.image}")` : '';
    saved = null;
  };
  document.addEventListener('click', e => {
    if (!e.target.closest('[data-theme-open]')) return;
    document.querySelector('.pcard-pop')?.remove();
    saved = { mode: root.getAttribute('data-theme') || '', bg: root.getAttribute('data-bg') || '', style: root.getAttribute('style') || '' };
    dlg.showModal();
  });
  dlg.querySelectorAll('[data-theme-cancel]').forEach(b => b.addEventListener('click', () => dlg.close()));
  dlg.addEventListener('close', restore); // after «Сохранить» the page goes away, the dialog is not closed
  form.addEventListener('submit', () => { saved = null; });
  form.addEventListener('change', e => {
    if (e.target === file) {
      const f = file.files[0];
      if (!f) return;
      if (f.size > 5 << 20) { alert(dlg.dataset.tooBig); file.value = ''; return; }
      if (blob) URL.revokeObjectURL(blob);
      blob = URL.createObjectURL(f);
      pic.style.backgroundImage = `url("${blob}")`;
      dlg.querySelector('[data-theme-custom]').checked = true;
    } else if (e.target.matches('[data-theme-custom]') && !blob && !dlg.dataset.image) {
      file.click(); // a picture of one's own: there is none yet
    }
    show();
  });
  // the picture's tile picks another picture
  pic.addEventListener('click', e => {
    if (blob || dlg.dataset.image) { e.preventDefault(); dlg.querySelector('[data-theme-custom]').checked = true; file.click(); show(); }
  });
})();
