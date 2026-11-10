import { useEffect, useRef, useState, type KeyboardEvent } from 'react'
import { Progress } from '../components/Controls'
import { AlertIcon, BendTestIcon, ResetViewIcon, SkeletonIcon, TurntableIcon } from '../components/icons'
import { count } from '../lib/format'
import { Viewer, type MeshStats, type ViewMode } from './scene'

const modes: { id: ViewMode; label: string; title: string }[] = [
  { id: 'lit', label: 'Lit', title: 'Materials under studio light' },
  { id: 'albedo', label: 'Base color', title: 'The base colour map alone, without light' },
  { id: 'normals', label: 'Normals', title: 'Which way each surface faces' },
  { id: 'wireframe', label: 'Wireframe', title: 'The triangles' },
  { id: 'uv', label: 'UV', title: 'A checker through the UV layout: stretching shows as uneven squares' },
]

/** Radians a press of an arrow key turns the view. */
const TURN = Math.PI / 12

type State = { kind: 'loading' } | { kind: 'ready' } | { kind: 'error'; reason: string }

/**
 * The 3D viewer and its controls. `rigged` (what the library already knows) reserves the rig
 * controls before the model has loaded, so the toolbar never changes size.
 */
export function ViewerView({
  url,
  name,
  rigged = false,
  placeholder,
  onStats,
}: {
  url: string | undefined
  name?: string
  rigged?: boolean
  /** A picture to show, blurred, while the model loads. */
  placeholder?: string
  onStats?: (s: MeshStats) => void
}) {
  const stage = useRef<HTMLDivElement>(null)
  const startError = useRef('')
  const viewer = useRef<Viewer | null>(null)
  // A canvas whose context was lost cannot get another: "Try again" starts over with a new one.
  const [attempt, setAttempt] = useState(0)
  const [mode, setMode] = useState<ViewMode>('lit')
  const [spin, setSpin] = useState(false)
  const [state, setState] = useState<State>({ kind: 'loading' })
  const [bones, setBones] = useState(false)
  const [hasBones, setHasBones] = useState(false)
  const [bending, setBending] = useState(false)
  const [lods, setLods] = useState<number[]>([])
  const [lod, setLod] = useState(0)
  const statsRef = useRef(onStats)
  useEffect(() => {
    statsRef.current = onStats
  })

  // The canvas is made here and not in the markup: a canvas whose context was given back cannot
  // draw again, and React may run this effect twice on the same element.
  useEffect(() => {
    const el = document.createElement('canvas')
    stage.current!.appendChild(el)
    let v: Viewer | null = null
    try {
      v = new Viewer(el)
      startError.current = ''
    } catch (err) {
      startError.current = `This computer could not start a 3D view: ${(err as Error).message}`
    }
    viewer.current = v
    const onLost = (): void => setState({ kind: 'error', reason: 'The graphics driver reset the 3D view.' })
    el.addEventListener('webglcontextlost', onLost)
    return () => {
      // Before the viewer gives its context back, which is a loss of its own.
      el.removeEventListener('webglcontextlost', onLost)
      v?.dispose()
      el.remove()
      viewer.current = null
    }
  }, [attempt])

  useEffect(() => {
    const v = viewer.current
    const ctrl = new AbortController()
    const run = v ? (url ? v.load(url, ctrl.signal) : undefined) : Promise.reject(new Error(startError.current))
    run
      ?.then((s) => {
        setState({ kind: 'ready' })
        setHasBones(s.bones > 0)
        setLods(s.lodTriangles.length > 1 ? s.lodTriangles : [])
        statsRef.current?.(s)
      })
      .catch((e: unknown) => {
        if ((e as Error).name !== 'AbortError') setState({ kind: 'error', reason: (e as Error).message || String(e) })
      })
    return () => ctrl.abort()
  }, [url, attempt])

  const retry = (): void => {
    setState({ kind: 'loading' })
    setMode('lit')
    setSpin(false)
    setBones(false)
    setBending(false)
    setLods([])
    setLod(0)
    setAttempt((n) => n + 1)
  }

  const ready = state.kind === 'ready'
  const onKey = (e: KeyboardEvent): void => {
    const v = viewer.current
    if (!v || !ready || e.ctrlKey || e.metaKey || e.altKey) return
    switch (e.key) {
      case 'ArrowLeft':
        v.orbitBy(-TURN, 0)
        break
      case 'ArrowRight':
        v.orbitBy(TURN, 0)
        break
      case 'ArrowUp':
        v.orbitBy(0, TURN / 2)
        break
      case 'ArrowDown':
        v.orbitBy(0, -TURN / 2)
        break
      case '+':
      case '=':
        v.zoomBy(0.87)
        break
      case '-':
      case '_':
        v.zoomBy(1.15)
        break
      case '0':
      case 'f':
      case 'F':
      case 'Home':
        v.resetView()
        break
      default:
        return
    }
    e.preventDefault()
  }

  const showRig = rigged || hasBones
  return (
    <div className="viewer">
      <div
        ref={stage}
        className="viewer-stage"
        role="group"
        tabIndex={0}
        aria-label={`3D view${name ? ` of ${name}` : ''}. Arrow keys turn it, plus and minus zoom, 0 frames it.`}
        onKeyDown={onKey}
        onDoubleClick={() => viewer.current?.resetView()}
      />
      {state.kind === 'loading' && (
        <div className="viewer-status">
          {placeholder && <img className="viewer-placeholder" src={placeholder} alt="" />}
          <div className="viewer-progress">
            <Progress label="Loading the model" />
          </div>
        </div>
      )}
      {state.kind === 'error' && (
        <div className="viewer-status" role="alert">
          <AlertIcon size={20} />
          <strong>This model could not be shown</strong>
          <span className="selectable">{state.reason}</span>
          <button className="btn sm" onClick={retry}>
            Try again
          </button>
        </div>
      )}
      {lods.length > 1 && (
        <select
          className="input sm viewer-lod"
          aria-label="Level of detail"
          title="The level of detail to look at. Game engines switch to the lower ones at a distance."
          value={lod}
          disabled={!ready}
          onChange={(e) => {
            const level = Number(e.target.value)
            setLod(level)
            viewer.current?.setLod(level)
          }}
        >
          {lods.map((tris, level) => (
            <option key={level} value={level}>
              LOD {level} · {count(tris)} tris
            </option>
          ))}
        </select>
      )}
      <div className="viewer-tools" role="toolbar" aria-label="View">
        <button
          className="btn ghost icon sm"
          aria-label="Turntable"
          title="Turn the model slowly"
          aria-pressed={spin}
          disabled={!ready}
          onClick={() => {
            setSpin(!spin)
            viewer.current?.setTurntable(!spin)
          }}
        >
          <TurntableIcon />
        </button>
        {showRig && (
          <>
            <button
              className="btn ghost icon sm"
              aria-label="Skeleton"
              title="Show the bones over the mesh"
              aria-pressed={bones}
              disabled={!ready || !hasBones}
              onClick={() => {
                setBones(!bones)
                viewer.current?.setSkeleton(!bones)
              }}
            >
              <SkeletonIcon />
            </button>
            <button
              className="btn ghost icon sm"
              aria-label="Bend test"
              title="Flex the limbs to check how the skin follows the bones"
              aria-pressed={bending}
              disabled={!ready || !hasBones || bending}
              onClick={() => {
                const v = viewer.current
                if (!v) return
                setBending(true)
                void v.playBend().then(() => {
                  if (viewer.current === v) setBending(false)
                })
              }}
            >
              <BendTestIcon />
            </button>
          </>
        )}
        <button
          className="btn ghost icon sm"
          aria-label="Reset view"
          title="Frame the model again (0)"
          disabled={!ready}
          onClick={() => {
            viewer.current?.resetView()
          }}
        >
          <ResetViewIcon />
        </button>
      </div>
      <div className="viewer-modes" role="toolbar" aria-label="Shading">
        {modes.map((m) => (
          <button
            key={m.id}
            title={m.title}
            aria-pressed={mode === m.id}
            disabled={!ready}
            onClick={() => {
              setMode(m.id)
              viewer.current?.setMode(m.id)
            }}
          >
            {m.label}
          </button>
        ))}
      </div>
    </div>
  )
}
