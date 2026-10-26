// «CRM-форма» on a client's site «Встроить на сайт»:
//   <script src="https://go24…/static/js/crmform.js" data-form="https://go24…/cf/TOKEN" async></script>
// puts the form in the page where the script stands; with data-button="Текст" — a button that opens it in a window
// over the page. The form (/cf/TOKEN?embed=1) tells its height, and its frame follows.
(() => {
  const me = document.currentScript;
  const link = me?.dataset.form;
  if (!link) return;
  const src = link + (link.includes('?') ? '&' : '?') + 'embed=1&page=' + encodeURIComponent(location.href);
  const token = link.split('/cf/')[1]?.split(/[?#]/)[0];
  const frame = () => {
    const f = document.createElement('iframe');
    f.src = src;
    f.title = me.dataset.title || '';
    f.style.cssText = 'width:100%;border:0;height:480px;display:block;background:transparent';
    f.setAttribute('allowtransparency', 'true');
    return f;
  };
  const frames = [];
  addEventListener('message', e => {
    const m = e.data && e.data.go24form;
    if (!m || m !== token || !link.startsWith(e.origin)) return;
    frames.forEach(f => { if (f.contentWindow === e.source) f.style.height = Math.min(e.data.height + 2, 4000) + 'px'; });
  });
  if (!me.dataset.button) {
    const f = frame();
    frames.push(f);
    me.parentNode.insertBefore(f, me.nextSibling);
    return;
  }
  const btn = document.createElement('button');
  btn.type = 'button';
  btn.textContent = me.dataset.button;
  btn.style.cssText = 'padding:12px 24px;border:0;border-radius:8px;background:#1f6feb;color:#fff;font:inherit;font-weight:600;cursor:pointer';
  me.parentNode.insertBefore(btn, me.nextSibling);
  btn.addEventListener('click', () => {
    const shade = document.createElement('div');
    shade.style.cssText = 'position:fixed;inset:0;z-index:2147483000;background:rgba(0,0,0,.45);display:flex;align-items:flex-start;justify-content:center;overflow:auto;padding:5vh 12px';
    const box = document.createElement('div');
    box.style.cssText = 'position:relative;width:100%;max-width:560px;background:#fff;border-radius:12px;padding:8px 0';
    const close = document.createElement('button');
    close.type = 'button';
    close.textContent = '×';
    close.setAttribute('aria-label', 'Close');
    close.style.cssText = 'position:absolute;top:4px;right:8px;border:0;background:none;font-size:28px;line-height:1;cursor:pointer;color:#5b6474';
    const f = frame();
    frames.push(f);
    const shut = () => { shade.remove(); frames.splice(frames.indexOf(f), 1); removeEventListener('keydown', esc); };
    const esc = e => { if (e.key === 'Escape') shut(); };
    close.addEventListener('click', shut);
    shade.addEventListener('click', e => { if (e.target === shade) shut(); });
    addEventListener('keydown', esc);
    box.append(close, f);
    shade.append(box);
    document.body.append(shade);
  });
})();
