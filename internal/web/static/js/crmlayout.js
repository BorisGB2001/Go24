// CRM cards, «Настроить карточку»: the card's fields are dragged between its sections (and to «Скрытые
// поля») or moved by the arrows; sections are added, renamed and deleted (their fields go to the section above); the
// form sends the result as JSON in "layout".
(() => {
  document.querySelectorAll('form[data-crmlayout]').forEach(form => {
    const secs = form.querySelector('.crmlsecs');
    const hidden = form.querySelector('[data-hidden] .crmlfields');
    const lists = () => [...secs.querySelectorAll('.crmlfields'), hidden];
    let dragged = null;

    // ↑ at the top of a section goes to the end of the one above, ↓ at its bottom — to the start of the one below
    const move = (li, up) => {
      const sib = up ? li.previousElementSibling : li.nextElementSibling;
      if (sib) {
        li.parentNode.insertBefore(li, up ? sib : sib.nextSibling);
      } else {
        const all = lists();
        const to = all[all.indexOf(li.parentNode) + (up ? -1 : 1)];
        if (!to) return;
        if (up) to.appendChild(li); else to.insertBefore(li, to.firstChild);
      }
      li.querySelector(up ? '[data-up]' : '[data-down]')?.focus();
    };
    const addSection = () => {
      const sec = document.createElement('div');
      sec.className = 'crmlsec';
      sec.dataset.sec = '';
      sec.dataset.code = '';
      const tpl = secs.querySelector('[data-sec]');
      sec.innerHTML = tpl ? tpl.querySelector('.row').outerHTML : '<div class="row nowrap"><input type="text" class="crmlsec-name"></div>';
      const name = sec.querySelector('.crmlsec-name');
      name.value = form.dataset.newName || '';
      name.placeholder = form.dataset.newName || '';
      const ul = document.createElement('ul');
      ul.className = 'crmlfields';
      sec.appendChild(ul);
      secs.appendChild(sec);
      name.focus();
      name.select?.();
    };

    form.addEventListener('click', e => {
      const b = e.target.closest('button[type=button]');
      if (!b) return;
      const li = b.closest('li');
      if (b.matches('[data-up]')) move(li, true);
      else if (b.matches('[data-down]')) move(li, false);
      else if (b.matches('[data-hide]')) { hidden.appendChild(li); li.querySelector('[data-show]')?.focus(); }
      else if (b.matches('[data-show]')) { secs.querySelector('.crmlfields')?.appendChild(li); li.querySelector('[data-hide]')?.focus(); }
      else if (b.matches('[data-sec-add]')) addSection();
      else if (b.matches('[data-sec-del]')) {
        const sec = b.closest('[data-sec]');
        const all = [...secs.querySelectorAll('[data-sec]')];
        if (all.length < 2) return; // the card keeps one section at least
        const to = (all[all.indexOf(sec) - 1] || all[1]).querySelector('.crmlfields');
        sec.querySelectorAll('.crmlfields > li').forEach(x => to.appendChild(x));
        sec.remove();
      }
    });

    form.addEventListener('dragstart', e => {
      dragged = e.target.closest?.('li[data-code]');
      if (!dragged) return;
      dragged.classList.add('dragging');
      e.dataTransfer?.setData('text/plain', '');
    });
    form.addEventListener('dragover', e => {
      if (!dragged) return;
      const li = e.target.closest?.('li[data-code]');
      const ul = e.target.closest?.('.crmlfields') || e.target.closest?.('.crmlsec')?.querySelector('.crmlfields');
      if (!ul) return;
      e.preventDefault();
      if (li && li !== dragged) {
        const r = li.getBoundingClientRect();
        li.parentNode.insertBefore(dragged, e.clientY > r.top + r.height / 2 ? li.nextSibling : li);
      } else if (!li) {
        ul.appendChild(dragged); // an empty section or below its last field
      }
    });
    form.addEventListener('drop', e => e.preventDefault());
    form.addEventListener('dragend', () => {
      dragged?.classList.remove('dragging');
      dragged = null;
    });

    form.addEventListener('submit', () => {
      const codes = ul => [...ul.querySelectorAll(':scope > li[data-code]')].map(li => li.dataset.code);
      const layout = {
        sections: [...secs.querySelectorAll('[data-sec]')].map(sec => {
          const name = sec.querySelector('.crmlsec-name');
          const code = sec.dataset.code || '';
          return { code, name: (name?.value || '').trim() || (code ? '' : name?.placeholder || ''), fields: codes(sec.querySelector('.crmlfields')) };
        }),
        hidden: codes(hidden),
      };
      form.querySelector('input[name=layout]').value = JSON.stringify(layout);
    });
  });
})();
