// «Почта → Экспорт» (/mail/export): another mailbox shows its folders; the folders are asked only for «Выбранные
// папки»; while an export waits or runs, the page looks again every 10 s.
(() => {
  const box = document.querySelector('[data-export-box]');
  box?.addEventListener('change', () => box.form.submit());
  const form = document.querySelector('[data-export-form]');
  const folders = document.querySelector('[data-export-folders]');
  const sync = () => { if (form && folders) folders.hidden = form.elements.scope.value === 'all'; };
  form?.addEventListener('change', sync);
  sync();
  if (document.querySelector('[data-export-busy]')) setTimeout(() => location.reload(), 10000);
})();
