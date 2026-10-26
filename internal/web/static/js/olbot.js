// «Открытые линии → Виджеты», the line's chat bot «Интерактивное меню»: the menu's items as a tree — a title, an action
// (answer with a text, a submenu, to the operators, close the dialogue), its text and the submenu's items; on saving the
// tree goes to the form's bot_menu as JSON.
(() => {
  document.querySelectorAll('[data-olbot]').forEach(box => {
    const tpl = box.querySelector('[data-olbot-item]');
    const field = box.querySelector('[name="bot_menu"]');
    const top = box.querySelector('[data-olbot-list]');
    const item = (list, x) => {
      const el = tpl.content.firstElementChild.cloneNode(true);
      const v = k => el.querySelector('[data-k="' + k + '"]');
      v('title').value = x.title || '';
      v('action').value = x.action || 'text';
      v('text').value = x.text || '';
      const sub = el.querySelector('.olbot-sub'), kids = sub.querySelector('[data-olbot-list]');
      const shape = () => {
        const a = v('action').value;
        sub.hidden = a !== 'menu';
        v('text').placeholder = v('text').dataset['ph' + a[0].toUpperCase() + a.slice(1)] || '';
        if (a === 'menu' && !kids.children.length) item(kids, {});
      };
      v('action').addEventListener('change', shape);
      (x.items || []).forEach(y => item(kids, y));
      list.appendChild(el);
      shape();
      return el;
    };
    box.addEventListener('click', e => {
      const add = e.target.closest('[data-olbot-add]'), del = e.target.closest('[data-olbot-del]');
      if (add) item(add.parentElement.querySelector(':scope > [data-olbot-list]'), {}).querySelector('[data-k="title"]').focus();
      if (del) del.closest('.olbot-item').remove();
    });
    let menu = [];
    try { menu = JSON.parse(field.value); } catch { /* none */ }
    (Array.isArray(menu) ? menu : []).forEach(x => item(top, x));
    const read = list => [...list.children].map(el => {
      const v = k => el.querySelector('[data-k="' + k + '"]').value.trim();
      const x = { title: v('title'), action: v('action'), text: v('text') };
      if (x.action === 'menu') x.items = read(el.querySelector('.olbot-sub > [data-olbot-list]'));
      return x;
    }).filter(x => x.title || x.text || (x.items && x.items.length));
    box.closest('form').addEventListener('submit', () => { field.value = JSON.stringify(read(top)); });
  });
})();
