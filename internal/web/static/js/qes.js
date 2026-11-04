// «Подпись» with КЭП: the signer picks their qualified certificate from the CryptoPro plug-in
// (vendor/cadesplugin) and, on submitting the form, signs the document's PDF with it — a detached CAdES-BES signature
// with the signing time, put in «cms» for the server to check (internal/qes).
(() => {
  const box = document.querySelector('[data-qes]');
  if (!box) return;
  const form = box.closest('form'), select = box.querySelector('[data-qes-cert]');
  const input = box.querySelector('input[name="cms"]'), msg = box.querySelector('[data-qes-msg]');
  const say = (text, bad) => { msg.textContent = text; msg.hidden = !text; msg.classList.toggle('error', !!bad); };
  const error = (e) => (e && e.message) || String(e || '');
  const cn = (dn) => { const m = /(?:^|,\s*)CN=("(?:[^"]|"")*"|[^,]*)/.exec(dn || ''); return m ? m[1].replace(/^"|"$/g, '').replace(/""/g, '"') : dn; };
  const day = (d) => new Date(d).toLocaleDateString();
  let certs = [];

  // the plug-in, once loaded, as { cp }: cadesplugin is itself a promise, so it is never resolved with or returned
  const loaded = new Promise((resolve, reject) => {
    if (!window.cadesplugin) return reject(new Error(box.dataset.noPlugin));
    const t = setTimeout(() => reject(new Error(box.dataset.noPlugin)), 15000);
    window.cadesplugin.then(() => { clearTimeout(t); resolve({ cp: window.cadesplugin }); }, () => { clearTimeout(t); reject(new Error(box.dataset.noPlugin)); });
  });
  loaded.catch(() => {});

  // the certificates with a private key, valid now — the store is closed after reading them
  const load = async () => {
    const { cp } = await loaded;
    const store = await cp.CreateObjectAsync('CAdESCOM.Store');
    await store.Open(cp.CAPICOM_CURRENT_USER_STORE, cp.CAPICOM_MY_STORE, cp.CAPICOM_STORE_OPEN_MAXIMUM_ALLOWED);
    const all = await store.Certificates, n = await all.Count, now = new Date();
    const out = [];
    for (let i = 1; i <= n; i++) {
      const c = await all.Item(i);
      const to = new Date(await c.ValidToDate), from = new Date(await c.ValidFromDate);
      if (to < now || from > now || !(await c.HasPrivateKey())) continue;
      out.push({ cert: c, name: cn(await c.SubjectName), issuer: cn(await c.IssuerName), to });
    }
    await store.Close();
    return out;
  };

  load().then((list) => {
    certs = list;
    select.textContent = '';
    if (!list.length) { say(box.dataset.noCerts, true); return; }
    list.forEach((c, i) => {
      const o = document.createElement('option');
      o.value = String(i);
      o.textContent = `${c.name} — ${box.dataset.until} ${day(c.to)} (${c.issuer})`;
      select.append(o);
    });
    select.disabled = false;
  }, (e) => { select.textContent = ''; say(error(e), true); });

  const base64 = (buf) => {
    const b = new Uint8Array(buf);
    let s = '';
    for (let i = 0; i < b.length; i += 0x8000) s += String.fromCharCode.apply(null, b.subarray(i, i + 0x8000));
    return btoa(s);
  };

  const sign = async (c) => {
    const { cp } = await loaded;
    const res = await fetch(box.dataset.pdf, { credentials: 'same-origin' });
    if (!res.ok) throw new Error(box.dataset.failed);
    const signer = await cp.CreateObjectAsync('CAdESCOM.CPSigner');
    await signer.propset_Certificate(c.cert);
    try { // the signing time among the signed attributes, as CAdES wants
      const at = await cp.CreateObjectAsync('CADESCOM.CPAttribute');
      await at.propset_Name(cp.CAPICOM_AUTHENTICATED_ATTRIBUTE_SIGNING_TIME);
      await at.propset_Value(new Date());
      await (await signer.AuthenticatedAttributes2).Add(at);
    } catch (_) { /* an older plug-in signs without it */ }
    const sd = await cp.CreateObjectAsync('CAdESCOM.CadesSignedData');
    await sd.propset_ContentEncoding(cp.CADESCOM_BASE64_TO_BINARY);
    await sd.propset_Content(base64(await res.arrayBuffer()));
    return sd.SignCades(signer, cp.CADESCOM_CADES_BES, true);
  };

  form.addEventListener('submit', (ev) => {
    if (input.value || ev.submitter?.dataset.noQes !== undefined) return;
    ev.preventDefault();
    if (!form.reportValidity()) return;
    const c = certs[Number(select.value)];
    if (!c || select.disabled) { say(certs.length ? box.dataset.pick : (msg.textContent || box.dataset.noPlugin), true); return; }
    const buttons = form.querySelectorAll('button');
    buttons.forEach((b) => { b.disabled = true; });
    say(box.dataset.signing);
    sign(c).then((sig) => {
      input.value = sig;
      buttons.forEach((b) => { b.disabled = false; });
      form.submit();
    }, (e) => {
      buttons.forEach((b) => { b.disabled = false; });
      say(`${box.dataset.failed}: ${error(e)}`, true);
    });
  });
})();
