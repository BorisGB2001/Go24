// «Терминал учёта времени»: the PIN pad, the clock, and the way back to the pad after a while.
(() => {
  const root = document.querySelector('.wterm');
  if (!root) return;
  const pin = root.querySelector('.wterm-pin');
  root.querySelectorAll('[data-key]').forEach(b => b.addEventListener('click', () => {
    if (!pin) return;
    if (b.dataset.key === 'C') pin.value = pin.value.slice(0, -1);
    else if (pin.value.length < 8) pin.value += b.dataset.key;
    pin.focus();
  }));
  const clock = root.querySelector('[data-clock]');
  if (clock) setInterval(() => {
    clock.textContent = new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  }, 5000);
  const back = Number(root.dataset.return);
  if (back) setTimeout(() => { location.href = location.pathname; }, back * 1000);
})();
