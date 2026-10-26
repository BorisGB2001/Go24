// «Почта → Импорт из файла» (/mail/import): another mailbox shows its folders; the folder is asked only for «В папку»;
// a file from the computer goes with a progress bar (a .pst may be gigabytes); while an import waits or runs, the page
// looks again every 10 s.
(() => {
  const box = document.querySelector('[data-export-box]');
  box?.addEventListener('change', () => box.form.submit());
  const form = document.querySelector('[data-import-form]');
  if (form) {
    const into = form.querySelector('[data-import-into]');
    const drive = form.querySelector('[data-import-drive]');
    const file = form.querySelector('[data-import-file]');
    const sync = () => {
      into.disabled = form.elements.where.value !== 'folder';
      if (drive) file.disabled = drive.value !== '';
    };
    form.addEventListener('change', sync);
    sync();
    form.addEventListener('submit', (ev) => {
      if (!file.files.length || file.disabled) return; // a file of the drive: the plain form
      ev.preventDefault();
      const bar = form.querySelector('[data-import-progress]');
      const button = form.querySelector('button.primary');
      bar.hidden = false;
      button.disabled = true;
      const xhr = new XMLHttpRequest();
      xhr.open('POST', form.action);
      xhr.upload.onprogress = (e) => { if (e.lengthComputable) bar.value = Math.round(e.loaded * 100 / e.total); };
      xhr.onload = () => { location.href = xhr.responseURL || location.href; };
      xhr.onerror = () => { button.disabled = false; bar.hidden = true; };
      xhr.send(new FormData(form));
    });
  }
  if (document.querySelector('[data-export-busy]')) setTimeout(() => location.reload(), 10000);
})();
