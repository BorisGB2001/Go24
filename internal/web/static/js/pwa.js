// The installable go24 (PWA): registers the service worker (/sw.js) and shows «Установить приложение» ([data-pwa-install])
// when the browser can install the portal — Chrome and Android at once (beforeinstallprompt), iPhone and iPad with
// a hint (Safari has no such button: «Поделиться» → «На экран «Домой»»). In the installed app the entry stays hidden.
(() => {
  if ('serviceWorker' in navigator && window.isSecureContext) {
    navigator.serviceWorker.register('/sw.js', { scope: '/' }).catch(() => {});
  }
  const standalone = matchMedia('(display-mode: standalone)').matches || navigator.standalone === true;
  const links = document.querySelectorAll('[data-pwa-install]');
  if (standalone || !links.length) return;
  let prompt = null;
  const show = () => links.forEach((a) => { a.hidden = false; });
  const ios = /iPad|iPhone|iPod/.test(navigator.userAgent) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1);
  if (ios) show();
  window.addEventListener('beforeinstallprompt', (e) => {
    e.preventDefault();
    prompt = e;
    show();
  });
  window.addEventListener('appinstalled', () => links.forEach((a) => { a.hidden = true; }));
  links.forEach((a) => a.addEventListener('click', (e) => {
    e.preventDefault();
    if (prompt) {
      prompt.prompt();
      prompt.userChoice.finally(() => { prompt = null; });
    } else if (ios) {
      alert(a.dataset.iosHint);
    }
  }));
})();

// Push notifications on this device (GO24DEV-743), in «Настройки уведомлений» ([data-push-key] — the company's VAPID
// key): «Включить на этом устройстве» asks the browser's permission, subscribes the service worker and hands the
// subscription to go24; a device go24 already knows is marked «это устройство». A subscription made with another key
// (the company's key changed) is dropped first.
(() => {
  const row = document.querySelector('[data-push-key]');
  if (!row) return;
  const btn = row.querySelector('[data-push-on]');
  const state = row.querySelector('[data-push-state]');
  const say = (text) => { state.textContent = text; };
  if (!('serviceWorker' in navigator) || !('PushManager' in window) || !window.isSecureContext) {
    say(row.dataset.pushOff);
    return;
  }
  const key = row.dataset.pushKey;
  const raw = (s) => Uint8Array.from(atob(s.replace(/-/g, '+').replace(/_/g, '/')), (c) => c.charCodeAt(0));
  const same = (a, b) => a && b && a.byteLength === b.byteLength && new Uint8Array(a).every((v, i) => v === b[i]);
  const label = () => {
    const ua = navigator.userAgent;
    const browser = /Edg\//.test(ua) ? 'Edge' : /YaBrowser/.test(ua) ? 'Yandex' : /Firefox\//.test(ua) ? 'Firefox'
      : /Chrome\//.test(ua) ? 'Chrome' : /Safari\//.test(ua) ? 'Safari' : '';
    const os = /Android/.test(ua) ? 'Android' : /iPhone|iPad|iPod/.test(ua) ? 'iOS' : /Mac OS X/.test(ua) ? 'macOS'
      : /Windows/.test(ua) ? 'Windows' : /Linux/.test(ua) ? 'Linux' : '';
    return [browser, os].filter(Boolean).join(' · ');
  };
  const send = (sub) => {
    const j = sub.toJSON();
    return fetch('/settings/notifications/push/devices', {
      method: 'POST', headers: { Accept: 'application/json' },
      body: new URLSearchParams({ endpoint: j.endpoint, p256dh: j.keys.p256dh, auth: j.keys.auth, label: label() }),
    }).then((r) => { if (!r.ok) throw new Error(String(r.status)); });
  };
  navigator.serviceWorker.ready.then((reg) => reg.pushManager.getSubscription().then((sub) => {
    if (sub && !same(sub.options.applicationServerKey, raw(key))) return sub.unsubscribe().then(() => null);
    return sub;
  }).then((sub) => {
    const known = sub && [...row.querySelectorAll('[data-push-endpoint]')].find((li) => li.dataset.pushEndpoint === sub.endpoint);
    if (known && Notification.permission === 'granted') {
      known.querySelector('[data-push-this]').hidden = false;
      row.querySelector('[data-push-here]').hidden = false;
      return;
    }
    if (Notification.permission === 'denied') {
      say(row.dataset.pushDenied);
      return;
    }
    btn.hidden = false;
    btn.addEventListener('click', () => {
      btn.disabled = true;
      Notification.requestPermission().then((p) => {
        if (p !== 'granted') throw new Error('denied');
        return sub || reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: raw(key) });
      }).then(send).then(() => location.reload()).catch((err) => {
        btn.disabled = false;
        say(err && err.message === 'denied' ? row.dataset.pushDenied : row.dataset.pushFail);
      });
    });
  })).catch(() => say(row.dataset.pushFail));
})();
