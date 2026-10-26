// «Роботы»: the robot form shows only the fields of the chosen kind (data-for="task activity ...").
// A row of a field and its value (data-fieldrow: the fields robot's rows, the condition) shows the value's control
// the field is chosen with (data-pick of its option: typed, a dictionary, people, yes/no, a list's options).
document.querySelectorAll('form[data-robot]').forEach(form => {
  const kind = form.querySelector('select[name=kind]')
  const pickValue = row => {
    const opt = row.querySelector('select[data-field]').selectedOptions[0]
    const pick = (opt && opt.dataset.pick) || ''
    const op = row.querySelector('select[data-op]')
    const none = !opt || !opt.value || row.closest('[hidden]') || (op && (op.value === 'empty' || op.value === 'filled'))
    if (op) op.hidden = op.disabled = !opt || !opt.value || !!row.closest('[hidden]')
    row.querySelectorAll('[data-pick]:not(option)').forEach(f => {
      const on = !none && f.dataset.pick === pick && (pick !== 'list' || f.dataset.of === opt.value)
      f.hidden = !on
      f.disabled = !on
    })
  }
  const show = () => {
    form.querySelectorAll('[data-for]').forEach(el => {
      const on = el.dataset.for.split(' ').includes(kind.value)
      el.hidden = !on
      el.querySelectorAll('input, select, textarea').forEach(f => { f.disabled = !on })
    })
    form.querySelectorAll('[data-fieldrow]').forEach(pickValue)
  }
  form.querySelectorAll('[data-fieldrow]').forEach(row => {
    row.querySelectorAll('select[data-field], select[data-op]').forEach(s => s.addEventListener('change', () => pickValue(row)))
  })
  kind.addEventListener('change', show)
  show()
})
