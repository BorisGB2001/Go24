// «Местоположение» of the working day, as the portal mobile time tracking: a form[data-geo] (the top bar's working
// day) asks the browser where it is before starting or finishing the day, and sends lat, lon and acc with
// it. Refused, unavailable (plain http) or slower than 10 s — the day goes on without a position.
(() => {
  const actions = ['start', 'finish'];
  document.addEventListener('submit', e => {
    const form = e.target;
    if (!(form instanceof HTMLFormElement) || !('geo' in form.dataset) || form.dataset.geoDone) return;
    const action = e.submitter?.value || form.querySelector('[name=action]')?.value;
    if (!actions.includes(action) || !navigator.geolocation) return;
    // before forms.js and data-confirm (listening without capture): the form goes once the position is known
    e.preventDefault();
    e.stopImmediatePropagation();
    if (form.dataset.geoWait) return;
    form.dataset.geoWait = '1';
    const submitter = e.submitter;
    const go = pos => {
      delete form.dataset.geoWait;
      const c = pos?.coords;
      for (const [name, v] of [['lat', c?.latitude], ['lon', c?.longitude], ['acc', c?.accuracy]]) {
        let input = form.querySelector(`input[name=${name}]`);
        if (!input) {
          input = Object.assign(document.createElement('input'), { type: 'hidden', name });
          form.append(input);
        }
        input.value = v == null ? '' : String(v);
      }
      form.dataset.geoDone = '1';
      try {
        form.requestSubmit(submitter?.form === form ? submitter : undefined);
      } finally {
        delete form.dataset.geoDone;
      }
    };
    navigator.geolocation.getCurrentPosition(go, () => go(null), { enableHighAccuracy: true, timeout: 10000, maximumAge: 60000 });
  }, true);
})();
