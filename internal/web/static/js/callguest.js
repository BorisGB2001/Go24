// go24 lobby of a guest link of a call: asks every few seconds whether someone of the call has let the guest in
// (or not), then shows what came of it.
(() => {
  const box = document.querySelector('[data-guest-wait]');
  if (!box) return;
  const ask = async () => {
    try {
      const r = await fetch(box.dataset.guestWait, { headers: { Accept: 'application/json' }, cache: 'no-store' });
      const d = r.ok ? await r.json() : { state: 'gone' };
      if (d.state !== 'waiting') { location.reload(); return; }
    } catch { /* offline: ask again */ }
    setTimeout(ask, 3000);
  };
  setTimeout(ask, 3000);
})();
