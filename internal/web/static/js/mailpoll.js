// «Опрос о времени встречи» of the editor, as Outlook's Scheduling Poll (mail.html «mpolldlg», web/mailpoll.go): the
// times of the meeting — each with the employees among the letter's people busy then (/mail/polls/check), «Подобрать
// время» adds times all of them are free — its length, place, video meeting, «Назначать автоматически»; «Вставить в
// письмо» makes the poll of the letter's To and Cc (/mail/polls) and puts its block with the link into the text.
(() => {
  const headers = { Accept: 'application/json', 'X-Go24-Form': '1' };
  const post = async (url, body) => {
    const res = await fetch(url, { method: 'POST', body, headers });
    const data = await res.json().catch(() => null);
    if (!res.ok) throw new Error(data?.error || res.statusText || String(res.status));
    return data || {};
  };
  const pad = n => String(n).padStart(2, '0');
  const local = d => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
  // the n-th working day (Monday to Friday) from today at the next whole hour of the working day
  const nextTime = n => {
    const d = new Date();
    d.setMinutes(0, 0, 0);
    d.setHours(Math.min(Math.max(d.getHours() + 1, 9), 17));
    for (let i = 0; i < n || d.getDay() === 0 || d.getDay() === 6;) {
      d.setDate(d.getDate() + 1);
      if (d.getDay() !== 0 && d.getDay() !== 6) i++;
    }
    return local(d);
  };

  document.addEventListener('go24:pollpick', e => {
    const { form, fields } = e.detail;
    const dlg = form.querySelector('[data-poll-dlg]') || form.pollDialog;
    if (!dlg) return;
    form.pollDialog = dlg;
    document.body.append(dlg); // out of the letter's form: its fields are not the letter's
    const tpl = dlg.querySelector('template[data-poll-labels]');
    const label = k => tpl?.content.querySelector(`[data-l="${k}"]`)?.textContent || k;
    const f = k => dlg.querySelector(`[data-p="${k}"]`);
    const times = dlg.querySelector('[data-poll-times]');
    const err = dlg.querySelector('[data-poll-error]');
    const people = dlg.querySelector('[data-poll-people]');
    const showErr = m => { err.textContent = m || ''; err.hidden = !m; };
    const data = fields();
    const addrs = ['to', 'cc'].flatMap(k => data.getAll(k)).filter(v => v.trim());
    if (!f('title').value) f('title').value = data.get('subject') || '';
    showErr('');

    let timer = 0;
    const starts = () => [...times.querySelectorAll('input[type="datetime-local"]')].map(i => i.value).filter(Boolean);
    const check = (suggest = false) => {
      clearTimeout(timer);
      timer = setTimeout(async () => {
        const body = new URLSearchParams({ length: f('length').value });
        addrs.forEach(a => body.append('addr', a));
        starts().forEach(s => body.append('start', s));
        if (suggest) body.set('suggest', '1');
        let res;
        try { res = await post('/mail/polls/check', body); } catch (x) { showErr(x.message); return; }
        people.textContent = addrs.length ? label('people').replace('%s', res.people) : label('no_people');
        for (const s of res.suggest || []) if (!starts().includes(s)) row(s);
        if (suggest && !(res.suggest || []).length) showErr(label('none_free'));
        const busy = new Map((res.options || []).map(o => [o.start, o.busy]));
        for (const r of times.children) {
          const v = r.querySelector('input').value, b = busy.get(v) || [], note = r.querySelector('.mpoll-note');
          note.textContent = !v ? '' : b.length ? `${label('busy')} ${b.join(', ')}` : label('free');
          note.classList.toggle('mpoll-busy', b.length > 0);
        }
      }, suggest ? 0 : 300);
    };
    const row = value => {
      const r = document.createElement('div');
      r.className = 'row mpoll-time';
      const i = document.createElement('input');
      i.type = 'datetime-local';
      i.step = 900;
      i.value = value;
      i.addEventListener('change', () => dlg.pollCheck());
      const x = document.createElement('button');
      x.type = 'button';
      x.className = 'ghost small';
      x.textContent = '×';
      x.title = label('remove');
      x.addEventListener('click', () => { r.remove(); dlg.pollCheck(); });
      const note = document.createElement('span');
      note.className = 'small mpoll-note';
      r.append(i, x, note);
      times.append(r);
      return r;
    };
    if (!times.children.length) { row(nextTime(1)); row(nextTime(2)); }
    if (!dlg.dataset.wired) {
      dlg.dataset.wired = '1';
      dlg.querySelector('[data-poll-add]').addEventListener('click', () => { dlg.pollRow(nextTime(times.children.length + 1)).querySelector('input').focus(); dlg.pollCheck(); });
      dlg.querySelector('[data-poll-suggest]').addEventListener('click', () => dlg.pollCheck(true));
      f('length').addEventListener('change', () => dlg.pollCheck());
      dlg.querySelector('[data-poll-close]').addEventListener('click', () => dlg.close());
      dlg.querySelector('[data-poll-insert]').addEventListener('click', async () => {
        showErr('');
        const list = starts();
        if (!list.length) { showErr(label('no_times')); return; }
        const d = dlg.pollFields();
        const body = new URLSearchParams({ title: f('title').value, length: f('length').value, location: f('location').value, box: d.get('box') || '' });
        if (f('video').checked) body.set('video', '1');
        if (f('auto').checked) body.set('auto', '1');
        for (const k of ['to', 'cc']) d.getAll(k).forEach(v => body.append(k, v));
        list.forEach(s => body.append('start', s));
        const btn = dlg.querySelector('[data-poll-insert]');
        btn.disabled = true;
        try {
          const p = await post('/mail/polls', body);
          dlg.close();
          times.replaceChildren();
          dlg.pollForm.dispatchEvent(new CustomEvent('go24:insert', { detail: { html: p.html } }));
        } catch (x) { showErr(x.message); } finally { btn.disabled = false; }
      });
    }
    // the handlers wired once work with the letter of this opening
    dlg.pollForm = form;
    dlg.pollFields = fields;
    dlg.pollCheck = check;
    dlg.pollRow = row;
    dlg.showModal ? dlg.showModal() : dlg.setAttribute('open', '');
    check();
  });
})();
