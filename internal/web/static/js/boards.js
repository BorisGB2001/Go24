// go24 boards: project kanban (columns = project stages) and «Мой план» (personal columns).
// A dropped card moves at once; the change is sent to the server, and the server's answer corrects the page
// (e.g. dropping into «Сделаны» with result review leaves the card there as «Ждёт контроля»). On error the card goes back.
// The «+» form of a stage column creates a task and puts its card on top, also without a reload.
(() => {
  const flash = (text) => {
    let el = document.querySelector('.content .flash');
    if (!el) {
      el = document.createElement('div');
      el.className = 'flash';
      document.querySelector('.content').prepend(el);
    }
    el.textContent = text;
  };

  // The header shows the column total, which may exceed the cards on the page (the finish stage is cut short),
  // so it changes by the number of cards moved instead of being recounted.
  // A CRM column also shows the sum of its cards in the base currency; a card carries its part (data-sum).
  const recount = (col, delta, card) => {
    const c = col.querySelector('.colcount');
    if (c && delta) c.textContent = Math.max(0, (parseInt(c.textContent, 10) || 0) + delta);
    const sum = col.querySelector('.colsum');
    if (sum && card?.dataset.sum) {
      const v = Math.max(0, (parseFloat(sum.dataset.sum) || 0) + Math.sign(delta) * (parseFloat(card.dataset.sum) || 0));
      sum.dataset.sum = v;
      sum.textContent = money(v) + (sum.dataset.cur ? ' ' + sum.dataset.cur : '');
    }
    const empty = col.querySelector('.empty');
    if (empty) empty.hidden = col.querySelectorAll('.kcard').length > 0;
  };

  // as the server's money: 1 234 567,89 (no cents when whole)
  const money = (v) => {
    let whole = Math.trunc(v), cents = Math.round((v - whole) * 100);
    if (cents === 100) { whole++; cents = 0; }
    const s = String(whole).replace(/\B(?=(\d{3})+(?!\d))/g, ' ');
    return cents > 0 ? s + ',' + String(cents).padStart(2, '0') : s;
  };

  const place = (card, col) => { // newest change on top
    const list = col.querySelector('.cards');
    if (list) list.prepend(card); else col.querySelector('.colhead').after(card);
  };

  const fromHTML = (html) => {
    const t = document.createElement('template');
    t.innerHTML = html.trim();
    return t.content.firstElementChild;
  };

  const updateCounters = (c) => {
    if (!c) return;
    const set = (name, n) => document.querySelectorAll(`[data-counter="${name}"]`).forEach(el => {
      el.textContent = n;
      el.hidden = n === 0 || (name === 'open' && c.overdue > 0);
    });
    set('overdue', c.overdue); set('open', c.open); set('to_control', c.to_control);
  };

  const parse = async (res) => {
    try { return await res.json(); } catch { return {}; }
  };

  const post = async (url, params) => {
    try {
      const res = await fetch(url, { method: 'POST', headers: { Accept: 'application/json' }, body: new URLSearchParams(params) });
      return { ok: res.ok, data: await parse(res) };
    } catch {
      return { ok: false, data: {} };
    }
  };

  document.querySelectorAll('[data-board]').forEach(board => {
    const kind = board.dataset.board; // "stage" | "plan" | "deadline" | "crm"
    const failed = board.dataset.failed;
    let dragged = null, from = null;

    board.addEventListener('dragstart', e => {
      const card = e.target.closest('.kcard[draggable=true]');
      if (!card) return;
      dragged = card; from = card.closest('.col');
      e.dataTransfer.effectAllowed = 'move';
      card.classList.add('dragging');
    });
    board.addEventListener('dragend', () => {
      dragged?.classList.remove('dragging');
      board.querySelectorAll('.col').forEach(x => x.classList.remove('over', 'deny'));
      dragged = null;
    });

    board.querySelectorAll('.col[data-stage]').forEach(col => {
      col.addEventListener('dragover', e => {
        if (!dragged || col === from) return;
        col.classList.add('over');
        e.preventDefault();
      });
      col.addEventListener('dragleave', () => col.classList.remove('over', 'deny'));
      col.addEventListener('drop', async e => {
        e.preventDefault();
        col.classList.remove('over', 'deny');
        if (!dragged || col === from) return;
        const card = dragged, source = from;
        place(card, col); recount(col, 1, card); recount(source, -1, card);

        const { ok, data } = kind === 'plan'
          ? await post('/plan/move', { task: card.dataset.key, stage: col.dataset.stage })
          : kind === 'deadline'
            ? await post('/deadlines/move', { task: card.dataset.key, period: col.dataset.stage })
            : kind === 'crm'
            ? await post(board.dataset.move + encodeURIComponent(card.dataset.key) + '/stage', { stage: col.dataset.stage })
            : await post('/t/' + encodeURIComponent(card.dataset.key) + '/stage', { stage: col.dataset.stage });
        let res = { ok, data }, cancelled = false;
        if (!ok && kind === 'crm' && data.fields && window.crmRequired) { // a field required at the stage is empty: ask
          const values = await window.crmRequired(data.fields);
          cancelled = !values;
          if (values) res = await post(board.dataset.move + encodeURIComponent(card.dataset.key) + '/stage', { stage: col.dataset.stage, ...values });
        }

        if (!res.ok) { // undo
          place(card, source); recount(col, -1, card); recount(source, 1, card);
          if (!cancelled) flash(res.data.error || failed);
          return;
        }
        if (kind === 'deadline') { // the new deadline (the column is where the task belongs now)
          const dl = card.querySelector('.dl');
          if (dl) { dl.textContent = data.deadline || ''; dl.classList.remove('over'); }
        }
        if (kind === 'stage') {
          const fresh = data.card ? fromHTML(data.card) : null; // status, chips and drag rights may have changed
          let target = col;
          if (data.stage && data.stage !== col.dataset.stage) { // the server decided another column
            target = board.querySelector(`.col[data-stage="${data.stage}"]`) || col;
          }
          if (fresh) { card.replaceWith(fresh); }
          const now = fresh || card;
          if (target !== col) { place(now, target); recount(col, -1); recount(target, 1); }
          updateCounters(data.counters);
        }
      });
    });

    board.querySelectorAll('form[data-quick]').forEach(form => {
      form.addEventListener('submit', async e => {
        e.preventDefault();
        const col = form.closest('.col');
        let { ok, data } = await post(form.action, new FormData(form));
        if (!ok && kind === 'crm' && data.fields && window.crmRequired) { // the stage's required fields: ask
          const values = await window.crmRequired(data.fields);
          if (!values) return;
          ({ ok, data } = await post(form.action, { ...Object.fromEntries(new FormData(form)), ...values }));
        }
        if (!ok || !data.card) { flash(data.error || failed); return; }
        const card = fromHTML(data.card);
        place(card, col); recount(col, 1, card);
        form.reset();
        form.querySelector('input[name=title]').focus();
      });
    });
  });
})();
