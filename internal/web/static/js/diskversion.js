// «Загрузить новую версию»: a button [data-disk-newver="file id"] (data-action — the drive's version-upload,
// data-folder — the folder shown) asks for a file on the computer; whatever its name, it becomes the file's new
// version (the name, links and access stay), then the page shows it. The script's data-l-failed is the error text.
(() => {
  const me = document.currentScript;
  const pick = document.createElement('input');
  pick.type = 'file';
  pick.hidden = true;
  document.body.append(pick);
  let target = null;
  document.addEventListener('click', e => {
    const b = e.target.closest('[data-disk-newver]');
    if (!b) return;
    e.preventDefault();
    b.closest('details')?.removeAttribute('open');
    target = b;
    pick.value = '';
    pick.click();
  });
  pick.addEventListener('change', async () => {
    const f = pick.files[0];
    if (!f || !target) return;
    const body = new FormData();
    body.append('file', target.dataset.diskNewver);
    body.append('folder', target.dataset.folder || '');
    body.append('data', f);
    document.documentElement.classList.add('go24-busy');
    try {
      const res = await fetch(target.dataset.action, { method: 'POST', headers: { Accept: 'application/json' }, body });
      const d = await res.json().catch(() => null);
      if (!res.ok) { alert(d?.error || me?.dataset.lFailed || res.statusText); return; }
      location.reload();
    } catch { alert(me?.dataset.lFailed || 'error'); } finally { document.documentElement.classList.remove('go24-busy'); }
  });
})();
