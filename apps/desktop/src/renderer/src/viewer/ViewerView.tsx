import { useEffect, useRef, useState } from 'react'
import { Corners } from '../components/icons'
import { Viewer, type MeshStats, type ViewMode } from './scene'

const modes: { id: ViewMode; label: string }[] = [
  { id: 'lit', label: 'Lit' },
  { id: 'albedo', label: 'Albedo' },
  { id: 'normals', label: 'Normals' },
  { id: 'wireframe', label: 'Wire' },
  { id: 'uv', label: 'UV' },
]

export function ViewerView({ url, onStats }: { url: string | undefined; onStats?: (s: MeshStats) => void }) {
  const canvas = useRef<HTMLCanvasElement>(null)
  const viewer = useRef<Viewer | null>(null)
  const [mode, setMode] = useState<ViewMode>('lit')
  const [spin, setSpin] = useState(false)
  const [state, setState] = useState<'idle' | 'loading' | 'ready' | 'error'>('idle')
  const [rigged, setRigged] = useState(false)
  const [bones, setBones] = useState(false)
  const [bending, setBending] = useState(false)
  const statsRef = useRef(onStats)
  useEffect(() => {
    statsRef.current = onStats
  })

  useEffect(() => {
    viewer.current = new Viewer(canvas.current!)
    return () => {
      viewer.current?.dispose()
      viewer.current = null
    }
  }, [])

  useEffect(() => {
    if (!url || !viewer.current) return
    const ctrl = new AbortController()
    setState('loading')
    // A new model drops the old one's overlay and stops its bend test.
    setRigged(false)
    setBones(false)
    setBending(false)
    viewer.current
      .load(url, ctrl.signal)
      .then((s) => {
        setState('ready')
        setRigged(s.bones > 0)
        statsRef.current?.(s)
      })
      .catch((e: unknown) => {
        if ((e as Error).name !== 'AbortError') setState('error')
      })
    return () => ctrl.abort()
  }, [url])

  return (
    <div className="viewer">
      <canvas ref={canvas} tabIndex={0} aria-label="3D model viewer" />
      <span className="frame" aria-hidden />
      <Corners />
      <div className={`viewer-loading ${state === 'ready' ? 'hidden' : ''}`}>
        {state === 'error' ? 'Could not load this model' : state === 'loading' ? <div className="skeleton" style={{ width: 120, height: 8 }} /> : 'No model yet'}
      </div>
      <div className="viewer-modes" role="toolbar" aria-label="View mode">
        {modes.map((m) => (
          <button
            key={m.id}
            aria-pressed={mode === m.id}
            onClick={() => {
              setMode(m.id)
              viewer.current?.setMode(m.id)
            }}
          >
            {m.label}
          </button>
        ))}
        <button
          aria-pressed={spin}
          onClick={() => {
            setSpin(!spin)
            viewer.current?.setTurntable(!spin)
          }}
        >
          Turntable
        </button>
        {rigged && (
          <>
            <button
              className="viewer-rig"
              aria-pressed={bones}
              title="Show the bones over the mesh"
              onClick={() => {
                setBones(!bones)
                viewer.current?.setSkeleton(!bones)
              }}
            >
              Skeleton
            </button>
            <button
              className="viewer-rig"
              aria-pressed={bending}
              disabled={bending}
              title="Flex the limbs to check how the skin follows the bones"
              onClick={() => {
                const v = viewer.current
                if (!v) return
                setBending(true)
                void v.playBend().then(() => {
                  if (viewer.current === v) setBending(false)
                })
              }}
            >
              Bend test
            </button>
          </>
        )}
      </div>
    </div>
  )
}
