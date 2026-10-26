// A card's phones and e-mails in its form (data-crmcomms): «+ добавить» adds a row like the last (empty, the default
// type), «×» removes a row (the only one is emptied instead). The first row is the card's main phone or e-mail.
document.querySelectorAll('[data-crmcomms]').forEach(box => {
  box.addEventListener('click', e => {
    const add = e.target.closest('[data-comm-add]')
    const del = e.target.closest('[data-comm-del]')
    if (add) {
      const rows = box.querySelectorAll('.crmcomm')
      if (rows.length >= 10) return
      const row = rows[rows.length - 1].cloneNode(true)
      row.querySelector('[name=comm_value]').value = ''
      row.querySelector('select').selectedIndex = 0
      add.before(row)
      row.querySelector('[name=comm_value]').focus()
    } else if (del) {
      const row = del.closest('.crmcomm')
      if (box.querySelectorAll('.crmcomm').length > 1) row.remove()
      else row.querySelector('[name=comm_value]').value = ''
    }
  })
})
