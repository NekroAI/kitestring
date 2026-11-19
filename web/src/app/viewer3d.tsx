import { useEffect, useRef, useState } from 'react'
import { AmbientLight, AnimationMixer, TOUCH, Box3, DirectionalLight, HemisphereLight, Mesh, PerspectiveCamera, Scene, SkeletonHelper, SkinnedMesh, SRGBColorSpace, Vector3, WebGLRenderer, type AnimationAction, type AnimationClip, type Material, type Object3D } from 'three'
import { accel } from '../lib/accel'
import { GLTFLoader } from 'three/addons/loaders/GLTFLoader.js'
import { OrbitControls } from 'three/addons/controls/OrbitControls.js'
import { Loader2 } from 'lucide-react'
import { t } from '../i18n'
import { glbLoop, inferFps, wrapTime, type AnimSync, type ClipInfo } from '../lib/anim'
import { isDoubleTap, KIND_GAP, swipeAsDrag, tapOf, wheel3d, WheelKind, type Tap } from '../lib/wheelKind'
import { scrollerOf } from '../ui/gesture'
import { cx } from '../lib/util'

// 资源库的 3D 查看（R-351 第 3 步）：只在抽屉切到「3D」时按需加载（app/assets.tsx 里 lazy import），three.js 不进首屏包。
// 顶点色：glTF 规定 COLOR_0 是线性值，GLTFLoader 原样读入、材质开 vertexColors，渲染输出转 sRGB；这里不再做任何颜色空间换算，
// 否则低多边形零件靠顶点色区分的橄榄绿、枪灰会整体发白或发暗。
// 手势见下面「手势」一节（R-378）；容器标 data-no-drag，手机底部抽屉不会把拖动当成下拉关闭。
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
// R-378：
// - 鼠标：拖动旋转、滚轮缩放，和原来一样（滚轮仍交给 OrbitControls 的缩放算法）。
// - 触控板：捏合（ctrl+wheel；Safari 是 gesturechange）缩放；双指滑动转模型。嵌在能滚动的抽屉 / 页面里时，
//   点一下模型才接管双指滑动（外框高亮），点外面或按 Esc 放开；没接管时双指滑动照常滚页面。审片舞台不滚动，直接接管。
// - 触屏：一指旋转、两指捏合缩放并平移（OrbitControls 的 touches，与 lib/wheelKind 的 touchOp 一致）、双击复位。
// 判别逻辑（滚轮 / 触控板 / 捏合、该转还是放行、双击）都在 lib/wheelKind.ts，单测覆盖。

// OrbitControls（three 0.170）的内部方法：借用它的旋转、缩放累加，阻尼和范围限制与拖动一致。升级 three 时核对这几个名字。
type OrbitInternals = {
  state: number
  _onMouseWheel: (e: WheelEvent) => void
  _rotateLeft: (angle: number) => void
  _rotateUp: (angle: number) => void
  _dollyIn: (scale: number) => void
  _handleMouseWheel: (e: { clientX: number; clientY: number; deltaY: number }) => void
  _customWheelEvent: (e: WheelEvent) => { clientX: number; clientY: number; deltaY: number }
}

// embeddedIn：模型所在的地方能不能上下滚动（抽屉、页面）。能滚就先不接管触控板双指滑动。
function embeddedIn(el: HTMLElement) {
  if (scrollerOf(el.parentElement)) return true
  const doc = document.scrollingElement
  return !!doc && doc.scrollHeight > window.innerHeight + 1 && getComputedStyle(document.body).overflowY !== 'hidden'
}

type Hooks = {
  engaged: (on: boolean) => void // 嵌入时被点开接管（画外框）
  passed: () => void // 没接管时一次双指滑动放给了页面（提示「点一下」）
  reset: () => void // 双击复位
}

function setupControls(cam: PerspectiveCamera, el: HTMLElement, hooks: Hooks) {
  const ctl = new OrbitControls(cam, el)
  ctl.enableDamping = true
  ctl.autoRotate = true
  ctl.autoRotateSpeed = 1.6
  ctl.touches = { ONE: TOUCH.ROTATE, TWO: TOUCH.DOLLY_PAN } // 与 touchOp 一致：一指旋转、两指缩放并平移
  ctl.addEventListener('start', () => (ctl.autoRotate = false))
  const inner = ctl as unknown as OrbitInternals
  el.removeEventListener('wheel', inner._onMouseWheel) // 滚轮改由下面统一分流

  const kinds = new WheelKind()
  let active = false
  let shown = false
  let embedded = false
  let lastWheel = -Infinity
  let lastPinch = -Infinity
  const engage = (on: boolean) => {
    active = on
    const s = on && embeddedIn(el)
    if (s !== shown) hooks.engaged((shown = s))
  }

  const onWheel = (e: WheelEvent) => {
    const mode = e.deltaMode // 先读 deltaMode（Firefox 只在这样时才按行报鼠标滚轮）
    const unit = mode === 1 ? 16 : mode === 2 ? 400 : 1
    const dx = e.deltaX * unit
    const dy = e.deltaY * unit
    const wdy = (e as WheelEvent & { wheelDeltaY?: number }).wheelDeltaY
    if (e.timeStamp - lastWheel > KIND_GAP) embedded = embeddedIn(el) // 每段开头看一次，一段之内不变
    lastWheel = e.timeStamp
    const kind = kinds.feed({ dx, dy, mode, ctrl: e.ctrlKey, shift: e.shiftKey, wdy, t: e.timeStamp })
    const act = wheel3d(kind, { active, embedded })
    if (act === 'pass') return hooks.passed()
    if (inner.state !== -1) return // 正在拖动
    e.preventDefault() // 也让抽屉的横滑切换、全站横滑守卫跳过这一下
    ctl.autoRotate = false
    if (act === 'zoom') {
      if (kind === 'pinch') lastPinch = e.timeStamp
      inner._handleMouseWheel(inner._customWheelEvent(e))
      return
    }
    const d = swipeAsDrag(dx, dy)
    const h = el.clientHeight || 1
    inner._rotateLeft(((2 * Math.PI * d.x) / h) * ctl.rotateSpeed)
    inner._rotateUp(((2 * Math.PI * d.y) / h) * ctl.rotateSpeed)
  }

  // Safari 的触控板捏合：gesturechange 的 scale 是相对起手的倍数。iOS 上两指触屏也会发它，那时交给 OrbitControls 的触屏缩放。
  const touches = new Set<number>()
  let gPrev = 1
  const onGStart = (e: Event) => {
    e.preventDefault()
    gPrev = 1
  }
  const onGChange = (e: Event) => {
    e.preventDefault()
    const sc = (e as Event & { scale: number }).scale
    if (touches.size || !(sc > 0) || e.timeStamp - lastPinch < 200) {
      gPrev = sc || 1
      return
    }
    ctl.autoRotate = false
    inner._dollyIn(gPrev / sc)
    gPrev = sc
  }

  // 双击复位（触屏）：只认单指轻点；中间有过两指就重新算
  let downs = new Map<number, Tap>()
  let multi = false
  let lastTap: Tap | null = null
  const onDown = (e: PointerEvent) => {
    if (e.pointerType !== 'touch') return engage(true) // 接管只关乎触控板；触屏不画外框
    touches.add(e.pointerId)
    downs.set(e.pointerId, { t: e.timeStamp, x: e.clientX, y: e.clientY })
    if (touches.size > 1) multi = true
  }
  const onUp = (e: PointerEvent) => {
    if (e.pointerType !== 'touch') return
    touches.delete(e.pointerId)
    const d = downs.get(e.pointerId)
    downs.delete(e.pointerId)
    const wasMulti = multi
    if (!touches.size) multi = false
    if (!d || wasMulti || e.type === 'pointercancel') {
      lastTap = null
      return
    }
    const tap = tapOf(d, { t: e.timeStamp, x: e.clientX, y: e.clientY })
    if (tap && isDoubleTap(lastTap, tap)) {
      lastTap = null
      hooks.reset()
    } else lastTap = tap
  }
  const onOutside = (e: PointerEvent) => {
    if (active && !el.contains(e.target as Node)) engage(false)
  }
  const onKey = (e: KeyboardEvent) => {
    if (e.key !== 'Escape' || !shown) return
    e.stopPropagation() // 第一下 Esc 只放开模型，不关抽屉
    e.preventDefault()
    engage(false)
  }

  el.addEventListener('wheel', onWheel, { passive: false })
  el.addEventListener('gesturestart', onGStart)
  el.addEventListener('gesturechange', onGChange)
  el.addEventListener('pointerdown', onDown, true)
  el.addEventListener('pointerup', onUp, true)
  el.addEventListener('pointercancel', onUp, true)
  document.addEventListener('pointerdown', onOutside, true)
  window.addEventListener('keydown', onKey, true)
  const dispose = () => {
    el.removeEventListener('wheel', onWheel)
    el.removeEventListener('gesturestart', onGStart)
    el.removeEventListener('gesturechange', onGChange)
    el.removeEventListener('pointerdown', onDown, true)
    el.removeEventListener('pointerup', onUp, true)
    el.removeEventListener('pointercancel', onUp, true)
    document.removeEventListener('pointerdown', onOutside, true)
    window.removeEventListener('keydown', onKey, true)
    downs = new Map()
    ctl.dispose()
  }
  return { ctl, dispose }
}

// 复位动画：从当前机位、观察点用 ease-out 插值回到加载时的取景
type Home = { pos: Vector3; target: Vector3 }
type Fly = { from: Home; to: Home; t0: number }
const FLY_MS = 320

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
  const [engaged, setEngaged] = useState(false) // 嵌入时点开接管了触控板双指滑动
  const [nudge, setNudge] = useState(false) // 没接管时双指滑动放给了页面：提示点一下
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

    let home: Home | null = null
    let fly: Fly | null = null
    let nudgeTimer = 0
    const { ctl, dispose: disposeControls } = setupControls(cam, renderer.domElement, {
      engaged: setEngaged,
      passed: () => {
        setNudge(true)
        window.clearTimeout(nudgeTimer)
        nudgeTimer = window.setTimeout(() => setNudge(false), 2400)
      },
      reset: () => {
        if (!home) return
        ctl.autoRotate = false
        fly = { from: { pos: cam.position.clone(), target: ctl.target.clone() }, to: home, t0: performance.now() }
      },
    })

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
      if (fly) {
        const k = Math.min(1, (performance.now() - fly.t0) / FLY_MS)
        const e = 1 - (1 - k) ** 3
        cam.position.lerpVectors(fly.from.pos, fly.to.pos, e)
        ctl.target.lerpVectors(fly.from.target, fly.to.target, e)
        if (k === 1) fly = null
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
        home = { pos: cam.position.clone(), target: ctl.target.clone() }
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
      window.clearTimeout(nudgeTimer)
      disposeControls()
      if (model) disposeTree(model)
      renderer.dispose()
      renderer.domElement.remove()
    }
  }, [url, label, main])

  return (
    <div className={className} data-no-drag="" data-vertex-colors={vc || undefined} data-engaged={engaged || undefined}>
      <div ref={host} className="absolute inset-0 touch-none" />
      {state !== 'ok' && (
        <div className="pointer-events-none absolute inset-0 flex items-center justify-center gap-2 text-sm text-fg-3">
          {state === 'loading' && <Loader2 className="size-4 animate-spin" />}
          {state === 'loading' ? t.asset.loading3d : t.asset.fail3d}
        </div>
      )}
      {engaged && <div className="pointer-events-none absolute inset-0 rounded-[inherit] ring-2 ring-accent/70 ring-inset" />}
      {state === 'ok' && (
        <div className={cx('pointer-events-none absolute right-3 bottom-2.5 text-[11px]', nudge && !engaged ? 'text-fg-2' : 'text-fg-3')} data-hint3d="">
          {coarse ? t.asset.hint3dTouch : nudge && !engaged ? t.asset.hint3dNudge : t.asset.hint3d}
        </div>
      )}
      {label && <span className="pointer-events-none absolute top-2 left-2 rounded bg-black/55 px-1.5 py-0.5 font-mono text-[11px] text-white">{label}</span>}
      {state === 'ok' && missing && (
        <div className="pointer-events-none absolute inset-x-3 top-9 text-center text-[12px] text-wait" data-anim-missing="">
          {t.asset.anim.missingHere(missing)}
        </div>
      )}
    </div>
  )
}
