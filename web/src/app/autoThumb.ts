import { AmbientLight, Box3, DirectionalLight, HemisphereLight, Mesh, PerspectiveCamera, Scene, SRGBColorSpace, Vector3, WebGLRenderer, type Material, type Object3D } from 'three'
import { GLTFLoader } from 'three/addons/loaders/GLTFLoader.js'
import { accelFetch } from '../lib/accel'

// 只有 GLB、没有预览图的零件（R-383）：在浏览器里把模型渲染成缩略图——固定的四分之三视角、透明底、4:3。
// 和 app/viewer3d.tsx 一样只在用到时按需加载（app/assets.tsx 里动态 import），three.js 不进首屏包。
// 全页共用一个离屏渲染器，一件一件排队渲染，不会同时开一堆 WebGL 上下文。光照与 3D 查看一致，顶点色不做颜色空间换算。

const W = 640
const H = 480

let renderer: WebGLRenderer | null = null
let chain: Promise<unknown> = Promise.resolve()

function disposeTree(root: Object3D) {
  root.traverse((o) => {
    if (!(o instanceof Mesh)) return
    o.geometry.dispose()
    const ms: Material[] = Array.isArray(o.material) ? o.material : [o.material]
    for (const m of ms) {
      for (const v of Object.values(m)) if (v && typeof v === 'object' && 'isTexture' in v) (v as { dispose: () => void }).dispose()
      m.dispose()
    }
  })
}

function getRenderer() {
  if (!renderer) {
    renderer = new WebGLRenderer({ antialias: true, alpha: true, preserveDrawingBuffer: true })
    renderer.setPixelRatio(1)
    renderer.setSize(W, H, false)
    renderer.setClearColor(0x000000, 0)
    renderer.outputColorSpace = SRGBColorSpace
  }
  return renderer
}

async function renderOnce(url: string): Promise<Blob> {
  const res = await accelFetch(url) // 加速地址上取不到时回主地址再取一次（R-369）
  if (!res.ok) throw new Error(String(res.status))
  const g = await new GLTFLoader().parseAsync(await res.arrayBuffer(), '')
  const model = g.scene
  try {
    model.traverse((o) => {
      if (!(o instanceof Mesh) || !o.geometry.getAttribute('color')) return
      const ms: Material[] = Array.isArray(o.material) ? o.material : [o.material]
      for (const m of ms) if (!m.vertexColors) (m.vertexColors = true), (m.needsUpdate = true)
    })
    const scene = new Scene()
    scene.add(new HemisphereLight(0xffffff, 0x9a9080, 1.4))
    scene.add(new AmbientLight(0xffffff, 0.25))
    const key = new DirectionalLight(0xffffff, 2.2)
    key.position.set(3, 5, 4)
    scene.add(key)
    const fill = new DirectionalLight(0xfff0dd, 0.8)
    fill.position.set(-4, 2, -3)
    scene.add(fill)

    const box = new Box3().setFromObject(model)
    const c = box.getCenter(new Vector3())
    const s = box.getSize(new Vector3())
    model.position.sub(c)
    scene.add(model)
    // 四分之三视角：从右前上方看；按包围球取景，模型占满画面又不被裁掉
    const cam = new PerspectiveCamera(30, W / H, 0.01, 100)
    const r = s.length() / 2 || 1
    const dist = (r / Math.sin((cam.fov * Math.PI) / 360)) * 1.02
    const dir = new Vector3(0.75, 0.5, 0.9).normalize()
    cam.position.copy(dir.multiplyScalar(dist))
    cam.near = dist / 100
    cam.far = dist * 100
    cam.lookAt(0, 0, 0)
    cam.updateProjectionMatrix()

    const gl = getRenderer()
    gl.render(scene, cam)
    return await new Promise<Blob>((ok, fail) => gl.domElement.toBlob((b) => (b ? ok(b) : fail(new Error('toBlob'))), 'image/png'))
  } finally {
    disposeTree(model)
  }
}

// renderGlbThumb：渲染一件，返回 PNG。排队执行，前一件失败不影响后一件。
export function renderGlbThumb(url: string): Promise<Blob> {
  const job = chain.then(() => renderOnce(url))
  chain = job.catch(() => undefined)
  return job
}
