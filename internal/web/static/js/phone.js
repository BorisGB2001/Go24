// The softphone from any page of go24 (for an employee with an inner number): «Телефон» in the top bar opens its
// window (/phone, softphone.js), a click on a phone number (tel:) calls through it, and a call ringing the
// softphone shows here too with «Ответить» and «Отклонить» (the event comes through the stream of menu.js).
(() => {
  const texts = document.querySelector('[data-phone-texts]');
  if (!texts) return;
  const t = texts.dataset;
  const btn = document.querySelector('[data-phone-open]');
  const features = 'popup,width=360,height=760';
  const post = data => fetch('/telephony/cmd', { method: 'POST', body: new URLSearchParams(data), headers: { Accept: 'application/json' } })
    .then(r => r.json()).catch(() => null);

  // window.open('', name) finds the softphone window when it is open (without reloading it, a call goes on) or
  // opens a blank one — that one is sent to /phone. true — a new window was opened.
  const openPhone = dial => {
    const url = t.url + (dial ? '?dial=' + encodeURIComponent(dial) : '');
    const w = window.open('', 'go24phone', features);
    if (!w) { location.href = url; return true; } // pop-ups blocked: the phone in this tab
    let blank = false;
    try { blank = w.location.href === 'about:blank'; } catch { /* the softphone's own page (https) */ }
    if (blank) w.location.href = url;
    else w.focus();
    return blank;
  };
  const dial = number => {
    if (openPhone(number)) return;
    post({ cmd: 'dial', number }).then(j => { if (j?.error) alert(j.error); });
  };
  btn?.addEventListener('click', () => openPhone(''));

  document.addEventListener('click', e => {
    const a = e.target.closest?.('a[href^="tel:"]');
    if (!a || e.defaultPrevented || e.button !== 0 || e.ctrlKey || e.metaKey || e.shiftKey || e.altKey) return;
    const number = decodeURIComponent(a.getAttribute('href').slice(4)).replace(/[\s\-().]/g, '');
    if (!/^\+?[0-9*#]{1,20}$/.test(number)) return;
    e.preventDefault();
    dial(number);
  });

  // the softphone's line: an incoming call pops up here, the button shows a call going on
  let pop = null;
  const close = () => { pop?.remove(); pop = null; };
  const button = (cls, text, fn) => { const b = document.createElement('button'); b.type = 'button'; b.className = cls; b.textContent = text; b.addEventListener('click', fn); return b; };
  addEventListener('go24:call', e => {
    const d = e.detail || {};
    if (d.kind !== 'phone') return;
    if (btn) {
      btn.dataset.state = d.state === 'offline' || d.state === 'online' || d.state === 'idle' ? '' : d.state;
      btn.title = d.state === 'talking' ? t.talking.replace('%s', d.from || d.number || '') : t.open;
    }
    if (d.state !== 'ringing' || d.dir !== 'in') { close(); return; }
    close();
    pop = document.createElement('div');
    pop.className = 'ringpop';
    if (d.photo) { const img = document.createElement('img'); img.src = d.photo; img.alt = ''; pop.append(img); }
    const who = document.createElement('div');
    who.className = 'ringwho';
    const b = document.createElement('b');
    b.textContent = t.calling.replace('%s', d.from || d.number || '');
    if (d.url) { const a = document.createElement('a'); a.href = d.url; a.target = '_blank'; a.rel = 'noopener'; a.append(b); who.append(a); } // the caller's card
    else who.append(b);
    if (d.user && d.number) { const s = document.createElement('span'); s.textContent = d.number; who.append(s); }
    const bar = document.createElement('div');
    bar.className = 'row';
    bar.append(button('primary', t.answer, () => { post({ cmd: 'answer', phone: d.phone }); openPhone(''); close(); }));
    bar.append(button('danger', t.decline, () => { post({ cmd: 'hangup', phone: d.phone }); close(); }));
    pop.append(who, bar);
    document.body.append(pop);
  });
})();
