// «CRM-формы», the editor: the fields' lines are dragged into the form's order (the order of their «key» inputs is
// the one saved); the custom fields of the other card hide while a scheme is chosen; the embedding code is copied on
// a click.
(() => {
  const form = document.querySelector('form[data-crmform]');
  if (!form) return;
  const scheme = form.querySelector('[data-scheme]');
  const show = () => form.querySelectorAll('[data-for-scheme]').forEach(el => { el.hidden = el.dataset.forScheme !== scheme.value; });
  scheme?.addEventListener('change', show);
  if (scheme) show();
  const body = form.querySelector('.crmform-fields tbody');
  let dragged = null;
  body?.addEventListener('dragstart', e => {
    dragged = e.target.closest('tr');
    if (!dragged) return;
    dragged.classList.add('dragging');
    e.dataTransfer?.setData('text/plain', '');
  });
  body?.addEventListener('dragover', e => {
    const tr = e.target.closest('tr');
    if (!dragged || !tr || tr === dragged) return;
    e.preventDefault();
    const r = tr.getBoundingClientRect();
    body.insertBefore(dragged, e.clientY > r.top + r.height / 2 ? tr.nextSibling : tr);
  });
  body?.addEventListener('drop', e => e.preventDefault());
  body?.addEventListener('dragend', () => { dragged?.classList.remove('dragging'); dragged = null; });
  document.querySelectorAll('textarea[data-copy]').forEach(t => t.addEventListener('click', () => {
    t.select();
    navigator.clipboard?.writeText(t.value).catch(() => {});
  }));
})();
