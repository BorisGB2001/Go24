// go24 «Виджет на сайт»: a button on any website that opens a chat with go24's operators and a contact form.
// It lives in a shadow root, so the site's styles do not reach it. The visitor's token stays in this site's storage.
(() => {
  const cfg = /*CONFIG*/{};
  if (window.__go24widget) return;
  window.__go24widget = true;
  const T = cfg.t, api = cfg.base + '/w/' + cfg.id + '/api/';
  const key = 'go24.widget.' + cfg.id;
  let token = '';
  try { token = localStorage.getItem(key) || ''; } catch (e) { /* no storage: a new dialogue each time */ }
  let last = 0, timer = null;

  const host = document.createElement('div');
  host.style.cssText = 'position:fixed;right:20px;bottom:20px;z-index:2147483000';
  const root = host.attachShadow({ mode: 'open' });
  root.innerHTML = `<style>
    :host{all:initial;font:14px/1.4 system-ui,-apple-system,Segoe UI,Roboto,sans-serif}
    .btn{width:56px;height:56px;border-radius:50%;border:0;background:#${cfg.color};color:#fff;font-size:26px;cursor:pointer;box-shadow:0 4px 14px rgba(0,0,0,.25);position:relative}
    .dot{position:absolute;top:2px;right:2px;min-width:18px;height:18px;border-radius:9px;background:#e53935;color:#fff;font-size:11px;line-height:18px}
    .box{position:absolute;right:0;bottom:70px;width:330px;max-width:calc(100vw - 40px);height:460px;max-height:calc(100vh - 110px);background:#fff;color:#222;border-radius:12px;box-shadow:0 8px 30px rgba(0,0,0,.25);display:flex;flex-direction:column;overflow:hidden}
    .head{background:#${cfg.color};color:#fff;padding:12px 14px;font-weight:600;display:flex;gap:8px}
    .head button{background:none;border:0;color:#fff;cursor:pointer;font:inherit;opacity:.8}.head button.on{opacity:1;text-decoration:underline}
    .msgs{flex:1;overflow:auto;padding:10px;display:flex;flex-direction:column;gap:6px;background:#f6f7f9}
    .m{max-width:80%;padding:7px 10px;border-radius:10px;white-space:pre-wrap;word-wrap:break-word}
    .v{align-self:flex-end;background:#${cfg.color};color:#fff}.o{align-self:flex-start;background:#fff;border:1px solid #e3e5e8}
    .who{font-size:11px;opacity:.7;margin-bottom:2px}
    form{display:flex;gap:6px;padding:8px;border-top:1px solid #e3e5e8;margin:0}
    textarea,input{flex:1;border:1px solid #d0d4d9;border-radius:8px;padding:7px;font:inherit;resize:none;box-sizing:border-box;width:100%}
    form button,.send{border:0;border-radius:8px;background:#${cfg.color};color:#fff;padding:0 12px;cursor:pointer;font:inherit}
    .contact{flex-direction:column;padding:14px;gap:8px;flex:1;overflow:auto;border:0}.contact .send{padding:9px}
    .note{font-size:12px;color:#666}.err{color:#c62828;font-size:12px}
    .rate{padding:8px 10px;border-top:1px solid #e3e5e8;font-size:13px;display:flex;flex-direction:column;gap:6px}
    .rate .q{display:flex;align-items:center;gap:6px}.rate .q span{flex:1}
    .rate .vb{background:none;border:1px solid #d0d4d9;border-radius:8px;cursor:pointer;font-size:16px;padding:2px 8px}.rate .vb.on{border-color:#${cfg.color};background:#${cfg.color}22}
    .rate form{padding:0;border:0}.hist{flex-direction:column;padding:14px;gap:8px;border:0}.hist .send{padding:9px}
  </style>
  <button class="btn" title="${esc(T.open)}">💬<span class="dot" hidden></span></button>
  <div class="box" hidden>
    <div class="head"><button data-tab="chat" class="on">${esc(T.chat)}</button><button data-tab="contact">${esc(T.contact)}</button><span style="flex:1"></span><button data-history title="${esc(T.history)}" hidden>✉</button><button data-close>✕</button></div>
    <div class="msgs"></div>
    <form class="hist" hidden>
      <div class="note">${esc(T.history_note)}</div>
      <input name="email" type="email" placeholder="${esc(T.email)}" maxlength="200" required>
      <div class="err" hidden></div>
      <button class="send">${esc(T.send)}</button>
    </form>
    <div class="rate" hidden><div class="q"><span></span><button type="button" class="vb" data-vote="1">👍</button><button type="button" class="vb" data-vote="-1">👎</button></div>
      <form class="vc" hidden><textarea rows="2" maxlength="1000" placeholder="${esc(T.vote_comment)}"></textarea><button>➤</button></form>
      <div class="note thanks" hidden>${esc(T.vote_thanks)}</div></div>
    <form class="say"><textarea rows="2" placeholder="${esc(T.placeholder)}" maxlength="5000"></textarea><button>➤</button></form>
    <form class="contact" hidden>
      <div class="note">${esc(T.contact_note)}</div>
      <input name="name" placeholder="${esc(T.name)}" maxlength="200" required>
      <input name="phone" placeholder="${esc(T.phone)}" maxlength="100">
      <input name="email" type="email" placeholder="${esc(T.email)}" maxlength="200">
      <textarea name="text" rows="3" placeholder="${esc(T.question)}" maxlength="5000"></textarea>
      <div class="err" hidden></div>
      <button class="send">${esc(T.send)}</button>
    </form>
  </div>`;
  function esc(s) { return String(s || '').replace(/[&<>"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c])); }
  document.body.appendChild(host);
  const $ = s => root.querySelector(s);
  const box = $('.box'), msgs = $('.msgs'), dot = $('.dot'), rate = $('.rate'), hist = $('.hist');
  let state = {};

  const call = async (op, body, method) => {
    const res = await fetch(api + op + (method === 'GET' ? '?after=' + last : ''), {
      method: method || 'POST', headers: { 'Content-Type': 'application/json', 'X-Go24-Visitor': token },
      body: method === 'GET' ? undefined : JSON.stringify(body || {}) });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(data.error || 'error');
    return data;
  };
  const visit = async () => {
    const d = await call('visit', { page: location.href });
    token = d.token;
    try { localStorage.setItem(key, token); } catch (e) { /* not kept */ }
  };
  const show = (list, unseen) => {
    let fresh = 0;
    list.forEach(m => {
      last = Math.max(last, m.id);
      const el = document.createElement('div');
      el.className = 'm ' + (m.operator ? 'o' : 'v');
      if (m.operator) { const w = document.createElement('div'); w.className = 'who'; w.textContent = m.operator; el.appendChild(w); fresh++; }
      el.appendChild(document.createTextNode(m.body));
      msgs.appendChild(el);
    });
    msgs.scrollTop = msgs.scrollHeight;
    if (unseen && fresh) { dot.textContent = fresh; dot.hidden = false; }
  };
  const poll = async () => {
    if (!token) return;
    try { const d = await call('messages', null, 'GET'); show(d.messages || [], box.hidden); if (d.state) apply(d.state); } catch (e) { /* offline: try later */ }
  };
  // the dialogue's state: rating the answer (once an operator has answered), the conversation by e-mail
  const apply = st => {
    state = st;
    rate.hidden = !st.can_vote || !$('.contact').hidden;
    rate.querySelector('.q span').textContent = st.vote_text;
    rate.querySelectorAll('[data-vote]').forEach(b => b.classList.toggle('on', +b.dataset.vote === st.vote));
    $('[data-history]').hidden = !st.history;
    const em = hist.querySelector('[name="email"]');
    if (st.email && !em.value) em.value = st.email;
  };
  const schedule = () => { clearInterval(timer); timer = setInterval(poll, box.hidden ? 30000 : 4000); };
  if (cfg.greeting && !token) show([{ id: 0, operator: cfg.name, body: cfg.greeting }], false);
  if (token) poll();
  schedule();

  $('.btn').addEventListener('click', () => { box.hidden = !box.hidden; dot.hidden = true; schedule(); if (!box.hidden) poll(); });
  $('[data-close]').addEventListener('click', () => { box.hidden = true; schedule(); });
  root.querySelectorAll('[data-tab]').forEach(b => b.addEventListener('click', () => {
    const chat = b.dataset.tab === 'chat';
    msgs.hidden = !chat; $('.say').hidden = !chat; $('.contact').hidden = chat; hist.hidden = true;
    rate.hidden = !chat || !state.can_vote;
    root.querySelectorAll('[data-tab]').forEach(x => x.classList.toggle('on', x === b));
  }));
  rate.querySelectorAll('[data-vote]').forEach(b => b.addEventListener('click', async () => {
    try {
      await call('vote', { vote: +b.dataset.vote });
      apply({ ...state, vote: +b.dataset.vote });
      $('.vc').hidden = false; rate.querySelector('.thanks').hidden = false;
    } catch (err) { alert(err.message); }
  }));
  $('.vc').addEventListener('submit', async e => {
    e.preventDefault();
    const t = $('.vc textarea');
    try { await call('vote', { vote: state.vote, text: t.value }); $('.vc').hidden = true; } catch (err) { alert(err.message); }
  });
  $('[data-history]').addEventListener('click', () => {
    const open = hist.hidden;
    hist.hidden = !open; msgs.hidden = open; $('.say').hidden = open; $('.contact').hidden = true;
    rate.hidden = open || !state.can_vote;
    root.querySelectorAll('[data-tab]').forEach(x => x.classList.toggle('on', !open && x.dataset.tab === 'chat'));
  });
  hist.addEventListener('submit', async e => {
    e.preventDefault();
    const err = hist.querySelector('.err');
    err.hidden = true;
    try {
      await call('history', { email: hist.querySelector('[name="email"]').value });
      err.textContent = T.history_sent; err.style.color = '#2e7d32'; err.hidden = false;
    } catch (x) { err.textContent = x.message; err.style.color = ''; err.hidden = false; }
  });
  const ta = $('.say textarea');
  ta.addEventListener('keydown', e => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); $('.say').requestSubmit(); } });
  $('.say').addEventListener('submit', async e => {
    e.preventDefault();
    const text = ta.value.trim();
    if (!text) return;
    try { if (!token) await visit(); await call('say', { body: text }); ta.value = ''; poll(); } catch (err) { alert(err.message); }
  });
  $('.contact').addEventListener('submit', async e => {
    e.preventDefault();
    const f = e.target, err = f.querySelector('.err');
    try {
      if (!token) await visit();
      const v = n => f.querySelector('[name="' + n + '"]').value; // form.name is the form's own name, not the field
      await call('contact', { name: v('name'), phone: v('phone'), email: v('email'), text: v('text') });
      f.innerHTML = '<div class="note">' + esc(T.thanks) + '</div>';
    } catch (x) { err.textContent = x.message; err.hidden = false; }
  });
})();
