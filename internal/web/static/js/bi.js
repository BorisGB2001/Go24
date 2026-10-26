// «BI-аналитика»: «Печать» prints the dashboard (bi.css leaves it alone on the page); while a dashboard is edited its
// widgets are dragged to a new order, which is saved at once.
document.querySelectorAll('[data-bi-print]').forEach(b => b.addEventListener('click', () => {
  b.closest('details')?.removeAttribute('open');
  print();
}));
(() => {
  const grid = document.querySelector('.bi-grid.editing[data-bi-grid]');
  if (!grid) return;
  let dragged = null;
  grid.addEventListener('dragstart', e => {
    dragged = e.target.closest('.bi-w');
    if (!dragged) return;
    dragged.classList.add('dragging');
    e.dataTransfer.effectAllowed = 'move';
    e.dataTransfer.setData('text/plain', dragged.dataset.id);
  });
  grid.addEventListener('dragover', e => {
    const over = e.target.closest('.bi-w');
    if (!dragged || !over || over === dragged) return;
    e.preventDefault();
    const r = over.getBoundingClientRect();
    const after = e.clientX > r.left + r.width / 2 || e.clientY > r.bottom - r.height / 4;
    over.parentNode.insertBefore(dragged, after ? over.nextSibling : over);
  });
  grid.addEventListener('dragend', () => {
    if (!dragged) return;
    dragged.classList.remove('dragging');
    dragged = null;
    const body = new URLSearchParams();
    grid.querySelectorAll('.bi-w').forEach(w => body.append('ids', w.dataset.id));
    fetch(grid.dataset.biGrid, { method: 'POST', headers: { Accept: 'application/json', 'X-Go24-Form': '1' }, body });
  });
})();
