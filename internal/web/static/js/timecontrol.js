// «Контроль времени»: while the working day runs, the page tells the server the person is active —
// on opening and then on any activity, at most once a minute for all the tabs (the last report is kept in
// localStorage). The server counts a long gap as an absence; then the question «where were you» is shown on top.
(() => {
  const KEY = 'go24.tc.alive', EVERY = 60000;
  const last = () => { try { return +localStorage.getItem(KEY) || 0; } catch { return 0; } };
  const mark = (t) => { try { localStorage.setItem(KEY, String(t)); } catch { /* private mode: every tab reports */ } };
  let busy = false, mine = 0;

  function ask(idle) {
    const tpl = document.getElementById('idle-tpl'), main = document.querySelector('main.content');
    if (!tpl || !main || document.querySelector('main.content > .idle-ask')) return;
    const box = tpl.content.firstElementChild.cloneNode(true);
    box.querySelector('form').action = '/worktime/idle/' + idle.id;
    const text = box.querySelector('span');
    text.textContent = text.textContent.replace('{from}', idle.from).replace('{to}', idle.to).replace('{dur}', idle.dur);
    main.prepend(box);
  }

  async function alive() {
    const now = Date.now();
    if (busy || now - Math.max(last(), mine) < EVERY) return;
    busy = true;
    mine = now;
    mark(now);
    try {
      const res = await fetch('/worktime/alive', { method: 'POST', headers: { Accept: 'application/json' } });
      const data = res.ok ? await res.json() : {};
      if (data.idle && data.idle.ask) ask(data.idle);
    } catch { /* offline: the gap is counted on the next report */ }
    busy = false;
  }

  for (const ev of ['mousemove', 'mousedown', 'keydown', 'scroll', 'touchstart', 'focus']) {
    window.addEventListener(ev, alive, { passive: true, capture: true });
  }
  alive();
})();
