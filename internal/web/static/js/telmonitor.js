// «Текущие звонки»: the durations tick every second, the list comes anew every 5 seconds (not while a button is being
// pressed or the tab is hidden).
(() => {
  const box = document.querySelector('[data-telactive]');
  if (!box) return;
  const talk = (s) => {
    const h = Math.floor(s / 3600), m = Math.floor(s / 60) % 60, sec = String(s % 60).padStart(2, '0');
    return h ? `${h}:${String(m).padStart(2, '0')}:${sec}` : `${m}:${sec}`;
  };
  setInterval(() => {
    const now = Math.floor(Date.now() / 1000);
    box.querySelectorAll('[data-since]').forEach(td => { td.textContent = talk(Math.max(0, now - Number(td.dataset.since))); });
  }, 1000);
  let busy = false;
  box.addEventListener('submit', () => { busy = true; });
  setInterval(async () => {
    if (busy || document.hidden) return;
    try {
      const r = await fetch(location.pathname, { credentials: 'same-origin' });
      if (!r.ok) return;
      const fresh = new DOMParser().parseFromString(await r.text(), 'text/html').querySelector('[data-telactive]');
      if (fresh && !busy) box.innerHTML = fresh.innerHTML;
    } catch { /* offline for a moment: next time */ }
  }, 5000);
})();
