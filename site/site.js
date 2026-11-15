// The page's animations. Everything drawn here is SVG: a small flat-shaded renderer turns one
// lathe profile into the reference picture, the hero's mesh and the four preset flasks.
;(() => {
  'use strict'

  document.documentElement.classList.add('js')
  const reduced = matchMedia('(prefers-reduced-motion: reduce)').matches
  const SVGNS = 'http://www.w3.org/2000/svg'
  const TAU = Math.PI * 2
  const $ = (sel, el = document) => el.querySelector(sel)
  const $$ = (sel, el = document) => Array.from(el.querySelectorAll(sel))
  const clamp01 = (v) => (v < 0 ? 0 : v > 1 ? 1 : v)

  // ── The flask: a profile of (radius, height) turned around its axis ──
  const PROFILE = [
    [0, -1], [0.55, -1], [0.86, -0.72], [0.96, -0.3], [0.84, 0.1], [0.5, 0.4], [0.28, 0.54],
    [0.28, 0.8], [0.4, 0.82], [0.4, 0.92], [0.24, 0.92], [0.27, 1.14], [0, 1.14],
  ]
  // One colour per band between two profile points, bottom to top: liquid, glass, brass lip, cork.
  const BANDS = ['#1d6a62', '#22837a', '#2a9d8e', '#38b5a3', '#9ad9cd', '#cfe9e2', '#dbf0ea', '#a88a52', '#c9a968', '#b99a5e', '#a8774a', '#bb8a58']
  const rgb = (hex) => [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16))
  const BAND_RGB = BANDS.map(rgb)
  const CLAY = [178, 170, 158]
  const TILT = 0.3
  const LIGHT = (() => {
    const l = [-0.5, 0.65, 0.6]
    const n = Math.hypot(...l)
    return l.map((v) => v / n)
  })()

  function subdivide(profile) {
    const out = []
    for (let i = 0; i < profile.length - 1; i++) {
      const [a, b] = [profile[i], profile[i + 1]]
      out.push(a, [(a[0] + b[0]) / 2, (a[1] + b[1]) / 2])
    }
    out.push(profile[profile.length - 1])
    return out
  }

  /** A faceted solid of revolution: `n` sides, flat quads, one outward normal per face. */
  function lathe(profile, n, bandOf = (i) => i) {
    const verts = new Float32Array(profile.length * n * 3)
    profile.forEach(([r, y], i) => {
      for (let j = 0; j < n; j++) {
        const a = (j / n) * TAU
        verts.set([r * Math.cos(a), y, r * Math.sin(a)], (i * n + j) * 3)
      }
    })
    const faces = []
    for (let i = 0; i < profile.length - 1; i++) {
      const [r0, y0] = profile[i]
      const [r1, y1] = profile[i + 1]
      const len = Math.hypot(r1 - r0, y1 - y0)
      if (!len) continue
      // The profile runs up the outside, so outward is to the right of its direction.
      const nr = (y1 - y0) / len
      const ny = -(r1 - r0) / len
      for (let j = 0; j < n; j++) {
        const am = ((j + 0.5) / n) * TAU
        const k = (j + 1) % n
        faces.push({
          v: [i * n + j, i * n + k, (i + 1) * n + k, (i + 1) * n + j],
          n: [nr * Math.cos(am), ny, nr * Math.sin(am)],
          cy: (y0 + y1) / 2,
          band: bandOf(i),
        })
      }
    }
    return { verts, faces }
  }

  function makeView(svg, scale, cx, cy, maxFaces) {
    const g = $('.mesh', svg)
    const pool = []
    for (let i = 0; i < maxFaces; i++) {
      const p = document.createElementNS(SVGNS, 'polygon')
      p.setAttribute('stroke-width', '0.8')
      p.setAttribute('stroke-linejoin', 'round')
      g.appendChild(p)
      pool.push(p)
    }
    return { pool, scale, cx, cy, used: 0, proj: new Float32Array(0), order: [], depth: [], shade: [] }
  }

  /**
   * Draws `mesh` turned by `theta`. `mode` is the look of an unpainted face ('wire' or 'clay');
   * faces below `paintY` take their band's colour, faces above `revealY` are not there yet.
   */
  function draw(view, mesh, theta, { mode = 'clay', paintY = -Infinity, revealY = Infinity, edges = false } = {}) {
    const { verts, faces } = mesh
    const count = verts.length / 3
    if (view.proj.length < count * 3) view.proj = new Float32Array(count * 3)
    const P = view.proj
    const ct = Math.cos(theta), st = Math.sin(theta)
    const cp = Math.cos(TILT), sp = Math.sin(TILT)
    for (let i = 0; i < count; i++) {
      const x = verts[i * 3], y = verts[i * 3 + 1], z = verts[i * 3 + 2]
      const x1 = x * ct + z * st
      const z1 = -x * st + z * ct
      const y2 = y * cp - z1 * sp
      const z2 = y * sp + z1 * cp
      const k = view.scale * (1 + z2 * 0.1)
      P[i * 3] = view.cx + x1 * k
      P[i * 3 + 1] = view.cy - y2 * k
      P[i * 3 + 2] = z2
    }

    const { order, depth, shade } = view
    order.length = 0
    for (let f = 0; f < faces.length; f++) {
      const face = faces[f]
      if (face.cy > revealY) continue
      const [nx, ny, nz] = face.n
      const x1 = nx * ct + nz * st
      const z1 = -nx * st + nz * ct
      const y2 = ny * cp - z1 * sp
      const z2 = ny * sp + z1 * cp
      if (z2 <= 0.02) continue
      const v = face.v
      depth[f] = P[v[0] * 3 + 2] + P[v[1] * 3 + 2] + P[v[2] * 3 + 2] + P[v[3] * 3 + 2]
      shade[f] = 0.4 + 0.6 * Math.max(0, x1 * LIGHT[0] + y2 * LIGHT[1] + z2 * LIGHT[2])
      order.push(f)
    }
    order.sort((a, b) => depth[a] - depth[b])

    for (let k = 0; k < order.length; k++) {
      const face = faces[order[k]]
      const el = view.pool[k]
      const v = face.v
      let pts = ''
      for (let c = 0; c < 4; c++) pts += P[v[c] * 3].toFixed(1) + ',' + P[v[c] * 3 + 1].toFixed(1) + ' '
      el.setAttribute('points', pts)
      const s = shade[order[k]]
      const painted = face.cy < paintY
      if (!painted && mode === 'wire') {
        el.setAttribute('fill', '#0d0c0b')
        el.setAttribute('stroke', '#f0934a')
      } else {
        const c = painted ? BAND_RGB[face.band] : CLAY
        const col = `rgb(${(c[0] * s) | 0},${(c[1] * s) | 0},${(c[2] * s) | 0})`
        el.setAttribute('fill', col)
        el.setAttribute('stroke', edges || !painted ? 'rgba(13,12,11,0.5)' : col)
      }
    }
    for (let k = order.length; k < view.used; k++) view.pool[k].setAttribute('points', '')
    view.used = order.length
  }

  // ── One animation loop for everything on screen ──
  const actors = new Map()
  let raf = 0
  function loop(now) {
    raf = 0
    let any = false
    for (const a of actors.values()) {
      if (!a.visible) continue
      a.frame(now)
      any = true
    }
    if (any && !document.hidden) raf = requestAnimationFrame(loop)
  }
  const kick = () => {
    if (!raf && !reduced) raf = requestAnimationFrame(loop)
  }
  const watcher = new IntersectionObserver(
    (entries) => {
      for (const e of entries) actors.get(e.target).visible = e.isIntersecting
      kick()
    },
    { rootMargin: '60px' },
  )
  function actor(el, frame) {
    actors.set(el, { visible: false, frame })
    if (reduced) frame(1400)
    else watcher.observe(el)
  }
  document.addEventListener('visibilitychange', kick)

  // ── The reference picture: the same profile, drawn flat ──
  {
    const g = $('#ref-flask')
    const S = 86, CX = 100, CY = 128
    const poly = (pts, fill, opacity) => {
      const p = document.createElementNS(SVGNS, 'polygon')
      p.setAttribute('points', pts.map(([r, y]) => `${(CX + r * S).toFixed(1)},${(CY - y * S).toFixed(1)}`).join(' '))
      p.setAttribute('fill', fill)
      if (opacity) p.setAttribute('opacity', opacity)
      g.appendChild(p)
    }
    for (let i = 0; i < PROFILE.length - 1; i++) {
      const [r0, y0] = PROFILE[i], [r1, y1] = PROFILE[i + 1]
      if (y0 === y1) continue
      poly([[-r0, y0], [r0, y0], [r1, y1], [-r1, y1]], BANDS[i])
    }
    for (let i = 0; i < PROFILE.length - 1; i++) {
      const [r0, y0] = PROFILE[i], [r1, y1] = PROFILE[i + 1]
      if (y0 === y1) continue
      poly([[-0.78 * r0, y0], [-0.5 * r0, y0], [-0.5 * r1, y1], [-0.78 * r1, y1]], '#fff', '0.22')
      poly([[0.45 * r0, y0], [r0, y0], [r1, y1], [0.45 * r1, y1]], '#000', '0.16')
    }
  }

  // ── Hero: a job from picture to file, on a loop ──
  {
    const mock = $('#mock')
    const dense = lathe(subdivide(PROFILE), 20, (i) => i >> 1)
    const low = lathe(PROFILE, 9)
    const view = makeView($('#hero-view'), 108, 160, 160, dense.faces.length)
    const segs = $$('#hero-track .seg')
    const pill = $('#pill')
    const stage = $('#stage')
    const SPANS = [[0, 2], [2, 6], [6, 9], [9, 10]]
    const LOOP = 14.5
    const LABELS = [
      [0, 'prepare · remove background'], [1.1, 'prepare · frame at 1024 px'], [2, 'shape · generate'],
      [4.6, 'shape · clean up'], [5.2, 'low-poly · 5,000 triangles'], [6, 'paint · base color'],
      [8.3, 'bake · normal and AO'], [9, 'finish · LODs and GLB'], [10, 'potion.glb'],
    ]
    const set = (key, value) => {
      if (mock.dataset[key] !== value) mock.dataset[key] = value
    }
    let t0 = null
    let label = -1
    let wasDone = null

    actor(mock, (now) => {
      if (t0 === null) t0 = now
      let t = reduced ? 12 : (now - t0) / 1000
      if (t >= LOOP) {
        t0 = now
        t = 0
      }
      set('phase', t < 2 ? 'prepare' : t < 6 ? 'shape' : t < 9 ? 'paint' : t < 10 ? 'finish' : 'done')
      set('cut', t > 0.9 ? '1' : '0')
      set('out', t > LOOP - 0.45 ? '1' : '0')

      SPANS.forEach(([a, b], k) => {
        const cls = t >= b ? 'seg succeeded' : t >= a ? 'seg running' : 'seg'
        if (segs[k].className !== cls) segs[k].className = cls
        segs[k].style.setProperty('--p', t >= a && t < b ? ((t - a) / (b - a)).toFixed(3) : '0')
      })

      let l = 0
      while (l + 1 < LABELS.length && t >= LABELS[l + 1][0]) l++
      if (l !== label) stage.textContent = LABELS[(label = l)][1]
      const done = t >= 10
      if (done !== wasDone) {
        wasDone = done
        pill.className = done ? 'pill ok' : 'pill'
        pill.lastElementChild.textContent = done ? 'Done' : 'Forging'
      }

      const theta = now * 0.0006
      const sweep = (from) => -1.1 + 2.4 * clamp01((t - from) / 2.2)
      if (t < 2) return
      if (t < 5.2) draw(view, dense, theta, { mode: 'wire', revealY: sweep(2) })
      else if (t < 6) draw(view, low, theta, { mode: 'clay' })
      else draw(view, low, theta, { mode: 'clay', paintY: t < 9 ? sweep(6) : Infinity })
    })
  }

  // ── Presets: the same flask at four budgets ──
  $$('.pv').forEach((svg, i) => {
    const n = Number(svg.dataset.n)
    const mesh = svg.dataset.dense ? lathe(subdivide(PROFILE), n, (b) => b >> 1) : lathe(PROFILE, n)
    const view = makeView(svg, 64, 100, 104, mesh.faces.length)
    const painted = svg.dataset.mode === 'paint'
    actor(svg, (now) => draw(view, mesh, now * 0.0005 + i * 0.8, { mode: 'clay', paintY: painted ? Infinity : -Infinity, edges: true }))
  })

  // ── Pipeline: the track heats one stage at a time, and its card lights with it ──
  {
    const track = $('#pipe-track')
    const segs = $$('.seg', track)
    const cards = $$('#stages li')
    const paint = (at) => {
      segs.forEach((seg, k) => {
        seg.className = k < at ? 'seg succeeded' : k === at ? 'seg running' : 'seg'
        seg.style.setProperty('--p', k === at ? '1' : '0')
        cards[k].classList.toggle('done', k < at)
        cards[k].classList.toggle('on', k === at)
      })
    }
    if (reduced) paint(segs.length)
    else {
      let at = -1
      let timer = 0
      const step = () => {
        at++
        if (at > segs.length + 2) {
          // Start over cold, without the fills sliding back.
          at = -1
          segs.forEach((seg) => (seg.firstElementChild.style.transition = 'none'))
          paint(-1)
          requestAnimationFrame(() => segs.forEach((seg) => (seg.firstElementChild.style.transition = '')))
          return
        }
        paint(at)
      }
      new IntersectionObserver(([e]) => {
        clearInterval(timer)
        if (e.isIntersecting) timer = setInterval(step, 1000)
      }).observe(track)
    }
  }

  // ── Terminal: the commands type themselves once, when first seen ──
  {
    const term = $('#term')
    const lines = $$('.t', term)
    const caret = $('.caret', term)
    if (!reduced && lines.length) {
      const all = $$('.c, .p, .t', term)
      all.forEach((el) => (el.style.visibility = 'hidden'))
      const typed = lines.map((el) => {
        const rest = document.createElement('span')
        rest.textContent = el.textContent
        rest.style.visibility = 'hidden'
        const done = document.createTextNode('')
        el.textContent = ''
        el.append(done, rest)
        return { el, done, rest }
      })
      const type = (i) => {
        if (i >= typed.length) return
        const { el, done, rest } = typed[i]
        // The comment and the prompt that come before this command appear with it.
        for (let p = el.previousElementSibling; p && !p.classList.contains('t'); p = p.previousElementSibling) p.style.visibility = ''
        el.style.visibility = ''
        el.insertBefore(caret, rest)
        const tick = () => {
          if (!rest.textContent) return setTimeout(() => type(i + 1), 520)
          done.data += rest.textContent[0]
          rest.textContent = rest.textContent.slice(1)
          setTimeout(tick, 16 + Math.random() * 28)
        }
        setTimeout(tick, 380)
      }
      const seen = new IntersectionObserver(
        ([e]) => {
          if (!e.isIntersecting) return
          seen.disconnect()
          type(0)
        },
        { threshold: 0.5 },
      )
      seen.observe(term)
    }
  }

  // ── Content rises in as it is reached; siblings follow one another ──
  {
    const items = $$('.reveal')
    if (reduced) items.forEach((el) => el.classList.add('in'))
    else {
      const rise = new IntersectionObserver(
        (entries) => {
          for (const e of entries) {
            if (!e.isIntersecting) continue
            e.target.classList.add('in')
            rise.unobserve(e.target)
          }
        },
        { rootMargin: '0px 0px -8% 0px' },
      )
      items.forEach((el) => {
        const i = Array.from(el.parentElement.children).filter((c) => c.classList.contains('reveal')).indexOf(el)
        el.style.setProperty('--i', Math.min(i, 8))
        rise.observe(el)
      })
    }
  }

  if (reduced) $('#net').pauseAnimations()
})()
