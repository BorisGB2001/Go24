// «Обзвон» (/telephony/calllists/{id}): «Позвонить» tells go24 the client is dialed — the softphone calls the number
// (the tel: link, phone.js) — and the page reloads itself till the call log gives the call's result.
(() => {
  const box = document.querySelector('[data-calllist]');
  if (!box) return;
  const watch = () => setInterval(() => { if (!document.hidden) location.reload(); }, 8000);
  box.querySelector('[data-cl-dial]')?.addEventListener('click', e => {
    fetch(e.currentTarget.dataset.clDial, { method: 'POST', headers: { Accept: 'application/json' }, keepalive: true })
      .then(r => r.ok && !box.hasAttribute('data-cl-waiting') && (box.setAttribute('data-cl-waiting', ''), watch()))
      .catch(() => {});
  });
  if (box.hasAttribute('data-cl-waiting')) watch();
})();
