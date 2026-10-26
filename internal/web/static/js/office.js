// «Документы онлайн»: opens the file in ONLYOFFICE with the config go24 signed (data-office).
(function () {
  const box = document.getElementById('office-editor');
  if (!box) return;
  if (!window.DocsAPI) { // the document server is not answering
    box.textContent = box.dataset.failed;
    box.classList.add('empty');
    return;
  }
  new window.DocsAPI.DocEditor('office-editor', JSON.parse(box.dataset.office));
})();
