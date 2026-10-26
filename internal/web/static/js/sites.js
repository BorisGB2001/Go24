// «Сайты», the page editor: «Загрузить» beside a picture field (input[data-site-file]) uploads the picture to the
// site and puts its link into the field; the colour picker and the colour field (#RRGGBB) follow each other.
(() => {
  const root = document.querySelector('[data-site-upload]');
  if (!root) return;

  document.addEventListener('change', async e => {
    const input = e.target.closest?.('input[data-site-file]');
    if (!input || !input.files.length) return;
    const field = input.closest('.row')?.querySelector('[data-site-image]');
    const body = new FormData();
    body.append('file', input.files[0], input.files[0].name);
    try {
      const res = await fetch(root.dataset.siteUpload, { method: 'POST', headers: { Accept: 'application/json' }, body });
      const data = await res.json();
      if (!res.ok || !data.url) throw new Error(data.error || res.statusText);
      field.value = data.url;
    } catch (err) {
      alert(root.dataset.siteFailed + (err.message ? ': ' + err.message : ''));
    }
    input.value = '';
  });

  document.addEventListener('input', e => {
    const t = e.target;
    if (t.matches?.('[data-site-picker]')) t.closest('.row').querySelector('[data-site-color]').value = t.value;
    if (t.matches?.('[data-site-color]') && /^#[0-9a-f]{6}$/i.test(t.value)) t.closest('.row').querySelector('[data-site-picker]').value = t.value;
  });
})();
