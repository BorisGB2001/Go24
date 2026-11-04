// The left menu on a phone (≤ 760px, app.css): «Меню» of the bottom bar or ☰ of the top bar slides it out over the
// page, as in the portal app; a tap on the shade, a link in it, Esc or a swipe to the left puts it back.
(() => {
  const root = document.documentElement, shade = document.querySelector('[data-drawer-close]');
  const side = document.querySelector('.side');
  if (!shade || !side) return;
  const open = on => {
    root.toggleAttribute('data-drawer', on);
    shade.hidden = !on;
  };
  document.addEventListener('click', e => {
    if (e.target.closest('[data-drawer-open]')) { open(!root.hasAttribute('data-drawer')); return; }
    if (e.target === shade || (root.hasAttribute('data-drawer') && e.target.closest('.side a[href]:not([href="#"])'))) open(false);
  });
  document.addEventListener('keydown', e => { if (e.key === 'Escape' && root.hasAttribute('data-drawer')) open(false); });
  let x0 = null;
  side.addEventListener('touchstart', e => { x0 = e.touches[0].clientX; }, { passive: true });
  side.addEventListener('touchend', e => {
    if (x0 !== null && x0 - e.changedTouches[0].clientX > 60) open(false);
    x0 = null;
  });
  // back to a wide screen: no drawer
  matchMedia('(max-width: 760px)').addEventListener('change', m => { if (!m.matches) open(false); });
})();
