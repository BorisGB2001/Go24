// «Онлайн-запись» in a frame of a website (?embed=1): tells the page around it its height, so the frame grows with
// it (the code «Код для сайта» and the block of Сайты listen), and asks before forms[data-confirm] (cancelling).
(() => {
  // the bottom of the content, not scrollHeight: that is never less than the frame, so the frame would not shrink
  const box = document.querySelector('.share') || document.body;
  const tell = () => parent.postMessage({ go24booking: Math.ceil(box.getBoundingClientRect().bottom + scrollY) + 8 }, '*');
  addEventListener('load', tell);
  new ResizeObserver(tell).observe(box);
  tell();
  document.addEventListener('submit', e => {
    const msg = e.target.dataset?.confirm;
    if (msg && !confirm(msg)) e.preventDefault();
  });
})();
