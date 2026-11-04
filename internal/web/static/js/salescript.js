// «Скрипты продаж»: walks a sales script — the block's text the employee says, buttons of the client's
// answers leading to the next block, «Назад» and «Сначала», a choice of the script when there are several.
// go24SalesScript(box, scripts) fills box (data-back, data-again, data-end — the texts) and returns nothing; scripts are
// [{id, name, blocks: [{title, text, answers: [{label, next}]}]}], next −1 — the script's end. The softphone's call card
// uses it, and «Настройки → Телефония» to try a script ([data-sales-try] with data-script — the script's JSON).
(() => {
  const el = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text) e.textContent = text; return e; };
  window.go24SalesScript = (box, scripts) => {
    const t = box.dataset;
    let script = null, path = []; // the blocks walked, the current last; −1 — the end
    const draw = () => {
      const view = box.querySelector('[data-sales-view]');
      view.replaceChildren();
      if (!script) return;
      const at = path[path.length - 1];
      const b = script.blocks[at];
      if (b) {
        view.append(el('b', 'small', b.title), el('div', 'ss-text', b.text));
        const answers = el('div', 'row wrap ss-answers');
        b.answers.forEach(a => {
          const btn = el('button', 'small', a.label);
          btn.type = 'button';
          btn.addEventListener('click', () => { path.push(a.next); draw(); });
          answers.append(btn);
        });
        if (b.answers.length) view.append(answers);
        else view.append(el('div', 'muted small', t.end));
      } else view.append(el('div', 'muted small', t.end));
      const nav = el('div', 'row wrap');
      if (path.length > 1) {
        const back = el('button', 'ghost small', '← ' + t.back);
        back.type = 'button';
        back.addEventListener('click', () => { path.pop(); draw(); });
        nav.append(back);
      }
      if (path.length > 1 || !b) {
        const again = el('button', 'ghost small', '↺ ' + t.again);
        again.type = 'button';
        again.addEventListener('click', () => { path = [0]; draw(); });
        nav.append(again);
      }
      if (nav.children.length) view.append(nav);
    };
    const pick = box.querySelector('[data-sales-pick]');
    const choose = id => { script = scripts.find(s => s.id === id) || scripts[0] || null; path = [0]; draw(); };
    if (pick) {
      pick.replaceChildren(...scripts.map(s => { const o = el('option', '', s.name); o.value = s.id; return o; }));
      pick.hidden = scripts.length < 2;
      pick.onchange = () => choose(pick.value);
    }
    box.hidden = !scripts.length;
    choose(scripts[0] && scripts[0].id);
  };
  document.querySelectorAll('[data-sales-try]').forEach(btn => btn.addEventListener('click', () => {
    const box = document.getElementById(btn.dataset.salesTry);
    window.go24SalesScript(box, [JSON.parse(btn.dataset.script)]);
  }));
})();
