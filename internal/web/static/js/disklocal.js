// «Редактировать на компьютере»: the button pressed opens the program; with «Запомнить выбор» the page opens the
// remembered program by itself next time.
(() => {
  const box = document.querySelector('[data-disk-local]');
  if (!box) return;
  const key = 'go24.disk.localApp', remember = box.querySelector('[data-local-remember]');
  let saved = '';
  try { saved = localStorage.getItem(key) || ''; } catch (e) { /* no storage: nothing remembered */ }
  remember.checked = saved !== '';
  box.querySelectorAll('[data-local-app]').forEach(a => a.addEventListener('click', () => {
    try {
      if (remember.checked) localStorage.setItem(key, a.dataset.localApp); else localStorage.removeItem(key);
    } catch (e) { /* no storage */ }
  }));
  remember.addEventListener('change', () => {
    if (!remember.checked) try { localStorage.removeItem(key); } catch (e) { /* no storage */ }
  });
  const auto = saved && box.querySelector(`[data-local-app="${saved}"]`);
  if (auto) location.href = auto.getAttribute('href');
})();
