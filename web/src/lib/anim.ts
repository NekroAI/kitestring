// 资源库 3D 查看的动作动画（R-392）：动作列表、播放时间、几个并排查看的模型同步播放。
// 这里只有纯逻辑（不引 three.js），播放控件（app/animBar.tsx）和查看器（app/viewer3d.tsx）共用，node --test 可测。
//
// 同步的做法：时间不由某个查看器推进，而是从一个共同的时钟算出来——播放中 t = base + (now - at) × speed，
// 暂停时 t = base。几个查看器（版本对比、候选并排）每帧都按同一个 t 取姿势，不会越播越错开。

/** GLB 里的一个动作（查看器加载后报给同步时钟）。 */
export type ClipInfo = {
  name: string;
  duration: number;
  fps: number;
  loop?: boolean;
};

/** asset.json 里 display.animations 的一项。 */
export type JsonAnim = { name: string; label?: string; loop?: boolean };

/** 动作列表的一行：以 asset.json 为准，和 GLB 对不上的标出来。 */
export type AnimRow = {
  name: string;
  label?: string;
  duration: number; // GLB 里没有时为 0
  fps: number;
  loop?: boolean; // 生效的循环标记：asset.json 写了取它，否则取 GLB 的；都没有时为 undefined
  inGlb: boolean;
  warn?: "missingInGlb" | "notInJson" | "loopDiff";
};

/** 意见挂在哪一帧：动作名、第几秒、模型版本（与后端 store.AssetFrame 一致）。 */
export type Frame = { anim: string; t: number; v: number };

export const SPEEDS = [0.25, 0.5, 1, 1.5, 2] as const;

const isObj = (x: unknown): x is Record<string, unknown> =>
  !!x && typeof x === "object" && !Array.isArray(x);

// jsonAnims：读 asset.json（版本的 meta）里的 display.animations。支持三种写法：
// ["idle", "run"]、[{"name": "run", "loop": true, "label": "跑"}]、{"run": {"loop": true}}；没写时返回 null。
export function jsonAnims(meta: unknown): JsonAnim[] | null {
  const d = isObj(meta) ? meta.display : undefined;
  const raw = isObj(d) ? d.animations : undefined;
  if (raw === undefined || raw === null) return null;
  const one = (name: unknown, x: unknown): JsonAnim | null => {
    const n = typeof name === "string" ? name.trim() : "";
    if (!n) return null;
    const o = isObj(x) ? x : {};
    return {
      name: n,
      label:
        typeof o.label === "string" && o.label.trim()
          ? o.label.trim()
          : undefined,
      loop: typeof o.loop === "boolean" ? o.loop : undefined,
    };
  };
  const out: JsonAnim[] = [];
  if (Array.isArray(raw))
    for (const x of raw) {
      const a =
        typeof x === "string" ? one(x, null) : isObj(x) ? one(x.name, x) : null;
      if (a) out.push(a);
    }
  else if (isObj(raw))
    for (const [k, v] of Object.entries(raw)) {
      const a = one(k, v);
      if (a) out.push(a);
    }
  return out;
}

// glbLoop：GLB 里的循环标记。glTF 没有统一写法：先看动画的 extras.loop，再看名字后缀（Godot 的 -loop / _loop / -cycle 约定）。
export function glbLoop(name: string, extras?: unknown): boolean | undefined {
  if (isObj(extras) && typeof extras.loop === "boolean") return extras.loop;
  if (/[-_](loop|cycle)$/i.test(name)) return true;
  return undefined;
}

// animRows：动作列表。asset.json 写了 display.animations 时按它的顺序列，GLB 里没有的标「GLB 里没有」、
// GLB 多出来的排在后面标「asset.json 没写」，两边循环标记不一致时标出来；没写时按 GLB 的顺序列。
export function animRows(clips: ClipInfo[], meta: unknown): AnimRow[] {
  const json = jsonAnims(meta);
  const byName = new Map(clips.map((c) => [c.name, c]));
  if (!json || !json.length)
    return clips.map((c) => ({
      name: c.name,
      duration: c.duration,
      fps: c.fps,
      loop: c.loop,
      inGlb: true,
    }));
  const rows: AnimRow[] = [];
  const seen = new Set<string>();
  for (const j of json) {
    if (seen.has(j.name)) continue;
    seen.add(j.name);
    const c = byName.get(j.name);
    const loop = j.loop ?? c?.loop;
    const warn = !c
      ? "missingInGlb"
      : j.loop !== undefined && c.loop !== undefined && j.loop !== c.loop
        ? "loopDiff"
        : undefined;
    rows.push({
      name: j.name,
      label: j.label,
      duration: c?.duration ?? 0,
      fps: c?.fps ?? 30,
      loop,
      inGlb: !!c,
      warn,
    });
  }
  for (const c of clips)
    if (!seen.has(c.name))
      rows.push({
        name: c.name,
        duration: c.duration,
        fps: c.fps,
        loop: c.loop,
        inGlb: true,
        warn: "notInJson",
      });
  return rows;
}

// defaultClip：打开时播哪个动作——idle 优先（名字里带 idle 的第一个），否则第一个 GLB 里有的。
export function defaultClip(rows: AnimRow[]): string | null {
  const ok = rows.filter((r) => r.inGlb);
  return (
    (
      ok.find((r) => r.name.toLowerCase() === "idle") ??
      ok.find((r) => /idle/i.test(r.name)) ??
      ok[0]
    )?.name ?? null
  );
}

// inferFps：从关键帧时间推算帧率（相邻关键帧间隔的中位数），推不出来时按 30。
export function inferFps(times: ArrayLike<number>[]): number {
  const gaps: number[] = [];
  for (const ts of times) {
    for (let i = 1; i < ts.length && gaps.length < 400; i++) {
      const g = ts[i] - ts[i - 1];
      if (g > 1e-4) gaps.push(g);
    }
    if (gaps.length >= 400) break;
  }
  if (!gaps.length) return 30;
  gaps.sort((a, b) => a - b);
  const fps = Math.round(1 / gaps[gaps.length >> 1]);
  return fps >= 1 && fps <= 240 ? fps : 30;
}

// wrapTime：某个查看器按自己动作的时长取第几秒。循环时取余，不循环时停在末尾。
export function wrapTime(t: number, dur: number, loop: boolean): number {
  if (!(dur > 0)) return 0;
  if (t < 0) t = 0;
  if (loop) return t % dur;
  return Math.min(t, dur);
}

// stepFrame：逐帧——对齐到帧格再前进 / 后退一帧，不越过 0 和末尾。
export function stepFrame(
  t: number,
  fps: number,
  dir: 1 | -1,
  dur: number,
): number {
  const f = fps > 0 ? fps : 30;
  const i = Math.round(t * f + 1e-6) + dir;
  const n = Math.max(0, i) / f;
  return dur > 0 ? Math.min(n, dur) : n;
}

/** 秒数的写法：保留两位，「0.80」。 */
export const fmtSec = (t: number) =>
  (Number.isFinite(t) ? Math.max(0, t) : 0).toFixed(2);

// ---------- 同步时钟 ----------

export type AnimState = {
  clip: string | null; // null = 静止姿势
  playing: boolean;
  speed: number;
  loop: boolean;
  skeleton: boolean;
  base: number; // 秒
  at: number; // 记下 base 时的时钟（毫秒）
  dur: number; // 当前动作在主查看器里的时长（秒），0 = 还不知道
  fps: number;
  picked: boolean; // 用户（或点意见跳帧）选过动作后，不再自动选默认动作
};

/** 一个查看器报给时钟的东西：它的动作、有没有骨骼、截当前画面。 */
export type ViewerEntry = {
  id: string;
  main?: boolean;
  label?: string;
  clips: ClipInfo[];
  skeleton: boolean;
  canvas: () => HTMLCanvasElement | null;
};

export type AnimSync = ReturnType<typeof createAnimSync>;

export function createAnimSync(clock: () => number = () => performance.now()) {
  let s: AnimState = {
    clip: null,
    playing: false,
    speed: 1,
    loop: true,
    skeleton: false,
    base: 0,
    at: 0,
    dur: 0,
    fps: 30,
    picked: false,
  };
  let viewers: ViewerEntry[] = [];
  let rows: AnimRow[] = [];
  let meta: unknown = null;
  const subs = new Set<() => void>();
  const emit = () => subs.forEach((f) => f());
  const rawAt = (now = clock()) =>
    s.playing ? s.base + ((now - s.at) / 1000) * s.speed : s.base;
  const timeAt = (now = clock()) => wrapTime(rawAt(now), s.dur, s.loop);
  // 改速度、循环、播放状态前先把当前时间记成新的起点，画面不跳
  const rebase = (patch: Partial<AnimState>) => {
    const now = clock();
    const t = s.dur > 0 ? timeAt(now) : rawAt(now);
    s = { ...s, base: t, at: now, ...patch };
    emit();
  };
  const main = () => viewers[0];
  // 动作或查看器变了：重算列表，状态换成新对象让订阅方重渲染
  const recompute = () => {
    s = { ...s };
    rows = animRows(main()?.clips ?? [], meta);
    const r = rows.find((x) => x.name === s.clip && x.inGlb);
    if (r) s = { ...s, dur: r.duration, fps: r.fps };
    if (!s.picked && !s.clip && rows.length) {
      const d = defaultClip(rows);
      const dr = rows.find((x) => x.name === d);
      if (dr)
        s = {
          ...s,
          clip: d,
          dur: dr.duration,
          fps: dr.fps,
          loop: dr.loop ?? true,
          base: 0,
          at: clock(),
          playing: true,
        };
    }
  };
  return {
    get: () => s,
    rows: () => rows,
    viewers: () => viewers,
    subscribe: (f: () => void) => (subs.add(f), () => void subs.delete(f)),
    rawAt,
    timeAt,
    setMeta(m: unknown) {
      meta = m;
      recompute();
      emit();
    },
    register(v: ViewerEntry) {
      // 主查看器（正在看的那一版）排在最前，动作列表和时长按它
      viewers = [...viewers.filter((x) => x.id !== v.id), v].sort(
        (x, y) => Number(!!y.main) - Number(!!x.main),
      );
      recompute();
      emit();
      return () => {
        viewers = viewers.filter((x) => x.id !== v.id);
        recompute();
        emit();
      };
    },
    select(name: string | null, at = 0, play = true) {
      const r = rows.find((x) => x.name === name);
      s = {
        ...s,
        clip: name,
        picked: true,
        dur: r?.duration ?? 0,
        fps: r?.fps ?? 30,
        loop: r?.loop ?? true,
        base: at,
        at: clock(),
        playing: !!name && play,
      };
      emit();
    },
    play() {
      if (!s.clip) return;
      const t = timeAt();
      // 不循环、已经停在末尾时从头播
      rebase({
        playing: true,
        ...(!s.loop && s.dur > 0 && t >= s.dur - 1e-3 ? { base: 0 } : {}),
      });
    },
    pause: () => rebase({ playing: false }),
    toggle() {
      if (s.playing) rebase({ playing: false });
      else this.play();
    },
    seek(t: number) {
      s = {
        ...s,
        base: Math.max(0, s.dur > 0 ? Math.min(t, s.dur) : t),
        at: clock(),
      };
      emit();
    },
    step(dir: 1 | -1) {
      const t = stepFrame(timeAt(), s.fps, dir, s.dur);
      s = { ...s, playing: false, base: t, at: clock() };
      emit();
    },
    setSpeed: (speed: number) => rebase({ speed }),
    setLoop: (loop: boolean) => rebase({ loop }),
    setSkeleton(skeleton: boolean) {
      s = { ...s, skeleton };
      emit();
    },
    // settle：不循环时播到末尾就停（播放控件每帧调用）
    settle() {
      if (s.playing && !s.loop && s.dur > 0 && rawAt() >= s.dur) {
        s = { ...s, playing: false, base: s.dur, at: clock() };
        emit();
      }
    },
    // frame：当前这一帧（挂意见用）；没在看动作时为 null
    frame(v: number): Frame | null {
      if (!s.clip) return null;
      return { anim: s.clip, t: Math.round(timeAt() * 1000) / 1000, v };
    },
  };
}
