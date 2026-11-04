// «Из производственного календаря компании» ([data-holidays] — the company's holidays, «31.12» comma-separated) fills
// the holidays of the line's form, one a line (the external lines of «Настройки → Телефония»).
(() => {
  document.addEventListener('click', e => {
    const b = e.target.closest('[data-holidays]');
    if (b) b.closest('form').querySelector('[name="holidays"]').value = b.dataset.holidays.split(',').join('\n');
  });
})();
