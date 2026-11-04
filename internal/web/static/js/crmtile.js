// CRM kanban, «Настроить вид карточки»: a field's line in the gear is dragged to another place; the
// tiles take the order when the view is saved.
(() => {
  document.querySelectorAll('form[data-crmtile]').forEach(form => {
    const list = form.querySelector('.crmtile-list');
    if (!list || list.dataset.ready) return;
    list.dataset.ready = '1';
    let dragged = null;
    list.addEventListener('dragstart', e => {
      dragged = e.target.closest('li');
      if (!dragged) return;
      dragged.classList.add('dragging');
      e.dataTransfer?.setData('text/plain', '');
    });
    list.addEventListener('dragover', e => {
      const li = e.target.closest('li');
      if (!dragged || !li || li === dragged) return;
      e.preventDefault();
      const r = li.getBoundingClientRect();
      li.parentNode.insertBefore(dragged, e.clientY > r.top + r.height / 2 ? li.nextSibling : li);
    });
    list.addEventListener('drop', e => e.preventDefault());
    list.addEventListener('dragend', () => {
      dragged?.classList.remove('dragging');
      dragged = null;
    });
  });
})();
