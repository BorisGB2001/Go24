// «Колл-трекинг» on a client's site: <script src="https://go24…/ct.js" async></script>.
// The visitor's channel is the utm_source of the address they came by, or else the site they came from; it is kept
// for 30 days. A phone of the company on the page (the number of one of its lines, in any spelling, or an element
// with the class go24-phone) is shown as the number of the line of that channel, so the call tells where the client
// came from. The config: {numbers: [every line's number], lines: [{number, text, channels: [utm_source…]}]}.
(() => {
  const cfg = /*CONFIG*/{};
  const lines = cfg.lines || [];
  if (!lines.length) return;
  const KEY = 'go24ct', DAYS = 30;
  const tail = s => String(s).replace(/\D/g, '').slice(-10);
  const ours = new Set((cfg.numbers || []).concat(lines.map(l => l.number)).map(tail)); // the company's numbers
  const lineOf = ch => {
    ch = String(ch || '').toLowerCase();
    if (!ch) return null;
    return lines.find(l => (l.channels || []).some(c => c && (ch === c || ch.split('.').includes(c) || (c.length >= 3 && ch.includes(c))))) || null;
  };
  // the channel: utm_source of this address, the site the visitor came from, or the one remembered
  let ch = new URLSearchParams(location.search).get('utm_source') || '';
  if (!ch && document.referrer) {
    try {
      const h = new URL(document.referrer).hostname;
      if (h !== location.hostname) ch = h;
    } catch (e) { /* not an address */ }
  }
  let saved = null;
  try { saved = JSON.parse(localStorage.getItem(KEY) || 'null'); } catch (e) { /* storage off */ }
  if (ch && lineOf(ch)) {
    try { localStorage.setItem(KEY, JSON.stringify({ ch, at: Date.now() })); } catch (e) { /* storage off */ }
  } else if (saved && Date.now() - saved.at < DAYS * 864e5) {
    ch = saved.ch;
  }
  const line = lineOf(ch);
  if (!line) return;
  const phoneRe = /\+?\d[\d\s().-]{8,}\d/g, hasPhone = new RegExp(phoneRe.source);
  const swap = root => {
    root.querySelectorAll('.go24-phone').forEach(el => { el.textContent = line.text; });
    root.querySelectorAll('a[href^="tel:"]').forEach(a => {
      if (ours.has(tail(a.getAttribute('href')))) a.setAttribute('href', 'tel:' + line.number);
    });
    const walk = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    const nodes = [];
    for (let n = walk.nextNode(); n; n = walk.nextNode()) if (hasPhone.test(n.nodeValue)) nodes.push(n);
    nodes.forEach(n => {
      n.nodeValue = n.nodeValue.replace(phoneRe, m => ours.has(tail(m)) ? line.text : m);
    });
  };
  const run = () => swap(document.body);
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', run);
  else run();
  window.go24CallTrack = { channel: ch, number: line.number };
})();
