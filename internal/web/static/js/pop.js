// Pop-ups of go24: one at a time — opening a menu, a hint or a window closes the others; a click
// anywhere outside it or Esc closes it. Menus are <details> whose content floats (positioned absolute or fixed;
// data-pop="keep" — a panel that stays open); pop-ups built by scripts tell of themselves:
//   go24Pop(el, close, owner) — el is open, close() shuts it; owner — what it belongs to (the field of a picker, the
//   button of a menu): a click in el counts as a click on the owner, so the menu holding the owner stays open.
(() => {
  const built = new Set(); // {el, close, owner}
  const floats = d => !d.matches('[data-pop="keep"]') && [...d.children].some(c => c.tagName !== 'SUMMARY' && /^(absolute|fixed)$/.test(getComputedStyle(c).position));
  // does pop-up p hold x (it is inside p or inside a pop-up of something in p)? A modal window over p holds its clicks.
  const holds = (p, x, seen = 0) => {
    if (!x || seen > 8) return false;
    if (p === x || p.contains(x)) return true;
    const dlg = x.closest?.('dialog[open]');
    if (dlg && !dlg.contains(p)) return true;
    for (const b of built) if (b.el !== p && b.el.contains(x) && holds(p, b.owner, seen + 1)) return true;
    return false;
  };
  // closes every pop-up (inside scope) that does not hold x; returns how many were open
  const closeBut = (x, scope = document) => {
    let n = 0;
    scope.querySelectorAll('details[open]').forEach(d => { if (floats(d) && !holds(d, x)) { d.open = false; n++; } });
    for (const b of [...built]) {
      if (!b.el.isConnected || b.el.hidden) { built.delete(b); continue; }
      if (holds(b.el, x) || !scope.contains(b.el)) continue;
      built.delete(b);
      b.close();
      n++;
    }
    return n;
  };
  window.go24Pop = (el, close, owner) => {
    closeBut(owner || el);
    for (const b of built) if (b.el === el) built.delete(b);
    built.add({ el, close, owner });
  };
  document.addEventListener('toggle', e => {
    const d = e.target;
    if (!d.matches?.('details') || !floats(d)) return;
    if (d.open) closeBut(d);
    else d.querySelectorAll('details[open]').forEach(x => { if (floats(x)) x.open = false; }); // its sub-menus too
  }, true);
  document.addEventListener('pointerdown', e => closeBut(e.target), true);
  document.addEventListener('keydown', e => {
    if (e.key !== 'Escape' || e.defaultPrevented) return;
    // in a modal window: its own pop-ups close first, not the window (nor the menu it was opened from)
    const dlg = [...document.querySelectorAll('dialog[open]')].pop();
    if (closeBut(null, dlg || document)) e.preventDefault();
  });
})();
