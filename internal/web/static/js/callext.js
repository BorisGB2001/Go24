// «Видеовстреча через …»: Zoom, Яндекс Телемост, VK Звонки (a new meeting by their API), Telegram, Max (the
// employee's own link). The list shows when some service is there; in a task or a chat the invitation with the link
// goes there and the meeting opens, in a form (the place of a meeting, a CRM activity) the link goes into its field.
(() => {
  let services; // asked once per page
  const load = () => services ??= fetch('/call/external', { headers: { Accept: 'application/json' }, cache: 'no-store' })
    .then(r => r.ok ? r.json() : { services: [] }).then(d => d.services || []).catch(() => []);

  const show = async box => {
    const list = await load();
    box.hidden = !list.length;
    const ul = box.querySelector('[data-call-ext-list]');
    if (!ul || ul.childElementCount) return;
    list.forEach(s => {
      const b = document.createElement('button');
      b.type = 'button';
      b.className = 'ghost';
      b.dataset.callExtService = s.service;
      b.textContent = s.name;
      ul.append(b);
    });
  };
  const all = () => document.querySelectorAll('[data-call-ext]').forEach(show);

  // into a form: the link replaces an empty field or goes after its text; a CRM activity becomes a meeting
  const fill = (box, url) => {
    const form = box.closest('form');
    const field = form?.elements[box.dataset.fill];
    if (!field) return;
    const v = field.value.trim();
    field.value = !v ? url : v + (field.tagName === 'TEXTAREA' ? '\n' : ' ') + url;
    field.dispatchEvent(new Event('input', { bubbles: true }));
    const meeting = form.querySelector('input[name="kind"][value="meeting"]');
    if (meeting) meeting.checked = true;
  };

  document.addEventListener('click', async e => {
    const b = e.target.closest('[data-call-ext-service]');
    const box = b?.closest('[data-call-ext]');
    if (!box) return;
    e.preventDefault();
    const filling = box.dataset.fill !== undefined;
    const win = filling ? null : window.open('', '_blank'); // opened by the click: no popup blocker
    b.disabled = true;
    const body = new URLSearchParams({ service: b.dataset.callExtService });
    const title = box.closest('form')?.elements.subject?.value || box.closest('form')?.elements.title?.value;
    if (title) body.set('title', title);
    let d = {};
    let ok = false;
    try {
      const r = await fetch('/call/external' + (box.dataset.callExt ? '?' + box.dataset.callExt : ''),
        { method: 'POST', headers: { Accept: 'application/json', 'X-Requested-With': 'XMLHttpRequest' }, body });
      d = await r.json().catch(() => ({}));
      ok = r.ok && d.url;
    } catch { /* offline */ }
    b.disabled = false;
    box.open = false;
    if (!ok) {
      win?.close();
      alert(d.error || box.querySelector('summary')?.title || '');
      return;
    }
    if (filling) fill(box, d.url);
    else if (win) win.location = d.url;
    else location.href = d.url;
  });
  // a chat opened later brings its own list
  new MutationObserver(() => document.querySelectorAll('[data-call-ext][hidden]').forEach(show))
    .observe(document.body, { childList: true, subtree: true });
  all();
})();
