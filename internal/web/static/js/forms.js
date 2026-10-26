// «Повторять задачу»: the schedule shows when ticked; its fields follow the chosen period.
(() => {
  const sync = box => {
    const on = box.querySelector('[data-repeat-on]').checked;
    box.querySelector('[data-repeat-body]').hidden = !on;
    const period = box.querySelector('[data-period]').value;
    box.querySelectorAll('[data-for]').forEach(el => { el.hidden = !el.dataset.for.split(' ').includes(period); });
  };
  document.querySelectorAll('[data-repeat]').forEach(sync);
  document.addEventListener('change', e => {
    const box = e.target.closest?.('[data-repeat]');
    if (box && e.target.matches('[data-repeat-on], [data-period]')) sync(box);
  });
})();

// «Шаблон для новых сотрудников»: the responsible is each new employee, the template does not repeat
(() => {
  const sync = box => {
    box.form.querySelectorAll('[data-nnu]').forEach(el => { el.hidden = box.checked; });
    box.form.querySelectorAll('[data-nnu-hint]').forEach(el => { el.hidden = !box.checked; });
  };
  document.addEventListener('change', e => { if (e.target.matches?.('[data-new-users]')) sync(e.target); });
})();

// form[data-submit-now]: sent as the page opens (an application's page gets its tokens in the frame)
document.querySelectorAll('form[data-submit-now]').forEach(f => f.submit());

// details[data-app-tab]: an application's tab in a card opens its frame the first time it is opened
document.addEventListener('toggle', e => {
  const d = e.target;
  if (!d.matches?.('details[data-app-tab]') || !d.open) return;
  const f = d.querySelector('iframe[data-src]');
  if (f && !f.src) f.src = f.dataset.src;
}, true);

// [data-toggle="id"]: shows or hides the element (an inline edit form)
document.addEventListener('click', e => {
  const b = e.target.closest?.('[data-toggle]');
  if (!b) return;
  const el = document.getElementById(b.dataset.toggle);
  if (el) { el.hidden = !el.hidden; if (!el.hidden) el.querySelector('input')?.focus(); }
});

// form[data-confirm] (or its button): asks before sending (deleting something)
document.addEventListener('submit', e => {
  const msg = e.submitter?.dataset?.confirm || e.target.dataset?.confirm; // or the button pressed
  if (msg && !confirm(msg)) e.preventDefault();
});

// Every POST form is sent in the background: on a mistake (a taken project key, an empty name…) the page stays
// as it is, with everything typed, and the message shows above the fields; on success the page goes where the
// server says, as before. A form opts out with data-native; file uploads and forms other scripts handle are left alone.
(() => {
  document.addEventListener('submit', async e => {
    const form = e.target;
    // attributes, not form.method / form.target / form.enctype: a field of that name would stand for them
    const attr = n => (form instanceof HTMLFormElement && form.getAttribute(n) || '').toLowerCase();
    if (e.defaultPrevented || !(form instanceof HTMLFormElement) || attr('method') !== 'post' || attr('target') ||
        'native' in form.dataset || attr('enctype') === 'multipart/form-data' || !window.fetch) return;
    e.preventDefault();
    if (form.dataset.busy) return;
    // the pressed button's formaction, else the form's own address: form.action is the field when a field or a button
    // is named «action» (the task's «Начать выполнение», the work day's buttons) — GO24DEV-349
    const target = new URL(e.submitter?.getAttribute('formaction') || form.getAttribute('action') || location.href, location.href).href;
    form.dataset.busy = '1';
    // the fields with the button pressed (name=value) — before the buttons are disabled: a disabled one is not sent
    const body = new URLSearchParams(new FormData(form, e.submitter));
    const buttons = [...form.querySelectorAll('button:not([type=button])')];
    buttons.forEach(b => { b.disabled = true; });
    // the work shows: a running line on top, the pressed button spins (a second press does nothing)
    const pressed = e.submitter || form.querySelector('button:not([type=button])');
    document.documentElement.classList.add('go24-busy');
    pressed?.classList.add('busy');
    const done = () => {
      delete form.dataset.busy;
      buttons.forEach(b => { b.disabled = false; });
      document.documentElement.classList.remove('go24-busy');
      pressed?.classList.remove('busy');
    };
    let res, data = null;
    try {
      res = await fetch(target, { method: 'POST', headers: { 'X-Go24-Form': '1' }, body });
      if (res.status >= 502 && res.status <= 504) { // go24 restarting behind the proxy: once more, a moment later
        await new Promise(r => setTimeout(r, window.go24RetryMs ?? 2000));
        res = await fetch(target, { method: 'POST', headers: { 'X-Go24-Form': '1' }, body });
      }
      if ((res.headers.get('Content-Type') || '').includes('application/json')) data = await res.json().catch(() => null);
    } catch { // the server could not be reached this way (e.g. a sign-in redirect): send it the usual way
      done();
      HTMLFormElement.prototype.submit.call(form);
      return;
    }
    if (res.ok && data?.redirect) {
      const u = new URL(data.redirect, location.href);
      if (u.pathname + u.search === location.pathname + location.search) { location.hash = u.hash; location.reload(); } else location.href = u.href;
      return;
    }
    if (res.ok && !data) { // the server answered with a page
      const html = await res.text();
      document.open(); document.write(html); document.close();
      return;
    }
    done();
    let box = form.querySelector('[data-error]');
    if (!box) { box = document.createElement('div'); box.className = 'form-error'; box.dataset.error = ''; form.prepend(box); }
    const text = data ? '' : await res.text().catch(() => '');
    box.textContent = data?.error || (res.status === 403 || res.status === 404 ? text : '') ||
      (form.dataset.failed || document.documentElement.dataset.failed || 'Error') + ' (' + res.status + ')';
    if (!data?.error) { // an answer go24 did not mean (a proxy's, a crash): into go24's journal, to find the cause
      const report = new URLSearchParams({ page: location.pathname + location.search, url: new URL(target).pathname, status: res.status, body: text.slice(0, 500) });
      fetch('/client-error', { method: 'POST', body: report, keepalive: true }).catch(() => {});
    }
    box.hidden = false;
    box.scrollIntoView?.({ behavior: 'smooth', block: 'nearest' });
  });
})();

// [data-copy="text"]: copies the text and shows «Скопировано». navigator.clipboard works only on https and localhost;
// on plain http (go24 at http://192.168.1.15:8080) the text is copied through a hidden field and execCommand,
// and when even that is refused the text is offered selected in a prompt.
window.go24Copy = async text => {
  try { await navigator.clipboard.writeText(text); return true; } catch { /* not a secure context */ }
  const f = document.createElement('textarea');
  f.value = text; f.readOnly = true;
  f.style.cssText = 'position:fixed;top:0;left:0;opacity:0';
  document.body.append(f);
  f.focus(); f.select();
  let ok = false;
  try { ok = document.execCommand('copy'); } catch { ok = false; }
  f.remove();
  if (!ok) window.prompt('', text);
  return ok;
};
document.addEventListener('click', async e => {
  const c = e.target.closest?.('[data-copy]');
  if (!c) return;
  e.preventDefault();
  if (!await window.go24Copy(c.dataset.copy)) return;
  clearTimeout(c.copyTimer);
  if (!c.classList.contains('copied')) c.dataset.was = c.title;
  c.title = c.closest('[data-copied]')?.dataset.copied || ''; c.classList.add('copied');
  c.dataset.tip = c.title;
  c.copyTimer = setTimeout(() => { c.title = c.dataset.was || ''; c.classList.remove('copied'); }, 1500);
});
