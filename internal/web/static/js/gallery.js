// «Фотогалерея»: photos dropped on the upload form; on a photo's page — ← and → to the neighbours, the slide show
// going on by itself, the photo on the whole screen.
(() => {
  const SLIDE = 4000; // a slide of the slide show, ms

  const form = document.querySelector('[data-gdrop]');
  if (form) {
    const input = form.querySelector('input[type=file]');
    const zone = form.querySelector('.gdrop');
    const picked = form.querySelector('[data-gpicked]');
    const show = () => {
      const names = [...input.files].map(f => f.name);
      picked.textContent = names.length ? names.length + ': ' + names.slice(0, 5).join(', ') + (names.length > 5 ? '…' : '') : '';
    };
    input.addEventListener('change', show);
    ['dragenter', 'dragover'].forEach(t => zone.addEventListener(t, e => { e.preventDefault(); zone.classList.add('over'); }));
    ['dragleave', 'drop'].forEach(t => zone.addEventListener(t, () => zone.classList.remove('over')));
    zone.addEventListener('drop', e => {
      e.preventDefault();
      if (e.dataTransfer?.files?.length) {
        input.files = e.dataTransfer.files;
        show();
      }
    });
  }

  const view = document.querySelector('[data-gview]');
  if (!view) return;
  const play = view.hasAttribute('data-play');
  const go = url => { if (url) location.href = url + (play ? '?play=1' : ''); };
  document.addEventListener('keydown', e => {
    if (e.target.closest('input, textarea, select')) return;
    if (e.key === 'ArrowLeft') go(view.dataset.prev);
    else if (e.key === 'ArrowRight') go(view.dataset.next);
    else if (e.key === 'Escape' && play) location.href = location.pathname;
  });
  if (play && view.dataset.next) setTimeout(() => go(view.dataset.next), SLIDE);
  document.querySelector('[data-gfull]')?.addEventListener('click', () => view.requestFullscreen?.());
})();
