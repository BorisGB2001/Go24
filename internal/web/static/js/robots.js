// «Роботы»: the robot form shows only the fields of the chosen kind (data-for="task activity ...").
document.querySelectorAll('form[data-robot]').forEach(form => {
  const kind = form.querySelector('select[name=kind]')
  const show = () => form.querySelectorAll('[data-for]').forEach(el => {
    const on = el.dataset.for.split(' ').includes(kind.value)
    el.hidden = !on
    el.querySelectorAll('input, select, textarea').forEach(f => { f.disabled = !on })
  })
  kind.addEventListener('change', show)
  show()
})
