// The 3D viewer. Renders on demand only — on interaction, data change or an animation in
// flight — so it idles at zero GPU cost while the same card runs inference.
import * as THREE from 'three'
import { OrbitControls } from 'three/examples/jsm/controls/OrbitControls.js'
import { RoomEnvironment } from 'three/examples/jsm/environments/RoomEnvironment.js'
import { GLTFLoader } from 'three/examples/jsm/loaders/GLTFLoader.js'
import { MeshoptDecoder } from 'three/examples/jsm/libs/meshopt_decoder.module.js'

export type ViewMode = 'lit' | 'albedo' | 'normals' | 'wireframe' | 'uv'

export interface MeshStats {
  triangles: number
  vertices: number
  materials: number
  textures: string[]
  size: [number, number, number]
  lods: number
}

const MAX_PIXEL_RATIO = 1.5
const bytesCache = new Map<string, ArrayBuffer>()
const BYTES_BUDGET = 96 * 1024 * 1024

/** Content-addressed bytes never change, so a small LRU makes revisits instant. */
async function fetchBytes(url: string, signal?: AbortSignal): Promise<ArrayBuffer> {
  const hit = bytesCache.get(url)
  if (hit) {
    bytesCache.delete(url)
    bytesCache.set(url, hit)
    return hit
  }
  const res = await fetch(url, { signal })
  if (!res.ok) throw new Error(`loading model failed: ${res.status}`)
  const buf = await res.arrayBuffer()
  bytesCache.set(url, buf)
  let total = 0
  for (const b of bytesCache.values()) total += b.byteLength
  for (const [k, b] of bytesCache) {
    if (total <= BYTES_BUDGET) break
    bytesCache.delete(k)
    total -= b.byteLength
  }
  return buf
}

let decoderReady: Promise<void> | null = null
function loader(): GLTFLoader {
  // Meshopt decoding runs in Web Workers, off the renderer thread.
  decoderReady ??= MeshoptDecoder.ready.then(() => MeshoptDecoder.useWorkers?.(2))
  return new GLTFLoader().setMeshoptDecoder(MeshoptDecoder)
}

export async function parseGLB(url: string, signal?: AbortSignal): Promise<THREE.Group> {
  const [buf] = await Promise.all([fetchBytes(url, signal), decoderReady ?? Promise.resolve(loader())])
  const gltf = await loader().parseAsync(buf, '')
  return gltf.scene
}

/** Only LOD0 is shown; LOD1+ nodes ride along in the GLB for engines to use. */
function lodLevel(o: THREE.Object3D): number {
  const m = /_LOD(\d+)$/.exec(o.name)
  return m ? Number(m[1]) : 0
}

function checkerTexture(): THREE.Texture {
  const c = document.createElement('canvas')
  c.width = c.height = 256
  const g = c.getContext('2d')!
  for (let y = 0; y < 8; y++)
    for (let x = 0; x < 8; x++) {
      g.fillStyle = (x + y) % 2 ? '#e8622c' : '#f4efe9'
      g.fillRect(x * 32, y * 32, 32, 32)
    }
  const t = new THREE.CanvasTexture(c)
  t.colorSpace = THREE.SRGBColorSpace
  t.wrapS = t.wrapT = THREE.RepeatWrapping
  return t
}

export class Viewer {
  readonly renderer: THREE.WebGLRenderer
  private scene = new THREE.Scene()
  private camera = new THREE.PerspectiveCamera(35, 1, 0.01, 100)
  private controls: OrbitControls
  private model: THREE.Group | null = null
  private grid: THREE.GridHelper | null = null
  private original = new Map<THREE.Mesh, THREE.Material | THREE.Material[]>()
  private mode: ViewMode = 'lit'
  private frame = 0
  private turntable = false
  private intro: { start: number; from: number } | null = null
  private resize: ResizeObserver
  private sphere = new THREE.Sphere(new THREE.Vector3(), 1)

  /** Places the camera so the model's bounding sphere fills the view at the current aspect. */
  private frameCamera(): void {
    const { center, radius } = this.sphere
    const vFov = THREE.MathUtils.degToRad(this.camera.fov)
    const hFov = 2 * Math.atan(Math.tan(vFov / 2) * this.camera.aspect)
    const dist = (radius / Math.sin(Math.min(vFov, hFov) / 2)) * 1.08
    const dir = new THREE.Vector3(0.55, 0.35, 0.76).normalize()
    this.controls.target.copy(center)
    this.camera.position.copy(center).addScaledVector(dir, dist)
    this.camera.near = radius / 100
    this.camera.far = radius * 100
    this.camera.updateProjectionMatrix()
    this.controls.update()
  }
  private checker: THREE.Texture | null = null

  constructor(private canvas: HTMLCanvasElement) {
    this.renderer = new THREE.WebGLRenderer({ canvas, antialias: true, alpha: true, powerPreference: 'high-performance' })
    this.renderer.setPixelRatio(Math.min(window.devicePixelRatio, MAX_PIXEL_RATIO))
    this.renderer.outputColorSpace = THREE.SRGBColorSpace
    this.renderer.toneMapping = THREE.ACESFilmicToneMapping
    const pmrem = new THREE.PMREMGenerator(this.renderer)
    this.scene.environment = pmrem.fromScene(new RoomEnvironment(), 0.04).texture
    pmrem.dispose()
    const sun = new THREE.DirectionalLight(0xffffff, 1.2)
    sun.position.set(2, 4, 3)
    this.scene.add(sun)
    this.camera.position.set(1.6, 1.1, 2.2)
    this.controls = new OrbitControls(this.camera, canvas)
    this.controls.enableDamping = true
    this.controls.dampingFactor = 0.12
    this.controls.addEventListener('change', () => this.request())
    this.resize = new ResizeObserver(() => this.fit())
    this.resize.observe(canvas)
    this.fit()
  }

  /** Schedules one frame; frames keep coming only while something is still moving. */
  request(): void {
    if (this.frame) return
    this.frame = requestAnimationFrame((t) => {
      this.frame = 0
      let moving = this.controls.update()
      if (this.turntable && this.model) {
        this.model.rotation.y += 0.008
        moving = true
      }
      if (this.intro && this.model) {
        const k = Math.min(1, (t - this.intro.start) / 420)
        const e = 1 - Math.pow(1 - k, 3)
        this.model.scale.setScalar(this.intro.from + (1 - this.intro.from) * e)
        this.model.position.y = (1 - e) * -0.04
        if (k >= 1) this.intro = null
        moving = true
      }
      this.renderer.render(this.scene, this.camera)
      if (moving) this.request()
    })
  }

  private fit(): void {
    const { clientWidth: w, clientHeight: h } = this.canvas
    if (!w || !h) return
    this.renderer.setSize(w, h, false)
    this.camera.aspect = w / h
    this.camera.updateProjectionMatrix()
    this.request()
  }

  async load(url: string, signal?: AbortSignal): Promise<MeshStats> {
    const group = await parseGLB(url, signal)
    if (signal?.aborted) throw new DOMException('aborted', 'AbortError')
    group.traverse((o) => {
      if (lodLevel(o) > 0) o.visible = false
    })
    this.setModel(group)
    return this.stats()
  }

  private setModel(group: THREE.Group): void {
    const keepCamera = this.model !== null
    if (this.model) {
      this.scene.remove(this.model)
      this.disposeModel(this.model)
    }
    this.original.clear()
    const box = new THREE.Box3().setFromObject(group)
    const size = box.getSize(new THREE.Vector3())
    const center = box.getCenter(new THREE.Vector3())
    // Sit the model on the ground at the origin; 1 unit = 1 meter.
    group.position.sub(new THREE.Vector3(center.x, box.min.y, center.z))
    const pivot = new THREE.Group()
    pivot.add(group)
    this.model = pivot
    this.scene.add(pivot)
    pivot.traverse((o) => {
      if ((o as THREE.Mesh).isMesh) this.original.set(o as THREE.Mesh, (o as THREE.Mesh).material)
    })
    const radius = Math.max(size.x, size.y, size.z, 0.1)
    if (this.grid) this.scene.remove(this.grid)
    const cells = Math.max(2, Math.ceil(radius * 2))
    this.grid = new THREE.GridHelper(cells, cells, 0x888888, 0x444444)
    ;(this.grid.material as THREE.Material).transparent = true
    ;(this.grid.material as THREE.Material).opacity = 0.25
    this.scene.add(this.grid)
    this.sphere = new THREE.Box3().setFromObject(pivot).getBoundingSphere(new THREE.Sphere())
    if (!keepCamera) this.frameCamera()
    this.applyMode()
    this.intro = { start: performance.now(), from: 0.94 }
    this.request()
  }

  setMode(mode: ViewMode): void {
    this.mode = mode
    this.applyMode()
    this.request()
  }

  setTurntable(on: boolean): void {
    this.turntable = on
    this.request()
  }

  private modeMaterials: THREE.Material[] = []

  private applyMode(): void {
    for (const m of this.modeMaterials) m.dispose()
    this.modeMaterials = []
    const own = <M extends THREE.Material>(m: M): M => {
      this.modeMaterials.push(m)
      return m
    }
    for (const [mesh, mat] of this.original) {
      const src = (Array.isArray(mat) ? mat[0] : mat) as THREE.MeshStandardMaterial
      switch (this.mode) {
        case 'lit':
          mesh.material = mat
          break
        case 'albedo':
          mesh.material = own(new THREE.MeshBasicMaterial({ map: src.map ?? null, color: src.color ?? 0xffffff }))
          break
        case 'normals':
          mesh.material = own(new THREE.MeshNormalMaterial())
          break
        case 'wireframe':
          mesh.material = own(new THREE.MeshBasicMaterial({ color: 0xe8622c, wireframe: true }))
          break
        case 'uv':
          this.checker ??= checkerTexture()
          mesh.material = own(new THREE.MeshBasicMaterial({ map: this.checker }))
          break
      }
    }
  }

  stats(): MeshStats {
    let triangles = 0
    let vertices = 0
    let lods = 0
    const materials = new Set<THREE.Material>()
    const textures = new Set<string>()
    this.model?.traverse((o) => {
      lods = Math.max(lods, lodLevel(o))
      const mesh = o as THREE.Mesh
      if (!mesh.isMesh || lodLevel(o) > 0) return
      const g = mesh.geometry
      vertices += g.attributes.position?.count ?? 0
      triangles += (g.index ? g.index.count : (g.attributes.position?.count ?? 0)) / 3
      const mats = this.original.get(mesh) ?? mesh.material
      for (const m of Array.isArray(mats) ? mats : [mats]) {
        materials.add(m)
        const map = (m as THREE.MeshStandardMaterial).map
        const img = map?.image as { width?: number; height?: number } | undefined
        if (img?.width) textures.add(`${img.width}×${img.height}`)
      }
    })
    const size = this.model ? new THREE.Box3().setFromObject(this.model).getSize(new THREE.Vector3()) : new THREE.Vector3()
    return {
      triangles: Math.round(triangles),
      vertices,
      materials: materials.size,
      textures: [...textures],
      size: [size.x, size.y, size.z],
      lods,
    }
  }

  /** Renders the current model once into a square image for the library grid. */
  async thumbnail(px = 256): Promise<Blob | null> {
    const prev = new THREE.Vector2()
    this.renderer.getSize(prev)
    const ratio = this.renderer.getPixelRatio()
    const aspect = this.camera.aspect
    const pose = { position: this.camera.position.clone(), target: this.controls.target.clone() }
    this.renderer.setPixelRatio(1)
    this.renderer.setSize(px, px, false)
    this.camera.aspect = 1
    this.frameCamera()
    if (this.grid) this.grid.visible = false
    this.renderer.render(this.scene, this.camera)
    const blob = await new Promise<Blob | null>((done) => this.canvas.toBlob(done, 'image/webp', 0.9))
    if (this.grid) this.grid.visible = true
    this.renderer.setPixelRatio(ratio)
    this.renderer.setSize(prev.x, prev.y, false)
    this.camera.aspect = aspect
    this.camera.position.copy(pose.position)
    this.controls.target.copy(pose.target)
    this.camera.updateProjectionMatrix()
    this.request()
    return blob
  }

  private disposeModel(root: THREE.Object3D): void {
    root.traverse((o) => {
      const mesh = o as THREE.Mesh
      if (!mesh.isMesh) return
      mesh.geometry.dispose()
      const mats = this.original.get(mesh) ?? mesh.material
      for (const m of Array.isArray(mats) ? mats : [mats]) {
        for (const v of Object.values(m)) if (v instanceof THREE.Texture) v.dispose()
        m.dispose()
      }
    })
  }

  dispose(): void {
    cancelAnimationFrame(this.frame)
    this.resize.disconnect()
    this.controls.dispose()
    if (this.model) this.disposeModel(this.model)
    for (const m of this.modeMaterials) m.dispose()
    this.checker?.dispose()
    this.renderer.dispose()
  }
}
