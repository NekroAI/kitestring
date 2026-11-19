import { useEffect, useRef, useState } from 'react'
import { AmbientLight, Box3, DirectionalLight, HemisphereLight, Mesh, PerspectiveCamera, Scene, SRGBColorSpace, Vector3, WebGLRenderer, type Material, type Object3D } from 'three'
import { GLTFLoader } from 'three/addons/loaders/GLTFLoader.js'
import { OrbitControls } from 'three/addons/controls/OrbitControls.js'
import { Loader2 } from 'lucide-react'
import { t } from '../i18n'

// 资源库的 3D 查看（R-351 第 3 步）：只在抽屉切到「3D」时按需加载（app/assets.tsx 里 lazy import），three.js 不进首屏包。
// 顶点色：glTF 规定 COLOR_0 是线性值，GLTFLoader 原样读入、材质开 vertexColors，渲染输出转 sRGB；这里不再做任何颜色空间换算，
// 否则低多边形零件靠顶点色区分的橄榄绿、枪灰会整体发白或发暗。
// 拖动旋转、滚轮 / 双指缩放；容器标 data-no-drag，手机底部抽屉不会把拖动当成下拉关闭。

type Props = { url: string; className?: string }

const coarse = typeof matchMedia !== 'undefined' && matchMedia('(pointer: coarse)').matches

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

export default function Viewer3D({ url, className }: Props) {
  const host = useRef<HTMLDivElement>(null)
  const [state, setState] = useState<'loading' | 'ok' | 'fail'>('loading')
  const [vc, setVc] = useState('')

  useEffect(() => {
    const el = host.current
    if (!el) return
    setState('loading')
    const renderer = new WebGLRenderer({ antialias: true, alpha: true, preserveDrawingBuffer: true })
    renderer.setPixelRatio(Math.min(window.devicePixelRatio || 1, 2))
    renderer.outputColorSpace = SRGBColorSpace
    renderer.domElement.style.cssText = 'display:block;width:100%;height:100%;touch-action:none'
    el.appendChild(renderer.domElement)

    const scene = new Scene()
    const cam = new PerspectiveCamera(32, 1, 0.01, 100)
    scene.add(new HemisphereLight(0xffffff, 0x9a9080, 1.4))
    scene.add(new AmbientLight(0xffffff, 0.25))
    const key = new DirectionalLight(0xffffff, 2.2)
    key.position.set(3, 5, 4)
    scene.add(key)
    const fill = new DirectionalLight(0xfff0dd, 0.8)
    fill.position.set(-4, 2, -3)
    scene.add(fill)

    const ctl = new OrbitControls(cam, renderer.domElement)
    ctl.enableDamping = true
    ctl.autoRotate = true
    ctl.autoRotateSpeed = 1.6
    ctl.addEventListener('start', () => (ctl.autoRotate = false))

    const size = () => {
      const w = el.clientWidth || 1
      const h = el.clientHeight || 1
      renderer.setSize(w, h, false)
      cam.aspect = w / h
      cam.updateProjectionMatrix()
    }
    size()
    const ro = new ResizeObserver(size)
    ro.observe(el)

    // 看不见时（切到别的视图、页面在后台）不画，省电
    let visible = true
    const io = new IntersectionObserver((es) => (visible = es.some((e) => e.isIntersecting)))
    io.observe(el)
    renderer.setAnimationLoop(() => {
      if (!visible || document.hidden) return
      ctl.update()
      renderer.render(scene, cam)
    })

    let model: Object3D | null = null
    let alive = true
    new GLTFLoader().load(
      url,
      (g) => {
        if (!alive) {
          disposeTree(g.scene)
          return
        }
        model = g.scene
        let meshes = 0
        let colored = 0
        model.traverse((o) => {
          if (!(o instanceof Mesh)) return
          meshes++
          if (o.geometry.getAttribute('color')) {
            colored++
            const ms: Material[] = Array.isArray(o.material) ? o.material : [o.material]
            for (const m of ms) {
              // GLTFLoader 遇到 COLOR_0 已经会打开；个别导出器把材质共享给无顶点色的网格时这里补上
              if (!m.vertexColors) {
                m.vertexColors = true
                m.needsUpdate = true
              }
            }
          }
        })
        setVc(`${colored}/${meshes}`)
        const box = new Box3().setFromObject(model)
        const c = box.getCenter(new Vector3())
        const s = box.getSize(new Vector3())
        const m = Math.max(s.x, s.y, s.z) || 1
        model.position.sub(c)
        scene.add(model)
        const dist = (m / (2 * Math.tan((cam.fov * Math.PI) / 360))) * 1.55
        cam.position.set(dist * 0.75, dist * 0.45, dist * 0.9)
        cam.near = dist / 100
        cam.far = dist * 100
        cam.updateProjectionMatrix()
        ctl.target.set(0, 0, 0)
        ctl.minDistance = dist * 0.2
        ctl.maxDistance = dist * 5
        ctl.update()
        setState('ok')
      },
      undefined,
      () => alive && setState('fail'),
    )

    return () => {
      alive = false
      renderer.setAnimationLoop(null)
      ro.disconnect()
      io.disconnect()
      ctl.dispose()
      if (model) disposeTree(model)
      renderer.dispose()
      renderer.domElement.remove()
    }
  }, [url])

  return (
    <div className={className} data-no-drag="" data-vertex-colors={vc || undefined}>
      <div ref={host} className="absolute inset-0 touch-none" />
      {state !== 'ok' && (
        <div className="pointer-events-none absolute inset-0 flex items-center justify-center gap-2 text-sm text-fg-3">
          {state === 'loading' && <Loader2 className="size-4 animate-spin" />}
          {state === 'loading' ? t.asset.loading3d : t.asset.fail3d}
        </div>
      )}
      {state === 'ok' && <div className="pointer-events-none absolute right-3 bottom-2.5 text-[11px] text-fg-3">{coarse ? t.asset.hint3dTouch : t.asset.hint3d}</div>}
    </div>
  )
}
