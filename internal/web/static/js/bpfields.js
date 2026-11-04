// «Параметры» of a business process (template "bpfields"): a row a field — «+» adds an empty one, × removes one
// (the last is only cleared), the options are of a select only, «обязательное» — of a parameter only.
document.querySelectorAll('[data-bpfields]').forEach(box => {
  const body = box.querySelector('tbody')
  const kinds = row => {
    const kind = row.querySelector('select[name$=_kind]')
    const opts = row.querySelector('input[name$=_opts]')
    opts.hidden = kind.value !== 'select'
    const multi = row.querySelector('select[name$=_multi]') // «Множественный»: not of a text or a bool
    if (multi) {
      multi.hidden = kind.value === 'text' || kind.value === 'bool'
      if (multi.hidden) multi.value = ''
    }
    const scope = row.querySelector('select[name$=_scope]') // a variable and a constant are not asked: not required
    if (scope) row.querySelector('select[name$=_req]').hidden = scope.value !== 'param'
  }
  const clear = row => {
    row.querySelectorAll('input').forEach(i => { i.value = '' })
    row.querySelectorAll('select').forEach(s => { s.selectedIndex = 0 })
    kinds(row)
  }
  body.querySelectorAll('tr').forEach(kinds)
  box.addEventListener('change', e => {
    if (e.target.matches('select[name$=_kind], select[name$=_scope]')) kinds(e.target.closest('tr'))
  })
  box.addEventListener('click', e => {
    if (e.target.closest('[data-add-row]')) {
      const row = body.lastElementChild.cloneNode(true)
      clear(row)
      body.append(row)
      row.querySelector('input').focus()
    }
    const rm = e.target.closest('[data-rm-row]')
    if (rm) {
      const row = rm.closest('tr')
      if (body.children.length > 1) row.remove()
      else clear(row)
    }
  })
})

// The designer's steps: a double click on a step opens its properties («Изменить»), as an action in the designer
document.querySelectorAll('.bpsteps [data-bpedit]').forEach(li => {
  li.addEventListener('dblclick', e => {
    if (!e.target.closest('a, button, form')) location.href = li.dataset.bpedit
  })
})
