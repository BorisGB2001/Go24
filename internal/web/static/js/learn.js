// «Обучение»: the countdown of a test attempt limited in time — when it is over the answers are sent as they are;
// the certificate's print button.
(() => {
  const timer = document.querySelector('[data-learn-timer]');
  const form = document.getElementById('ltest');
  if (timer && form) {
    const end = Date.now() + Number(timer.dataset.learnTimer) * 1000;
    const out = timer.querySelector('span');
    const tick = () => {
      const left = Math.max(0, Math.round((end - Date.now()) / 1000));
      out.textContent = Math.floor(left / 60) + ':' + String(left % 60).padStart(2, '0');
      timer.classList.toggle('late', left < 60);
      if (left === 0) {
        form.removeAttribute('data-confirm');
        form.requestSubmit();
        return;
      }
      setTimeout(tick, 1000);
    };
    tick();
  }
  document.querySelector('[data-print-now]')?.addEventListener('click', () => print());
})();
