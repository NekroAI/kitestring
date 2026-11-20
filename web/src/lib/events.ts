// 3D 查看器的动作事件（R-408）：asset.json 的 display.events 按动作名列出某一时刻要做的事——
// 挂上 / 摘下挂件（attach / detach）、播音效素材（sound）、在挂点闪一个特效标记（mark）。
// 这里只有纯逻辑（不引 three.js），查看器（app/viewer3d.tsx）和播放控件（app/animBar.tsx）共用，node --test 可测。
// 推送端（internal/protocol 的 parseEvents）校验格式；这里读的时候宽松，格式不对的项跳过。
//
// 挂件状态只由时间算出（不记「上一帧挂没挂」），所以逐帧、拖进度、往回拖都对：
//   某一时刻之前最后一个 attach / detach 决定挂没挂；第一个事件之前取它的反面（第一个是 attach，之前就是摘下的）。
//   摘下时挂件放在「决定它摘下的那个事件」那一刻挂点正下方的地面上：之后摘下的取那个 detach 的时刻，开头就摘下的取第一个 attach 的时刻。

export type EventKind = "attach" | "detach" | "sound" | "mark";

/** 一个事件（与后端 protocol.ComposeEvent 一致）。 */
export type AnimEvent = {
  t: number;
  do: EventKind;
  name?: string; // attach / detach：挂件名
  asset?: string; // sound：音效素材 id（可带 @变体名）
  socket?: string; // mark：挂点
  label?: string; // mark：标签
};

/** 动作名 → 事件（按时间排好）。 */
export type ClipEvents = Record<string, AnimEvent[]>;

const KINDS: EventKind[] = ["attach", "detach", "sound", "mark"];

const isObj = (x: unknown): x is Record<string, unknown> =>
  !!x && typeof x === "object" && !Array.isArray(x);
const str = (x: unknown) => (typeof x === "string" ? x.trim() : "");

// jsonEvents：读版本 meta 里的 display.events；没写或格式不对时返回空对象。
export function jsonEvents(meta: unknown): ClipEvents {
  const d = isObj(meta) ? meta.display : undefined;
  const raw = isObj(d) ? d.events : undefined;
  const out: ClipEvents = {};
  if (!isObj(raw)) return out;
  for (const [k, list] of Object.entries(raw)) {
    const clip = k.trim();
    if (!clip || !Array.isArray(list)) continue;
    const evs: AnimEvent[] = [];
    for (const x of list) {
      if (!isObj(x)) continue;
      const t = x.t;
      const kind = x.do as EventKind;
      if (typeof t !== "number" || !Number.isFinite(t) || t < 0) continue;
      if (!KINDS.includes(kind)) continue;
      const e: AnimEvent = { t, do: kind };
      if (kind === "attach" || kind === "detach") {
        if (!(e.name = str(x.name))) continue;
      } else if (kind === "sound") {
        if (!(e.asset = str(x.asset))) continue;
      } else {
        if (!(e.socket = str(x.socket))) continue;
        if (str(x.label)) e.label = str(x.label);
      }
      evs.push(e);
    }
    if (evs.length) out[clip] = evs.sort((a, b) => a.t - b.t);
  }
  return out;
}

/** 一版素材的动作事件（素材详情的版本带 meta）。 */
export const eventsOf = (v?: { meta?: unknown } | null) => jsonEvents(v?.meta);

/** 挂件在某一时刻的状态：on 挂着；没挂时 at 是决定它位置的那个事件的时刻（放在那一刻挂点正下方的地面上）。 */
export type AttachPhase = { on: boolean; at: number };

// attachPhase：动作里写了这个挂件的 attach / detach 时，算它在 t 秒的状态；没写时返回 null（照 clips 的规则显示）。
export function attachPhase(
  evs: AnimEvent[] | undefined,
  name: string,
  t: number,
): AttachPhase | null {
  const mine = (evs ?? []).filter(
    (e) => (e.do === "attach" || e.do === "detach") && e.name === name,
  );
  if (!mine.length) return null;
  let last: AnimEvent | null = null;
  for (const e of mine) if (e.t <= t + 1e-6) last = e;
  if (last) return { on: last.do === "attach", at: last.t };
  const first = mine[0];
  return { on: first.do === "detach", at: first.t };
}

// crossed：从 a 秒播到 b 秒（都是没取余的播放时间）经过了哪些事件，按经过的先后。
// 循环时每一圈都算；不循环时超过时长的不算。a 到 b 不是向前的一小段（拖进度、切动作、跳帧）时不算，避免一下子补放一串。
export function crossed(
  evs: AnimEvent[] | undefined,
  a: number,
  b: number,
  dur: number,
  loop: boolean,
  maxGap = 0.5,
): AnimEvent[] {
  if (!evs?.length || !(b > a) || b - a > maxGap || !(dur > 0)) return [];
  const out: AnimEvent[] = [];
  if (!loop) {
    for (const e of evs) if (e.t > a && e.t <= Math.min(b, dur)) out.push(e);
    return out;
  }
  const k0 = Math.floor(a / dur);
  const k1 = Math.floor(b / dur);
  for (let k = k0; k <= k1; k++)
    for (const e of evs) {
      if (e.t > dur + 1e-6) continue;
      const at = k * dur + e.t;
      if (at > a && at <= b) out.push(e);
    }
  return out;
}

/** 特效标记闪多久（动作里的秒数）。 */
export const FLASH = 0.5;

// flashes：t 秒时正在闪的特效标记，k 是闪了多久（0～1）。按动作时间算，暂停、逐帧停在那几帧也看得到。
export function flashes(
  evs: AnimEvent[] | undefined,
  t: number,
  win = FLASH,
): { e: AnimEvent; i: number; k: number }[] {
  const out: { e: AnimEvent; i: number; k: number }[] = [];
  (evs ?? []).forEach((e, i) => {
    if (e.do !== "mark") return;
    const d = t - e.t;
    if (d >= -1e-6 && d < win) out.push({ e, i, k: Math.max(0, d) / win });
  });
  return out;
}

/** 进度条上的一个事件点：同一时刻的事件合成一个点。 */
export type EventPoint = { t: number; evs: AnimEvent[] };

export function eventPoints(
  evs: AnimEvent[] | undefined,
  dur: number,
): EventPoint[] {
  const out: EventPoint[] = [];
  for (const e of evs ?? []) {
    if (dur > 0 && e.t > dur + 1e-6) continue; // 超出时长的不画（查看器里另有提示）
    const last = out[out.length - 1];
    if (last && Math.abs(last.t - e.t) < 1e-3) last.evs.push(e);
    else out.push({ t: e.t, evs: [e] });
  }
  return out;
}

/** 有没有音效事件（决定要不要给声音开关）。 */
export const hasSound = (ev: ClipEvents) =>
  Object.values(ev).some((l) => l.some((e) => e.do === "sound"));

/** 查看器里给的事件提示（对不上只提示、不报错）。 */
export type EventNote =
  | { kind: "evClip"; clip: string }
  | { kind: "evTime"; clip: string; t: number; dur: number }
  | { kind: "evAttach"; clip: string; name: string }
  | { kind: "evSocket"; clip: string; socket: string };

// eventNotes：动作在模型里没有、时间超出时长、挂件名在配置里没有、特效标记的挂点在模型里没有。
// 音效素材找不到由组合预览的提示（composeNotes）给，不在这里重复。
export function eventNotes(
  ev: ClipEvents,
  clips: { name: string; duration: number }[],
  attachNames: ReadonlySet<string>,
  sockets: ReadonlySet<string>,
): EventNote[] {
  const out: EventNote[] = [];
  const seen = new Set<string>();
  const push = (n: EventNote, key: string) => {
    if (seen.has(key)) return;
    seen.add(key);
    out.push(n);
  };
  for (const [clip, evs] of Object.entries(ev)) {
    const c = clips.find((x) => x.name === clip);
    if (!c) {
      push({ kind: "evClip", clip }, "c:" + clip);
      continue;
    }
    for (const e of evs) {
      if (c.duration > 0 && e.t > c.duration + 1e-3)
        push(
          { kind: "evTime", clip, t: e.t, dur: c.duration },
          `t:${clip}:${e.t}`,
        );
      if ((e.do === "attach" || e.do === "detach") && !attachNames.has(e.name!))
        push({ kind: "evAttach", clip, name: e.name! }, `a:${clip}:${e.name}`);
      if (e.do === "mark" && !sockets.has(e.socket!))
        push(
          { kind: "evSocket", clip, socket: e.socket! },
          `s:${clip}:${e.socket}`,
        );
    }
  }
  return out;
}

// ---------- 声音开关（默认静音，本机记住） ----------

export const SOUND_KEY = "ks.anim.sound";

/** 存的值 → 开没开声音：只认 "on"，其余（没存、拿不到）都是静音。 */
export const parseSound = (v: string | null | undefined) => v === "on";
