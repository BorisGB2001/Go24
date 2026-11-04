// The steps of a template as a list or a scheme (remembered by the viewer), as the graphical designer:
// a block dragged to a «+» of the scheme (a step of the list — onto another step) moves there with its steps.
(() => {
  const card = document.querySelector('.bpscheme')?.closest('.card')
  if (!card) return
  const get = k => { try { return localStorage.getItem(k) } catch { return null } }
  const put = (k, v) => { try { localStorage.setItem(k, v) } catch {} }
  const view = v => {
    card.classList.toggle('bpview-list', v !== 'scheme')
    card.classList.toggle('bpview-scheme', v === 'scheme')
    card.querySelectorAll('[data-bpview]').forEach(b => b.classList.toggle('on', b.dataset.bpview === v))
  }
  view(get('bp.view') === 'scheme' ? 'scheme' : 'list')
  card.querySelectorAll('[data-bpview]').forEach(b => b.addEventListener('click', () => {
    put('bp.view', b.dataset.bpview)
    view(b.dataset.bpview)
  }))

  const move = fields => {
    const f = document.createElement('form')
    f.method = 'post'
    f.action = location.pathname
    for (const [k, v] of Object.entries({ op: 'move', ...fields })) {
      const i = document.createElement('input')
      i.type = 'hidden'
      i.name = k
      i.value = v
      f.appendChild(i)
    }
    document.body.appendChild(f)
    f.submit()
  }
  let dragged = null // {a, b} of the block
  const start = (el, a, b) => {
    el.addEventListener('dragstart', e => {
      if (e.target.closest('a, button, form') && e.target !== el) return
      dragged = { a, b }
      el.classList.add('dragging')
      e.dataTransfer.effectAllowed = 'move'
      e.dataTransfer.setData('text/plain', a + '-' + b)
    })
    el.addEventListener('dragend', () => {
      dragged = null
      el.classList.remove('dragging')
    })
  }
  const target = (el, ok, fields) => {
    el.addEventListener('dragover', e => {
      if (!dragged || !ok()) return
      e.preventDefault()
      el.classList.add('drop')
    })
    el.addEventListener('dragleave', () => el.classList.remove('drop'))
    el.addEventListener('drop', e => {
      el.classList.remove('drop')
      if (!dragged || !ok()) return
      e.preventDefault()
      move({ a: dragged.a, b: dragged.b, ...fields() })
    })
  }

  card.querySelectorAll('.bpscheme .bpblock').forEach(el => start(el, +el.dataset.a, +el.dataset.b))
  // a block goes neither into itself nor right by it
  card.querySelectorAll('.bpscheme .bpgap').forEach(g => target(g,
    () => g.dataset.at - 1 < dragged.a || g.dataset.at - 1 > dragged.b,
    () => ({ at: g.dataset.at, from: g.dataset.from, br: g.dataset.br })))
  card.querySelectorAll('.bpsteps > li').forEach((li, i) => {
    li.draggable = true
    start(li, i, i + 1)
    // up — before the step dropped on, down — after it
    target(li, () => i !== dragged.a, () => ({ at: i < dragged.a ? i + 1 : i + 2 }))
  })

  // a double click on a block opens its properties, as on a step of the list
  card.querySelectorAll('.bpscheme [data-bpedit]').forEach(el => el.addEventListener('dblclick', e => {
    if (!e.target.closest('a, button, form')) location.href = el.dataset.bpedit
  }))
})()
