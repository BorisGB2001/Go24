// «SMS» of a CRM card (parts/crm.html «crmsms»): under the text — its length and how many SMS it takes, as the
// server counts them (sms.Parts): 160 characters in one (153 in each of several) of the GSM alphabet, else 70 (67).
(() => {
  const gsm = '@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞÆæßÉ !"#¤%&\'()*+,-./0123456789:;<=>?¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà^{}\\[~]|€';
  const parts = text => {
    const chars = [...text];
    const plain = chars.every(c => gsm.includes(c));
    const [one, many] = plain ? [160, 153] : [70, 67];
    if (!chars.length) return [0, 0];
    return [chars.length, chars.length <= one ? 1 : Math.ceil(chars.length / many)];
  };
  const show = area => {
    const out = area.form?.querySelector('[data-smsparts]');
    if (!out) return;
    const [n, p] = parts(area.value);
    out.textContent = n ? out.dataset.tpl.replace('%d', n).replace('%d', p) : '';
  };
  document.addEventListener('input', e => {
    if (e.target.matches?.('[data-smscount]')) show(e.target);
  });
})();
