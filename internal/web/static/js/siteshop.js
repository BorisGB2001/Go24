// «Сайты», the block «Каталог товаров»: the visitor's cart — «В корзину» adds a product (the chosen variation), the
// cart under the goods shows the lines with their quantities and the total, and the order form sends it as
// cart=id:qty,… The cart is kept in the browser per block until the order is sent.
(() => {
  const lang = document.documentElement.lang || undefined;
  const fmt = n => n.toLocaleString(lang, { minimumFractionDigits: 0, maximumFractionDigits: 2 });
  const storeOf = id => 'go24cart:' + location.pathname + ':' + id;
  const load = id => {
    try { return JSON.parse(localStorage.getItem(storeOf(id))) || []; } catch { return []; }
  };
  const save = (id, lines) => {
    try { lines.length ? localStorage.setItem(storeOf(id), JSON.stringify(lines)) : localStorage.removeItem(storeOf(id)); } catch {}
  };

  document.querySelectorAll('[data-lp-shop]:not([data-lp-ready])').forEach(box => {
    box.dataset.lpReady = '1';
    const id = box.dataset.lpShop, form = box.querySelector('[data-lp-cart]');
    if (!form) return;
    if (box.querySelector('[data-lp-sent]')) save(id, []);
    let lines = load(id);
    const draw = () => {
      const table = form.querySelector('[data-lp-lines]');
      table.replaceChildren();
      let total = 0, cur = '';
      lines.forEach((l, i) => {
        const tr = table.insertRow();
        tr.insertCell().textContent = l.name;
        const qty = document.createElement('input');
        Object.assign(qty, { type: 'number', min: 1, max: 9999, value: l.qty });
        qty.addEventListener('change', () => {
          l.qty = Math.min(9999, Math.max(1, parseInt(qty.value, 10) || 1));
          save(id, lines);
          draw();
        });
        tr.insertCell().append(qty);
        tr.insertCell().textContent = fmt(l.price * l.qty) + ' ' + l.cur;
        const del = document.createElement('button');
        Object.assign(del, { type: 'button', textContent: '×', title: form.dataset.remove, className: 'lp-del' });
        del.addEventListener('click', () => {
          lines.splice(i, 1);
          save(id, lines);
          draw();
        });
        tr.insertCell().append(del);
        total += l.price * l.qty;
        cur = cur || l.cur;
      });
      form.querySelector('[data-lp-total]').textContent = fmt(total) + ' ' + cur;
      form.querySelector('[data-lp-cartval]').value = lines.map(l => l.id + ':' + l.qty).join(',');
      form.hidden = !lines.length;
    };
    box.addEventListener('click', e => {
      const btn = e.target.closest('[data-lp-add]');
      if (!btn) return;
      let src = btn, name = btn.dataset.name;
      const pick = btn.closest('.lp-good')?.querySelector('[data-lp-variant]');
      if (pick) {
        src = pick.selectedOptions[0];
        name += ' — ' + src.dataset.name;
      }
      const pid = pick ? src.value : btn.dataset.lpAdd;
      const have = lines.find(l => l.id === pid);
      if (have) have.qty = Math.min(9999, have.qty + 1);
      else lines.push({ id: pid, name, qty: 1, price: Number(src.dataset.price) || 0, cur: src.dataset.cur || '' });
      save(id, lines);
      draw();
      form.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
    });
    draw();
  });
})();
