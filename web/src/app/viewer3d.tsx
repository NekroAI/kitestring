import { useEffect, useRef, useState } from 'react'
import { AmbientLight, AnimationMixer, Box3, DirectionalLight, HemisphereLight, Mesh, PerspectiveCamera, Scene, SkeletonHelper, SkinnedMesh, SRGBColorSpace, Vector3, WebGLRenderer, type AnimationAction, type AnimationClip, type Material, type Object3D } from 'three'
import { accel } from '../lib/accel'
import { GLTFLoader } from 'three/addons/loaders/GLTFLoader.js'
import { OrbitControls } from 'three/addons/controls/OrbitControls.js'
import { Loader2 } from 'lucide-react'
import { t } from '../i18n'
import { glbLoop, inferFps, wrapTime, type AnimSync, type ClipInfo } from '../lib/anim'

// 资源库的 3D 查看（R-351 第 3 步）：只在抽屉切到「3D」时按需加载（app/assets.tsx 里 lazy import），three.js 不进首屏包。
// 顶点色：glTF 规定 COLOR_0 是线性值，GLTFLoader 原样读入、材质开 vertexColors，渲染输出转 sRGB；这里不再做任何颜色空间换算，
// 否则低多边形零件靠顶点色区分的橄榄绿、枪灰会整体发白或发暗。
// 拖动旋转、滚轮 / 双指缩放；容器标 data-no-drag，手机底部抽屉不会把拖动当成下拉关闭。
//
// 动作动画（R-392）：传了 sync（lib/anim.ts 的同步时钟）时，加载后把 GLB 里的动作报给它，每帧按它算出的时间取姿势；
// 播放控件（app/animBar.tsx）和别的并排查看器共用同一个时钟，所以版本对比、候选并排时两边播的是同一动作的同一时刻。
// 本文件分三块：场景与取景、手势（setupControls，R-378 在这里接触控板 / 触屏手势）、动画（Anim）。动画不碰手势。

type Props = {
  url: string
  className?: string
  sync?: AnimSync // 共用的动画时钟；不传时只显示静止模型
  label?: string // 并排时角上标的名字（如 v2）
  main?: boolean // 并排时正在看的那一版：动作列表、时长、截图顺序以它为准
}

// ---------- 手势 ----------

function setupControls(cam: PerspectiveCamera, el: HTMLElement) {
  const ctl = new OrbitControls(cam, el)
  ctl.enableDamping = true
  ctl.autoRotate = true
  ctl.autoRotateSpeed = 1.6
  ctl.addEventListener('start', () => (ctl.autoRotate = false))
  return ctl
}

// ---------- 动画 ----------

// Anim：一个模型的动作。按时钟给的时间摆姿势：选中的动作设好时间后 mixer.update(0)；没选动作时回到静止姿势（绑定姿势）。
class Anim {
  mixer: AnimationMixer
  clips: AnimationClip[]
  infos: ClipInfo[]
  helper: SkeletonHelper | null = null
  cur: { name: string | null; action: AnimationAction | null } = { name: null, action: null }
  constructor(
    private model: Object3D,
    clips: AnimationClip[],
    extras: unknown[],
  ) {
    this.mixer = new AnimationMixer(model)
    this.clips = clips
    this.infos = clips.map((c, i) => ({ name: c.name, duration: c.duration, fps: inferFps(c.tracks.map((k) => k.times)), loop: glbLoop(c.name, extras[i]) }))
    let bones = false
    model.traverse((o) => {
      if (o instanceof SkinnedMesh) bones = true
    })
    if (bones) {
      this.helper = new SkeletonHelper(model)
      this.helper.visible = false
    }
  }
  missing(name: string | null) {
    return !!name && !this.clips.some((c) => c.name === name)
  }
  apply(sync: AnimSync) {
    const s = sync.get()
    if (this.helper) this.helper.visible = s.skeleton
    if (s.clip !== this.cur.name) {
      this.mixer.stopAllAction()
      const clip = this.clips.find((c) => c.name === s.clip) ?? null
      const action = clip ? this.mixer.clipAction(clip) : null
      action?.play()
      this.cur = { name: s.clip, action }
      if (!action) this.rest()
    }
    const a = this.cur.action
    if (!a) return
    const dur = a.getClip().duration
    // LoopRepeat 下时间等于时长会绕回 0；不循环停在末尾时往回让一点
    a.time = Math.min(wrapTime(sync.rawAt(), dur, s.loop), Math.max(0, dur - 1e-4))
    this.mixer.update(0)
  }
  rest() {
    this.model.traverse((o) => {
      if (o instanceof SkinnedMesh) o.skeleton.pose()
    })
  }
  dispose() {
    this.mixer.stopAllAction()
    this.mixer.uncacheRoot(this.model)
    this.helper?.dispose()
  }
}

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

export default function Viewer3D({ url, className, sync, label, main }: Props) {
  const host = useRef<HTMLDivElement>(null)
  const [state, setState] = useState<'loading' | 'ok' | 'fail'>('loading')
  const [vc, setVc] = useState('')
  const [missing, setMissing] = useState('') // 并排时这一边的 GLB 里没有当前动作
  const syncRef = useRef(sync)
  syncRef.current = sync

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

    const ctl = setupControls(cam, renderer.domElement)

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
    let anim: Anim | null = null
    let unregister: (() => void) | null = null
    let miss = ''
    renderer.setAnimationLoop(() => {
      if (!visible || document.hidden) return
      const sy = syncRef.current
      if (anim && sy) {
        anim.apply(sy)
        const m = anim.missing(sy.get().clip) ? (sy.get().clip ?? '') : ''
        if (m !== miss) setMissing((miss = m))
      }
      ctl.update()
      renderer.render(scene, cam)
    })

    let model: Object3D | null = null
    let alive = true
    // 加速地址上取不到时（R-369）换回主地址再取一次
    const load = (u: string, retry: boolean) => new GLTFLoader().load(
      u,
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
        // 动作动画（R-392）：GLB 里有动作、或有骨骼时报给时钟；循环标记读动画的 extras（GLTFLoader 不放进 clip）
        const sy = syncRef.current
        if (sy) {
          const extras = ((g.parser.json as { animations?: { extras?: unknown }[] }).animations ?? []).map((x) => x.extras)
          anim = new Anim(model, g.animations, extras)
          if (anim.helper) scene.add(anim.helper)
          if (anim.clips.length || anim.helper) unregister = sy.register({ id: url, main, label, clips: anim.infos, skeleton: !!anim.helper, canvas: () => renderer.domElement })
        }
        setState('ok')
      },
      undefined,
      () => {
        const main = retry ? accel.failed(u) : null
        if (alive && main) load(main, false)
        else if (alive) setState('fail')
      },
    )
    load(url, true)

    return () => {
      alive = false
      renderer.setAnimationLoop(null)
      unregister?.()
      anim?.dispose()
      ro.disconnect()
      io.disconnect()
      ctl.dispose()
      if (model) disposeTree(model)
      renderer.dispose()
      renderer.domElement.remove()
    }
  }, [url, label, main])

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
      {label && <span className="pointer-events-none absolute top-2 left-2 rounded bg-black/55 px-1.5 py-0.5 font-mono text-[11px] text-white">{label}</span>}
      {state === 'ok' && missing && (
        <div className="pointer-events-none absolute inset-x-3 top-9 text-center text-[12px] text-wait" data-anim-missing="">
          {t.asset.anim.missingHere(missing)}
        </div>
      )}
    </div>
  )
}
