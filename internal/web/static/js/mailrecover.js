// «Восстановить удалённые элементы» (/mail/recover): another mailbox shows its letters; «Выбрать все»; the buttons
// wait till a letter is chosen.
(() => {
  const box = document.querySelector('[data-recover-box]');
  box?.addEventListener('change', () => box.form.submit());
  const form = document.querySelector('[data-recover]');
  if (!form) return;
  const sync = () => {
    const n = form.querySelectorAll('tbody input[name="id"]:checked').length;
    form.querySelectorAll('[data-recover-act]').forEach(b => { b.disabled = n === 0; });
  };
  form.addEventListener('change', e => {
    if (e.target.matches('[data-checkall]')) form.querySelectorAll('tbody input[name="id"]').forEach(c => { c.checked = e.target.checked; });
    sync();
  });
  sync();
})();
