// go24 «Лента»: publish, comment, react, confirm «Прочитано», delete — all on the page, without a reload.
(() => {
  const feed = document.querySelector('[data-feed]');
  if (!feed) return;
  const failed = feed.dataset.failed;
  // params: a FormData with files goes as it is (multipart), anything else as a plain form
  const post = async (url, params) => {
    const files = params instanceof FormData && [...params.values()].some(v => typeof v === 'object' && v.size > 0);
    try {
      const res = await fetch(url, { method: 'POST', headers: { Accept: 'application/json' }, body: files ? params : new URLSearchParams(params) });
      let data = {};
      try { data = await res.json(); } catch { data = {}; }
      return { ok: res.ok, data };
    } catch { return { ok: false, data: {} }; }
  };
  const node = (html) => { const t = document.createElement('template'); t.innerHTML = html.trim(); return t.content.firstElementChild; };

  // a post appears once, whichever comes first: the answer to our own publish or the live event
  const put = (id, html) => {
    const old = feed.querySelector(`[data-post-id="${id}"]`);
    if (old) { old.replaceWith(node(html)); return; }
    feed.querySelector('[data-empty]')?.remove();
    feed.querySelector('[data-items]').prepend(node(html));
  };

  // new post
  const form = feed.querySelector('form[data-post]');
  form?.addEventListener('submit', async e => {
    e.preventDefault();
    const err = form.querySelector('[data-error]');
    const { ok, data } = await post(form.action, new FormData(form));
    if (!ok || !data.html) { err.textContent = data.error || failed; err.hidden = false; return; }
    err.hidden = true;
    put(data.id, data.html);
    if (form.querySelector('[data-im-src]')) { // a post of a chat message: made once, the form is a plain one again
      form.querySelector('[data-im-src]').remove();
      form.elements.body.defaultValue = '';
      form.querySelectorAll('option').forEach(o => { o.defaultSelected = false; });
      history.replaceState(null, '', '/feed');
    }
    form.reset();
    if (names) names.textContent = '';
  });
  // «Задача», «Событие»: the full form of a task or a meeting with what is typed here
  form?.querySelectorAll('[data-feed-make]').forEach(b => b.addEventListener('click', () => {
    const q = new URLSearchParams({ title: form.elements.title.value, description: form.elements.body.value });
    location.href = b.dataset.feedMake + '?' + q;
  }));
  // «Важное»: for how long — always, a day, a week, a month or until a date; «Кому»: chosen people and departments —
  // the picker shows up for them. Of the new post's form and of the edit forms alike.
  const untilShow = f => {
    const imp = f?.querySelector('[data-important]'), impFor = f?.querySelector('[data-important-for]');
    if (!imp || !impFor) return;
    const sel = f.querySelector('[data-until-sel]'), date = f.querySelector('[data-until-date]');
    impFor.hidden = !imp.checked;
    date.hidden = sel.value !== 'date';
    date.required = imp.checked && sel.value === 'date';
  };
  const toShow = f => {
    const sel = f?.querySelector('[data-feed-to]'), people = f?.querySelector('[data-feed-people]');
    if (sel && people) people.hidden = sel.value !== 'people';
  };
  feed.addEventListener('change', e => {
    if (e.target.matches('[data-important], [data-until-sel]')) untilShow(e.target.form);
    if (e.target.matches('[data-feed-to]')) toShow(e.target.form);
  });
  form?.addEventListener('reset', () => setTimeout(() => {
    form.querySelector('[data-feed-people]')?.querySelectorAll('.upick-chip').forEach(c => c.remove());
    untilShow(form); toShow(form);
  }));
  const names = form?.querySelector('[data-files-names]');
  // files of a post or a comment: the paperclip, pasted from the clipboard or dropped on the form
  const fileNames = f => { const n = f.querySelector('[data-files-names]'); if (n) n.textContent = [...f.querySelector('[data-files]').files].map(x => x.name).join(', '); };
  const addFiles = (f, list) => {
    const input = f.querySelector('[data-files]');
    const dt = new DataTransfer();
    [...input.files, ...list].forEach(x => dt.items.add(x));
    input.files = dt.files;
    fileNames(f);
  };
  const filesForm = e => e.target.closest?.('form[data-post], form[data-comment], form[data-edit-form]');
  feed.addEventListener('change', e => { if (e.target.matches('[data-files]')) fileNames(e.target.form); });
  feed.addEventListener('paste', e => {
    const f = filesForm(e), list = e.clipboardData?.files;
    if (!f || !list?.length) return;
    e.preventDefault();
    addFiles(f, list);
  });
  feed.addEventListener('dragover', e => {
    const f = filesForm(e);
    if (!f || !e.dataTransfer.types.includes('Files')) return;
    e.preventDefault();
    f.classList.add('fdrop');
  });
  feed.addEventListener('dragleave', e => { const f = filesForm(e); if (f && !f.contains(e.relatedTarget)) f.classList.remove('fdrop'); });
  feed.addEventListener('drop', e => {
    const f = filesForm(e);
    if (!f || !e.dataTransfer.files.length) return;
    e.preventDefault();
    f.classList.remove('fdrop');
    addFiles(f, e.dataTransfer.files);
  });

  feed.addEventListener('submit', async e => {
    const vf = e.target.closest('form[data-vote]');
    if (vf) { // a vote (or taking it back): the post comes back with the results
      e.preventDefault();
      const card = vf.closest('[data-post-id]');
      const { ok, data } = await post(vf.action, new FormData(vf));
      if (!ok || !data.html) { alert(data.error || failed); return; }
      card.replaceWith(node(data.html));
      return;
    }
    const ef = e.target.closest('form[data-edit-form]');
    if (ef) {
      e.preventDefault();
      const card = ef.closest('[data-post-id]');
      const { ok, data } = await post('/feed/' + card.dataset.postId + '/edit', new FormData(ef));
      if (!ok || !data.html) { const b = ef.querySelector('[data-error]'); b.textContent = data.error || failed; b.hidden = false; return; }
      card.replaceWith(node(data.html));
      return;
    }
    const sf = e.target.closest('form[data-share-form]'); // «Поделиться»: the post comes back with the new addressees
    if (sf) {
      e.preventDefault();
      const card = sf.closest('[data-post-id]');
      const { ok, data } = await post('/feed/' + card.dataset.postId + '/share', new FormData(sf));
      if (!ok || !data.html) { const b = sf.querySelector('[data-error]'); b.textContent = data.error || failed; b.hidden = false; return; }
      const fresh = node(data.html);
      card.replaceWith(fresh);
      if (data.msg) { const m = document.createElement('div'); m.className = 'flash'; m.textContent = data.msg; fresh.querySelector('.fhead').after(m); setTimeout(() => m.remove(), 4000); }
      return;
    }
    const cf = e.target.closest('form[data-cedit-form]'); // an edited comment
    if (cf) {
      e.preventDefault();
      const c = cf.closest('[data-comment-id]');
      const { ok, data } = await post('/feed/comments/' + c.dataset.commentId + '/edit', new FormData(cf));
      if (!ok || !data.html) { alert(data.error || failed); return; }
      c.replaceWith(node(data.html));
      return;
    }
    const f = e.target.closest('form[data-comment]');
    if (!f) return;
    e.preventDefault();
    const card = f.closest('[data-post-id]');
    const { ok, data } = await post('/feed/' + card.dataset.postId + '/comments', new FormData(f));
    if (!ok || !data.html) { alert(data.error || failed); return; }
    card.querySelector('[data-comments]').append(node(data.html));
    f.reset();
    fileNames(f);
    f.querySelectorAll('.dpick-chip').forEach(c => c.remove());
  });

  // reactions: the button gives «Нравится» (or takes the own reaction back); hovering it or holding it
  // opens the picker of the others; the summary opens «Кто оценил» with a tab per reaction
  const react = async (box, reaction) => {
    closePicker();
    const { ok, data } = await post(box.dataset.react + '/like', { reaction });
    if (!ok || !data.html) { if (data.error) alert(data.error); return; }
    box.replaceWith(node(data.html));
  };
  let picker = null, pickTimer = 0, held = false;
  const closePicker = () => { clearTimeout(pickTimer); picker?.remove(); picker = null; };
  const openPicker = (box) => {
    if (picker && picker.parentNode === box) return;
    closePicker();
    picker = document.querySelector('template[data-react-picker]').content.firstElementChild.cloneNode(true);
    picker.querySelectorAll('[data-reaction]').forEach(b => b.classList.toggle('on', b.dataset.reaction === box.dataset.mine));
    box.append(picker);
  };
  feed.addEventListener('mouseover', e => {
    const like = e.target.closest('[data-react] [data-like]');
    const box = e.target.closest('[data-react]');
    clearTimeout(pickTimer);
    if (like) pickTimer = setTimeout(() => openPicker(box), 450);
    else if (!box || !picker || picker.parentNode !== box) pickTimer = setTimeout(closePicker, 350);
  });
  feed.addEventListener('pointerdown', e => {
    const like = e.target.closest('[data-react] [data-like]');
    if (!like || e.pointerType === 'mouse') return;
    held = false;
    pickTimer = setTimeout(() => { held = true; openPicker(like.closest('[data-react]')); }, 500);
  });
  feed.addEventListener('pointerup', e => { if (e.pointerType !== 'mouse' && !held) clearTimeout(pickTimer); });
  feed.addEventListener('contextmenu', e => { if (e.target.closest('[data-react] [data-like]')) e.preventDefault(); });

  let who = null;
  const closeWho = () => { who?.remove(); who = null; };
  const reactors = async (box) => {
    const was = who && who.parentNode === box;
    closeWho();
    if (was) return;
    let list = [];
    try {
      const res = await fetch(box.dataset.react + '/likes', { headers: { Accept: 'application/json' } });
      if (!res.ok) return;
      list = await res.json();
    } catch { return; }
    who = document.querySelector('template[data-reactors-box]').content.firstElementChild.cloneNode(true);
    const tabs = who.querySelector('[data-tabs]'), body = who.querySelector('[data-list]');
    const show = (reaction, tab) => {
      tabs.querySelectorAll('a').forEach(a => a.classList.toggle('on', a === tab));
      body.replaceChildren(...list.filter(x => !reaction || x.reaction === reaction).map(x => {
        const a = document.createElement('a');
        a.className = 'person'; a.href = '/u/' + x.id; a.dataset.user = x.id;
        const img = document.createElement('img'); img.src = x.photo || '/static/img/avatar.svg'; img.alt = '';
        const name = document.createElement('span'); name.textContent = x.name;
        const em = document.createElement('span'); em.className = 'remoji'; em.textContent = x.emoji;
        a.append(img, name, em);
        return a;
      }));
    };
    const tab = (label, reaction) => {
      const a = document.createElement('a');
      a.textContent = label;
      a.addEventListener('click', ev => { ev.stopPropagation(); show(reaction, a); });
      tabs.append(a);
      return a;
    };
    const all = tab(who.dataset.all + ' ' + list.length, '');
    const counts = {};
    list.forEach(x => { counts[x.reaction] = counts[x.reaction] || { n: 0, emoji: x.emoji }; counts[x.reaction].n++; });
    Object.entries(counts).sort((a, b) => b[1].n - a[1].n).forEach(([r, c]) => tab(c.emoji + ' ' + c.n, r));
    show('', all);
    box.append(who);
  };
  // «Просмотры»: who saw the post and when
  const viewers = async (box, id) => {
    const was = who && who.parentNode === box;
    closeWho();
    if (was) return;
    let list = [];
    try {
      const res = await fetch('/feed/' + id + '/views', { headers: { Accept: 'application/json' } });
      if (!res.ok) return;
      list = await res.json();
    } catch { return; }
    who = document.querySelector('template[data-viewers-box]').content.firstElementChild.cloneNode(true);
    who.querySelector('[data-none]').hidden = list.length > 0;
    who.querySelector('[data-list]').replaceChildren(...list.map(x => {
      const a = document.createElement('a');
      a.className = 'person'; a.href = '/u/' + x.id; a.dataset.user = x.id;
      const img = document.createElement('img'); img.src = x.photo || '/static/img/avatar.svg'; img.alt = '';
      const name = document.createElement('span'); name.textContent = x.name;
      const at = document.createElement('span'); at.className = 'muted small'; at.textContent = x.at;
      a.append(img, name, at);
      return a;
    }));
    const n = box.querySelector('[data-views-n]');
    if (n) n.textContent = list.length;
    box.append(who);
  };
  document.addEventListener('click', e => {
    if (who && !e.target.closest('.rwho') && !e.target.closest('[data-reactors]') && !e.target.closest('[data-views]')) closeWho();
    if (picker && !e.target.closest('[data-react]')) closePicker();
  });
  document.addEventListener('keydown', e => { if (e.key === 'Escape') { closeWho(); closePicker(); } });

  feed.addEventListener('click', async e => {
    const card = e.target.closest('[data-post-id]');
    if (!card) return;
    const id = card.dataset.postId;
    const mark = e.target.closest('[data-mark]'); // «В избранное», «Закрепить», «Следить»: the viewer's own marks
    if (mark) {
      const { ok, data } = await post('/feed/' + id + '/' + mark.dataset.mark, {});
      if (!ok) { alert(data.error || failed); return; }
      mark.classList.toggle('on', data.on);
      if (mark.dataset.hintOn) mark.title = data.on ? mark.dataset.hintOn : mark.dataset.hintOff;
      if (mark.dataset.mark === 'pin' && feed.dataset.general !== undefined) location.reload(); // the pinned go above
      // «Скрыть» takes the post out of the feed, «Вернуть в ленту» — out of «Скрытые»
      if (mark.dataset.mark === 'hide' && data.on === !card.closest('[data-hidden-view]')) card.remove();
      return;
    }
    const box = e.target.closest('[data-react]'); // reactions of the post or of a comment
    if (box) {
      const pick = e.target.closest('[data-reaction]');
      if (pick) { react(box, pick.dataset.reaction); return; }
      if (e.target.closest('[data-like]')) {
        if (held) { held = false; return; } // the hold opened the picker, it is not a click
        react(box, box.dataset.mine || 'like'); // the own reaction again takes it back
        return;
      }
      if (e.target.closest('[data-reactors]')) { reactors(box); return; }
      return;
    }
    const c = e.target.closest('[data-comment-id]'); // a comment: «Ответить», «Ссылка», «Изменить», «Удалить»
    if (c) {
      const cid = c.dataset.commentId;
      const reply = e.target.closest('[data-creply]');
      if (reply) { //: the answer starts with a mention of the author
        const input = card.querySelector('form[data-comment] [name=body]');
        const at = '@' + reply.dataset.creply + ' ';
        if (!input.value.includes(at)) input.value = at + input.value;
        input.focus();
        input.setSelectionRange(input.value.length, input.value.length);
        return;
      }
      const link = e.target.closest('[data-clink]');
      if (link) {
        const url = location.origin + '/feed?post=' + id + '#comment-' + cid;
        try { await navigator.clipboard.writeText(url); } catch { prompt('', url); return; }
        const was = link.textContent;
        link.textContent = link.dataset.copied;
        setTimeout(() => { link.textContent = was; }, 1500);
        return;
      }
      const cv = c.querySelector('[data-cview]'), cf = c.querySelector('[data-cedit-form]');
      if (e.target.closest('[data-cedit]') && cf) {
        cv.hidden = true; cf.hidden = false;
        const i = cf.elements.body; i.focus(); i.setSelectionRange(i.value.length, i.value.length);
        return;
      }
      if (e.target.closest('[data-cedit-cancel]') && cf) {
        cf.reset(); cf.hidden = true; cv.hidden = false;
        return;
      }
      const cdel = e.target.closest('[data-cdelete]');
      if (cdel) {
        if (!confirm(cdel.dataset.cdelete)) return;
        const { ok, data } = await post('/feed/comments/' + cid + '/delete', {});
        if (!ok) { alert(data.error || failed); return; }
        c.remove();
        return;
      }
    }
    if (e.target.closest('[data-views]')) { viewers(card.querySelector('[data-views-box]'), id); return; }
    const read = e.target.closest('[data-read]');
    if (read) {
      const { ok, data } = await post('/feed/' + id + '/read', {});
      if (!ok) return;
      read.remove();
      const r = card.querySelector('[data-readers]');
      if (r) r.textContent = data.text;
      document.querySelectorAll('[data-counter="feed"]').forEach(c => {
        const n = Math.max(0, (parseInt(c.textContent, 10) || 0) - 1);
        c.textContent = n; c.hidden = n === 0;
      });
      return;
    }
    if (e.target.closest('[data-share]')) { // the picker of more addressees under the header, once
      let f = card.querySelector('[data-share-form]');
      if (!f) {
        f = document.querySelector('template[data-share-box]').content.firstElementChild.cloneNode(true);
        card.querySelector('.fhead').after(f);
      }
      f.hidden = false; f.querySelector('input:not([type=hidden])')?.focus();
      return;
    }
    if (e.target.closest('[data-share-cancel]')) {
      card.querySelector('[data-share-form]')?.remove();
      return;
    }
    const ed = e.target.closest('[data-edit]');
    if (ed) { // «Изменить»: the whole form of the post in place of its text
      if (card.querySelector('[data-edit-form]')) return;
      let data = {};
      try {
        const res = await fetch('/feed/' + id + '/edit', { headers: { Accept: 'application/json' } });
        data = await res.json();
        if (!res.ok || !data.html) throw new Error();
      } catch { alert(data.error || failed); return; }
      const view = card.querySelector('[data-view]');
      const f = node(data.html);
      view.hidden = true;
      view.after(f);
      f.querySelector('textarea').focus();
      return;
    }
    if (e.target.closest('[data-edit-cancel]')) {
      card.querySelector('[data-edit-form]')?.remove();
      card.querySelector('[data-view]').hidden = false;
      return;
    }
    const del = e.target.closest('[data-delete]');
    if (del) {
      if (!confirm(del.title + '?')) return;
      const { ok } = await post('/feed/' + id + '/delete', {});
      if (ok) card.remove();
    }
  });

  // «Просмотры»: a post counts as seen once half of it was on the screen; sent in batches
  if (window.IntersectionObserver) {
    const seen = new Set(), queue = new Set();
    let timer = 0;
    const flush = () => {
      timer = 0;
      if (!queue.size) return;
      const p = new URLSearchParams();
      queue.forEach(id => p.append('id', id));
      queue.clear();
      fetch('/feed/views', { method: 'POST', headers: { Accept: 'application/json' }, body: p, keepalive: true }).catch(() => {});
    };
    const io = new IntersectionObserver(entries => entries.forEach(en => {
      const id = en.target.dataset.postId;
      if (!en.isIntersecting || seen.has(id)) return;
      seen.add(id); queue.add(id); io.unobserve(en.target);
      if (!timer) timer = setTimeout(flush, 1500);
    }), { threshold: 0.5 });
    const watch = () => feed.querySelectorAll('[data-post-id]').forEach(el => { if (!seen.has(el.dataset.postId)) io.observe(el); });
    watch();
    new MutationObserver(watch).observe(feed, { childList: true, subtree: true });
    addEventListener('pagehide', flush);
  }

  // live: posts from colleagues and from the messenger appear at the top of the unfiltered feed
  if (window.EventSource && !location.search) {
    const es = new EventSource('/feed/live');
    es.addEventListener('post', e => {
      try {
        const d = JSON.parse(e.data);
        if (!feed.querySelector(`[data-post-id="${d.id}"]`)) put(d.id, d.html);
      } catch { /* ignore a broken event */ }
    });
  }
})();
