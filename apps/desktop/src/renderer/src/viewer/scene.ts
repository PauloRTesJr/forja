// The 3D viewer. Renders on demand only — on interaction, data change or an animation in
// flight — so it idles at zero GPU cost while the same card runs inference.
import * as THREE from 'three'
import { OrbitControls } from 'three/examples/jsm/controls/OrbitControls.js'
import { RoomEnvironment } from 'three/examples/jsm/environments/RoomEnvironment.js'
import { GLTFLoader } from 'three/examples/jsm/loaders/GLTFLoader.js'
import { MeshoptDecoder } from 'three/examples/jsm/libs/meshopt_decoder.module.js'
import { bendPhase, genericAngle, HELD, humanoidPose, skeletonOf, type Phase, type Skeleton } from './rig'

export type ViewMode = 'lit' | 'albedo' | 'normals' | 'wireframe' | 'uv'

export interface MeshStats {
  triangles: number
  vertices: number
  materials: number
  /** Sizes of the base colour maps, such as "2048×2048". */
  textures: string[]
  /** Every texture map of the full-detail mesh, by what it is for. */
  maps: { name: string; size: string }[]
  size: [number, number, number]
  /** The highest level of detail below the full one; 0 when there is only one. */
  lods: number
  /** Triangles of each level of detail, the full one first. */
  lodTriangles: number[]
  /** Bones that deform the full-detail mesh; 0 when the asset is not rigged. */
  bones: number
  skeleton?: Skeleton
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

/** The level a node's own name gives it (`_LOD<n>`), if it names one. */
function namedLevel(o: THREE.Object3D): number | undefined {
  const m = /_LOD(\d+)$/.exec(o.name)
  return m ? Number(m[1]) : undefined
}

/**
 * The level of detail an object belongs to: its own name's or the nearest named ancestor's
 * (a node with several primitives loads as a named group of meshes), else the full one.
 */
function lodLevel(o: THREE.Object3D): number {
  for (let at: THREE.Object3D | null = o; at; at = at.parent) {
    const level = namedLevel(at)
    if (level !== undefined) return level
  }
  return 0
}

/** Thrown by `thumbnail` when the picture has nothing in it: a lost context draws nothing. */
export class BlankFrameError extends Error {
  override name = 'BlankFrameError'
}

/** True when no pixel of an RGBA image is visible at all. */
export function isBlank(rgba: ArrayLike<number>): boolean {
  for (let i = 3; i < rgba.length; i += 4) if (rgba[i]! > 8) return false
  return true
}

/**
 * A skinned mesh's bounds come from its bones' world matrices, cached on first use. They are
 * stale until the bones' matrices are, so refresh both before measuring.
 */
function refreshSkinnedBounds(root: THREE.Object3D): void {
  root.updateMatrixWorld(true)
  root.traverse((o) => {
    const mesh = o as THREE.SkinnedMesh
    if (!mesh.isSkinnedMesh) return
    mesh.computeBoundingBox()
    mesh.computeBoundingSphere()
  })
}

/** The bones of the full-detail skinned meshes, each once, and each bone's rest rotation. */
function bonesOf(root: THREE.Object3D): Map<THREE.Bone, THREE.Quaternion> {
  const rest = new Map<THREE.Bone, THREE.Quaternion>()
  root.traverse((o) => {
    const mesh = o as THREE.SkinnedMesh
    if (!mesh.isSkinnedMesh || lodLevel(o) > 0) return
    for (const b of mesh.skeleton.bones) if (!rest.has(b)) rest.set(b, b.quaternion.clone())
  })
  return rest
}

/** One turn of the turntable in about 13 seconds, whatever the display's refresh rate. */
const TURN_RAD_PER_MS = 0.00048

const reducedMotion = (): boolean => window.matchMedia?.('(prefers-reduced-motion: reduce)').matches ?? false
const HELD_MS = 1000

/** A material's texture maps by what each is for; glTF packs occlusion, roughness and metallic into one image. */
function textureMaps(m: THREE.MeshStandardMaterial): [string, THREE.Texture | null | undefined][] {
  const packed = !!m.roughnessMap && m.roughnessMap === m.metalnessMap
  const withOcclusion = packed && m.aoMap === m.roughnessMap
  return [
    ['Base color', m.map],
    ['Normal', m.normalMap],
    ...(packed
      ? ([[withOcclusion ? 'Occlusion, roughness, metallic' : 'Roughness, metallic', m.roughnessMap]] as [string, THREE.Texture][])
      : ([
          ['Roughness', m.roughnessMap],
          ['Metallic', m.metalnessMap],
        ] as [string, THREE.Texture | null][])),
    ...(withOcclusion ? [] : ([['Occlusion', m.aoMap]] as [string, THREE.Texture | null][])),
    ['Emissive', m.emissiveMap],
  ]
}

function checkerTexture(): THREE.Texture {
  const c = document.createElement('canvas')
  c.width = c.height = 256
  const g = c.getContext('2d')!
  for (let y = 0; y < 8; y++)
    for (let x = 0; x < 8; x++) {
      g.fillStyle = (x + y) % 2 ? '#7e848d' : '#e9ebee'
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
  private rest = new Map<THREE.Bone, THREE.Quaternion>()
  private skeleton: Skeleton | undefined
  private helper: THREE.SkeletonHelper | null = null
  private bend: { start: number; done: () => void } | null = null
  private held: ReturnType<typeof setTimeout> | undefined
  private lod = 0
  private lastFrame = 0
  private disposed = false
  private home = new THREE.Vector3(0.55, 0.35, 0.76).normalize()

  /** Places the camera so the model's bounding sphere fills the view at the current aspect. */
  private frameCamera(): void {
    const { center, radius } = this.sphere
    const vFov = THREE.MathUtils.degToRad(this.camera.fov)
    const hFov = 2 * Math.atan(Math.tan(vFov / 2) * this.camera.aspect)
    const dist = (radius / Math.sin(Math.min(vFov, hFov) / 2)) * 1.08
    this.controls.target.copy(center)
    this.camera.position.copy(center).addScaledVector(this.home, dist)
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
    if (this.frame || this.disposed) return
    this.frame = requestAnimationFrame((t) => {
      this.frame = 0
      // Time since the frame before, so a turn takes as long on a 60 Hz display as on a 240 Hz one.
      const dt = this.lastFrame ? Math.min(t - this.lastFrame, 100) : 16
      this.lastFrame = t
      let moving = this.controls.update()
      if (this.turntable && this.model) {
        this.model.rotation.y += TURN_RAD_PER_MS * dt
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
      if (this.bend) {
        const phase = bendPhase(t - this.bend.start)
        if (phase.done) this.endBend()
        else {
          this.pose(phase)
          moving = true
        }
      }
      this.renderer.render(this.scene, this.camera)
      if (moving) this.request()
      else this.lastFrame = 0
    })
  }

  /** True once the browser or the graphics driver took this viewer's WebGL context away. */
  get lost(): boolean {
    return this.renderer.getContext().isContextLost()
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
    if (signal?.aborted || this.disposed) throw new DOMException('aborted', 'AbortError')
    this.setModel(group)
    return this.stats()
  }

  private setModel(group: THREE.Group): void {
    this.lod = 0
    this.showLod(group)
    const keepCamera = this.model !== null
    this.stopBend()
    this.setSkeleton(false)
    if (this.model) {
      this.scene.remove(this.model)
      this.disposeModel(this.model)
    }
    this.original.clear()
    refreshSkinnedBounds(group)
    this.rest = bonesOf(group)
    this.skeleton = skeletonOf([...this.rest.keys()].map((b) => b.name))
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
    this.disposeGrid()
    const cells = Math.max(2, Math.ceil(radius * 2))
    this.grid = new THREE.GridHelper(cells, cells, 0x5b626c, 0x3d434b)
    ;(this.grid.material as THREE.Material).transparent = true
    ;(this.grid.material as THREE.Material).opacity = 0.35
    this.scene.add(this.grid)
    refreshSkinnedBounds(pivot)
    this.sphere = new THREE.Box3().setFromObject(pivot).getBoundingSphere(new THREE.Sphere())
    if (!keepCamera) this.frameCamera()
    this.applyMode()
    // A new model grows in; the same model with other materials replaces the old one in place.
    if (!keepCamera) this.intro = { start: performance.now(), from: 0.94 }
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

  /** Shows one level of detail (0 is the full one) in place of the others. */
  setLod(level: number): void {
    this.lod = level
    if (this.model) this.showLod(this.model)
    this.request()
  }

  private showLod(root: THREE.Object3D): void {
    root.traverse((o) => {
      const named = namedLevel(o)
      // Only what names a level is switched: a mesh under a named group follows its group.
      if (named !== undefined) o.visible = named === this.lod
      else if ((o as THREE.Mesh).isMesh && lodLevel(o) === 0) o.visible = this.lod === 0
    })
  }

  /** Back to the view the model opened in: framed, unturned. */
  resetView(): void {
    if (this.model) this.model.rotation.y = 0
    this.frameCamera()
    this.request()
  }

  /** Turns the camera around the model, for the arrow keys: radians to the right and up. */
  orbitBy(right: number, up: number): void {
    const offset = this.camera.position.clone().sub(this.controls.target)
    const at = new THREE.Spherical().setFromVector3(offset)
    at.theta -= right
    at.phi = THREE.MathUtils.clamp(at.phi - up, 0.05, Math.PI - 0.05)
    this.camera.position.copy(this.controls.target).add(offset.setFromSpherical(at))
    this.controls.update()
    this.request()
  }

  /** Moves the camera towards the model (factor below 1) or away from it. */
  zoomBy(factor: number): void {
    const offset = this.camera.position.clone().sub(this.controls.target)
    const length = THREE.MathUtils.clamp(offset.length() * factor, this.sphere.radius * 0.2, this.sphere.radius * 40)
    this.camera.position.copy(this.controls.target).add(offset.setLength(length))
    this.controls.update()
    this.request()
  }

  /** Draws the bones over the mesh, so they show through it. */
  setSkeleton(on: boolean): void {
    if (this.helper) {
      this.scene.remove(this.helper)
      this.helper.dispose()
      this.helper = null
    }
    if (on && this.model && this.rest.size > 0) {
      const helper = new THREE.SkeletonHelper(this.model)
      helper.setColors(new THREE.Color(0xf6b872), new THREE.Color(0xf0a04b))
      const mat = helper.material as THREE.LineBasicMaterial
      mat.depthTest = false
      mat.depthWrite = false
      mat.transparent = true
      helper.renderOrder = 999
      this.helper = helper
      this.scene.add(helper)
    }
    this.request()
  }

  /**
   * Flexes the limbs for a moment, then restores the rest pose; resolves when it has. With
   * reduced motion it holds one bent pose instead of moving.
   */
  playBend(): Promise<void> {
    this.stopBend()
    if (this.rest.size === 0) return Promise.resolve()
    return new Promise((done) => {
      if (reducedMotion()) {
        this.pose(HELD)
        this.request()
        this.held = setTimeout(() => {
          this.held = undefined
          this.restPose()
          this.request()
          done()
        }, HELD_MS)
        return
      }
      this.bend = { start: performance.now(), done }
      this.request()
    })
  }

  private stopBend(): void {
    if (this.held !== undefined) {
      clearTimeout(this.held)
      this.held = undefined
      this.restPose()
    }
    if (this.bend) this.endBend()
  }

  private endBend(): void {
    const done = this.bend?.done
    this.bend = null
    this.restPose()
    done?.()
  }

  private restPose(): void {
    for (const [bone, q] of this.rest) bone.quaternion.copy(q)
  }

  private pose(phase: Phase): void {
    const euler = new THREE.Euler()
    const delta = new THREE.Quaternion()
    const angles = this.skeleton === 'humanoid' ? humanoidPose(phase) : undefined
    for (const [bone, q] of this.rest) {
      let e: [number, number, number] | undefined
      if (angles) e = angles[bone.name]
      else if (bone.children.some((c) => (c as THREE.Bone).isBone)) {
        let depth = 0
        for (let p = bone.parent; p && (p as THREE.Bone).isBone; p = p.parent) depth++
        e = [genericAngle(phase, depth), 0, 0]
      }
      if (!e) continue
      delta.setFromEuler(euler.set(e[0], e[1], e[2]))
      bone.quaternion.copy(q).multiply(delta)
    }
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
          mesh.material = own(new THREE.MeshBasicMaterial({ color: 0xa8adb5, wireframe: true }))
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
    const lodTriangles: number[] = []
    const materials = new Set<THREE.Material>()
    const textures = new Set<string>()
    const maps = new Map<string, Set<string>>()
    const sizeOf = (t: THREE.Texture | null | undefined): string | undefined => {
      const img = t?.image as { width?: number; height?: number } | undefined
      return img?.width ? `${img.width}×${img.height}` : undefined
    }
    this.model?.traverse((o) => {
      const mesh = o as THREE.Mesh
      if (!mesh.isMesh) return
      const level = lodLevel(o)
      lods = Math.max(lods, level)
      const g = mesh.geometry
      const tris = (g.index ? g.index.count : (g.attributes.position?.count ?? 0)) / 3
      lodTriangles[level] = (lodTriangles[level] ?? 0) + tris
      if (level > 0) return
      vertices += g.attributes.position?.count ?? 0
      triangles += tris
      const mats = this.original.get(mesh) ?? mesh.material
      for (const m of Array.isArray(mats) ? mats : [mats]) {
        materials.add(m)
        for (const [name, tex] of textureMaps(m as THREE.MeshStandardMaterial)) {
          const size = sizeOf(tex)
          if (!size) continue
          if (name === 'Base color') textures.add(size)
          if (!maps.has(name)) maps.set(name, new Set())
          maps.get(name)!.add(size)
        }
      }
    })
    const size = this.model ? new THREE.Box3().setFromObject(this.model).getSize(new THREE.Vector3()) : new THREE.Vector3()
    return {
      triangles: Math.round(triangles),
      vertices,
      materials: materials.size,
      textures: [...textures],
      maps: [...maps].map(([name, sizes]) => ({ name, size: [...sizes].join(', ') })),
      size: [size.x, size.y, size.z],
      lods,
      lodTriangles: Array.from({ length: lods + 1 }, (_, i) => Math.round(lodTriangles[i] ?? 0)),
      bones: this.rest.size,
      skeleton: this.skeleton,
    }
  }

  /**
   * Renders the current model once into a square image for the library grid. It refuses to
   * hand back an empty picture (`BlankFrameError`): a context the browser took away draws
   * nothing, and a blank thumbnail would be kept for good.
   */
  async thumbnail(px = 512): Promise<Blob> {
    if (this.lost) throw new BlankFrameError('the 3D context was lost')
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
    if (this.helper) this.helper.visible = false
    this.renderer.render(this.scene, this.camera)
    // Read back in the same task as the draw, while the drawing buffer still holds it.
    const gl = this.renderer.getContext()
    const pixels = new Uint8Array(px * px * 4)
    gl.readPixels(0, 0, px, px, gl.RGBA, gl.UNSIGNED_BYTE, pixels)
    const blob = isBlank(pixels) ? null : await new Promise<Blob | null>((done) => this.canvas.toBlob(done, 'image/webp', 0.9))
    if (this.grid) this.grid.visible = true
    if (this.helper) this.helper.visible = true
    this.renderer.setPixelRatio(ratio)
    this.renderer.setSize(prev.x, prev.y, false)
    this.camera.aspect = aspect
    this.camera.position.copy(pose.position)
    this.controls.target.copy(pose.target)
    this.camera.updateProjectionMatrix()
    this.request()
    if (!blob) throw new BlankFrameError(this.lost ? 'the 3D context was lost' : 'the picture of this model came out empty')
    return blob
  }

  private disposeModel(root: THREE.Object3D): void {
    root.traverse((o) => {
      const mesh = o as THREE.Mesh
      if (!mesh.isMesh) return
      if ((mesh as THREE.SkinnedMesh).isSkinnedMesh) (mesh as THREE.SkinnedMesh).skeleton.dispose()
      mesh.geometry.dispose()
      const mats = this.original.get(mesh) ?? mesh.material
      for (const m of Array.isArray(mats) ? mats : [mats]) {
        for (const v of Object.values(m)) if (v instanceof THREE.Texture) v.dispose()
        m.dispose()
      }
    })
  }

  private disposeGrid(): void {
    if (!this.grid) return
    this.scene.remove(this.grid)
    this.grid.geometry.dispose()
    ;(this.grid.material as THREE.Material).dispose()
    this.grid = null
  }

  /**
   * Frees everything on the GPU and gives the WebGL context back. Without the last step the
   * context lives until the canvas is garbage collected, and a browser keeps only so many:
   * it then takes the oldest one away from a viewer that is still in use.
   */
  dispose(): void {
    if (this.disposed) return
    this.stopBend()
    this.setSkeleton(false)
    this.disposed = true
    cancelAnimationFrame(this.frame)
    this.frame = 0
    this.resize.disconnect()
    this.controls.dispose()
    if (this.model) {
      this.scene.remove(this.model)
      this.disposeModel(this.model)
      this.model = null
    }
    this.original.clear()
    this.rest.clear()
    this.disposeGrid()
    for (const m of this.modeMaterials) m.dispose()
    this.modeMaterials = []
    this.checker?.dispose()
    this.scene.environment?.dispose()
    this.scene.environment = null
    this.renderer.dispose()
    this.renderer.forceContextLoss()
  }
}
