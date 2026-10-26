// «Быстрые действия» in the mail settings (mail.html, mquick): each step shows only the field its action takes —
// a folder, a category, addresses, a template.
(() => {
  const show = step => {
    const what = step.querySelector('select[name^="do_"]').value;
    step.querySelectorAll('[data-for]').forEach(f => {
      f.hidden = !f.dataset.for.split(' ').includes(what);
      f.disabled = f.hidden;
    });
  };
  document.querySelectorAll('[data-quick-form] [data-step]').forEach(step => {
    show(step);
    step.addEventListener('change', e => { if (e.target.name?.startsWith('do_')) show(step); });
  });
})();
