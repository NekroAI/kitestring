import {
  createContext,
  useContext,
  useEffect,
  useRef,
  useSyncExternalStore,
} from "react";
import {
  Bone,
  Box as BoxIcon,
  ChevronDown,
  Crosshair,
  Ruler,
  MapPin,
  Pause,
  Play,
  Repeat,
  StepBack,
  StepForward,
  X,
} from "lucide-react";
import { t } from "../i18n";
import { cx } from "../lib/util";
import { SPEEDS, type AnimSync, type Frame } from "../lib/anim";
import { hiddenNodes, socketRows } from "../lib/compose";
import { Button, Tip } from "../ui/base";
import { Menu } from "../ui/overlay";

// 动作动画的播放控件（R-392）：选动作（名称、时长、循环标记）、播放 / 暂停、逐帧、拖进度、0.25～2 倍速、循环开关、显示骨骼，
// 以及「标记此帧」——把当前动作的这一帧和当前画面交给底栏的意见框（站内是审核意见，分享页是访客留言）。
// 控件只操作 lib/anim.ts 的同步时钟，不碰 three.js 和手势；几个并排的查看器共用一个时钟，所以对比时两边同步。

export const useAnimState = (sync: AnimSync) =>
  useSyncExternalStore(sync.subscribe, sync.get);

// FrameCtx：素材抽屉里共享的动画时钟和「挂在某一帧的意见」。底栏（意见框）登记 sink 接收标记的帧和截图；
// 审核记录、访客留言里的帧点一下调 jump 跳过去（必要时先切到那一版、切到 3D）。
export type FrameMark = { frame: Frame; shot: Blob };
export type FrameCtxValue = {
  sync: AnimSync;
  version: number;
  jump: (f: Frame) => void;
  sink: ((m: FrameMark) => void) | null;
  setSink: (f: ((m: FrameMark) => void) | null) => void;
};
export const FrameCtx = createContext<FrameCtxValue | null>(null);
export const useFrameCtx = () => useContext(FrameCtx);

// useFrameSink：意见框登记接收标记的帧；卸载时撤销。
export function useFrameSink(on: ((m: FrameMark) => void) | null) {
  const ctx = useFrameCtx();
  const ref = useRef(on);
  ref.current = on;
  const enabled = !!on;
  const setSink = ctx?.setSink;
  useEffect(() => {
    if (!setSink || !enabled) return;
    setSink((m) => ref.current?.(m));
    return () => setSink(null);
  }, [setSink, enabled]);
}

// snapshot：把几个查看器的当前画面并排拼成一张（对比时两边都在），白底 JPEG。
export async function snapshot(sync: AnimSync): Promise<Blob> {
  const cs = sync
    .viewers()
    .map((v) => v.canvas())
    .filter((c): c is HTMLCanvasElement => !!c && c.width > 0 && c.height > 0);
  if (!cs.length) throw new Error("no canvas");
  const h = Math.max(...cs.map((c) => c.height));
  const w = cs.reduce((n, c) => n + Math.round((c.width * h) / c.height), 0);
  const out = document.createElement("canvas");
  out.width = w;
  out.height = h;
  const g = out.getContext("2d")!;
  g.fillStyle = "#f4f2ee";
  g.fillRect(0, 0, w, h);
  let x = 0;
  for (const c of cs) {
    const cw = Math.round((c.width * h) / c.height);
    g.drawImage(c, x, 0, cw, h);
    x += cw;
  }
  return await new Promise<Blob>((ok, fail) =>
    out.toBlob(
      (b) => (b ? ok(b) : fail(new Error("toBlob"))),
      "image/jpeg",
      0.9,
    ),
  );
}

export function AnimBar({
  sync,
  className,
}: {
  sync: AnimSync;
  className?: string;
}) {
  const s = useAnimState(sync);
  const ctx = useFrameCtx();
  const rows = sync.rows();
  const skeleton = sync.viewers().some((v) => v.skeleton);
  // 组合预览（R-407）：挂点列表按正在看的那一版；挂件、参照开关几个并排的查看器共用
  const mainV = sync.viewers()[0];
  const nodes = mainV?.nodes ?? [];
  const items = mainV?.compose ?? [];
  const sockets = socketRows(nodes, items, s.allNodes);
  const more = hiddenNodes(nodes, items);
  const hasAttach = sync
    .viewers()
    .some((v) => v.compose?.some((x) => x.kind === "attach" && x.file));
  const hasRefs = sync
    .viewers()
    .some((v) => v.compose?.some((x) => x.kind === "ref" && x.file));
  // 没有挂点、也没有骨骼的静态模型不给挂点菜单（只有装模型的容器节点，列出来没有意义）
  const showSockets = sockets.length > 0 || (more > 0 && skeleton);
  const composeUi = showSockets || hasAttach || hasRefs;
  const range = useRef<HTMLInputElement>(null);
  const time = useRef<HTMLSpanElement>(null);
  const resume = useRef(false);

  // 每帧刷新进度条和时间（直接改 DOM，不重渲染整条）；不循环播到末尾时停住
  useEffect(() => {
    let raf = 0;
    const draw = () => {
      sync.settle();
      const cur = sync.timeAt();
      if (range.current && document.activeElement !== range.current)
        range.current.value = String(cur);
      if (time.current)
        time.current.textContent = t.asset.anim.time(cur, sync.get().dur);
      if (sync.get().playing) raf = requestAnimationFrame(draw);
    };
    draw();
    return () => cancelAnimationFrame(raf);
  }, [sync, s]);

  if (!rows.length && !skeleton && !composeUi) return null;
  const A = t.asset.anim;
  const C = t.asset.compose;
  const cur = rows.find((r) => r.name === s.clip);
  const warns = rows.filter((r) => r.warn).length;
  const warnText = (r: (typeof rows)[number]) =>
    r.warn === "loopDiff"
      ? A.warn.loopDiff(!r.loop)
      : r.warn
        ? A.warn[r.warn]
        : undefined;
  const has = !!cur && cur.inGlb;

  const mark = async () => {
    const f = sync.frame(ctx?.version ?? 0);
    if (!f || !ctx?.sink) return;
    sync.pause();
    // 等一帧，让画面停在标记的时刻再截
    await new Promise((r) =>
      requestAnimationFrame(() => requestAnimationFrame(r)),
    );
    try {
      ctx.sink({
        frame: { ...f, t: Math.round(sync.timeAt() * 1000) / 1000 },
        shot: await snapshot(sync),
      });
    } catch {
      const { toast } = await import("sonner");
      toast.error(A.markFail);
    }
  };

  // 排布（R-407 返工）：按用途分行，每行都不换行——
  //   动作行：选动作；右侧速度、循环、标记此帧
  //   进度行：上一帧、播放、下一帧、进度条、时间
  //   显示行（细线隔开）：挂点、挂件、参照、骨骼，一律图标加文字
  // 控件统一高 h-8（触屏 h-9），开关都用 ghost、打开时 accent-soft；窄屏只截断动作名和挂点名，不换行、不横向滚动
  // （这两个下拉按钮用 style 覆盖 Button 自带的 shrink-0，才截断得了）。
  const ib = "h-8 w-8 rounded-lg"; // 图标按钮和文字按钮一样高
  const on = (v: boolean) => v && "bg-accent-soft text-accent";
  const viewRow = showSockets || hasAttach || hasRefs || skeleton;

  return (
    <div
      className={cx(
        "rounded-xl border border-line bg-surface px-2.5 py-2",
        className,
      )}
      data-anim-bar=""
      data-clip={s.clip ?? ""}
      data-playing={s.playing || undefined}
    >
      {rows.length > 0 && (
        <div className="flex items-center gap-1" data-anim-row="clip">
          <Menu
            align="start"
            width="w-[min(340px,calc(100vw-32px))]"
            trigger={
              <Button
                size="sm"
                className="max-w-[260px] min-w-0"
                style={{ flexShrink: 1 }}
                aria-label={A.label}
                data-anim-pick=""
              >
                <span className="min-w-0 truncate">
                  {cur ? cur.label || cur.name : A.none}
                </span>
                {/* 能播时进度行已写总时长，这里不再重复（同一信息只写一处） */}
                {cur && !has && (
                  <span className="shrink-0 text-fg-3 tabular">
                    {A.sec(cur.duration)}
                  </span>
                )}
                {cur?.loop && (
                  <Repeat
                    className="size-3.5 shrink-0 text-fg-3"
                    aria-label={A.loop}
                  />
                )}
                <ChevronDown className="size-4 shrink-0 text-fg-3" />
              </Button>
            }
            items={[
              {
                type: "label",
                label: A.count(rows.filter((r) => r.inGlb).length),
              },
              {
                label: A.none,
                hint: A.noneHint,
                checked: !s.clip,
                onSelect: () => sync.select(null),
              },
              ...rows.map((r) => ({
                label: r.label ? `${r.label}（${r.name}）` : r.name,
                hint: warnText(r),
                disabled: !r.inGlb,
                checked: r.name === s.clip,
                onSelect: () => sync.select(r.name),
                trailing: (
                  <>
                    {r.loop && (
                      <Tip content={A.loop}>
                        <Repeat
                          className="size-3.5 text-fg-3"
                          aria-label={A.loop}
                        />
                      </Tip>
                    )}
                    {r.inGlb && (
                      <span className="text-xs text-fg-3 tabular">
                        {A.sec(r.duration)}
                      </span>
                    )}
                  </>
                ),
              })),
            ]}
          />
          {has && (
            <div className="ml-auto flex shrink-0 items-center gap-1 pl-2">
              <Menu
                trigger={
                  <Button
                    size="sm"
                    variant="ghost"
                    aria-label={A.speed}
                    data-anim-speed=""
                  >
                    <span className="tabular">{A.speedX(s.speed)}</span>
                    <ChevronDown className="size-3.5 text-fg-3" />
                  </Button>
                }
                width="min-w-[120px]"
                items={[
                  { type: "label", label: A.speed },
                  ...SPEEDS.map((x) => ({
                    label: A.speedX(x),
                    checked: x === s.speed,
                    onSelect: () => sync.setSpeed(x),
                  })),
                ]}
              />
              <Tip content={A.loop}>
                <Button
                  size="icon-sm"
                  variant="ghost"
                  aria-label={A.loop}
                  aria-pressed={s.loop}
                  onClick={() => sync.setLoop(!s.loop)}
                  className={cx(ib, on(s.loop))}
                >
                  <Repeat className="size-4" />
                </Button>
              </Tip>
              {ctx?.sink && (
                <Tip content={A.markTip}>
                  <Button
                    size="sm"
                    className="ml-1"
                    icon={<MapPin className="size-4" />}
                    onClick={() => void mark()}
                    data-anim-mark=""
                  >
                    {A.mark}
                  </Button>
                </Tip>
              )}
            </div>
          )}
        </div>
      )}
      {has && (
        <div className="mt-1.5 flex items-center gap-1" data-anim-row="seek">
          <Tip content={A.prev}>
            <Button
              size="icon-sm"
              variant="ghost"
              aria-label={A.prev}
              className={ib}
              onClick={() => sync.step(-1)}
            >
              <StepBack className="size-4" />
            </Button>
          </Tip>
          <Tip content={s.playing ? A.pause : A.play}>
            <Button
              size="icon-sm"
              variant="ghost"
              aria-label={s.playing ? A.pause : A.play}
              className={ib}
              onClick={() => sync.toggle()}
              data-anim-play=""
            >
              {s.playing ? (
                <Pause className="size-4" />
              ) : (
                <Play className="size-4" />
              )}
            </Button>
          </Tip>
          <Tip content={A.next}>
            <Button
              size="icon-sm"
              variant="ghost"
              aria-label={A.next}
              className={ib}
              onClick={() => sync.step(1)}
            >
              <StepForward className="size-4" />
            </Button>
          </Tip>
          <input
            ref={range}
            type="range"
            min={0}
            max={s.dur || 0}
            step={0.001}
            defaultValue={0}
            aria-label={A.seek}
            data-no-drag=""
            className="mx-1.5 block min-w-0 flex-1 accent-[var(--color-accent)]"
            onPointerDown={() => {
              resume.current = sync.get().playing;
              if (resume.current) sync.pause();
            }}
            onPointerUp={() => {
              if (resume.current) sync.play();
              resume.current = false;
            }}
            onChange={(e) => {
              sync.seek(+e.target.value);
              if (time.current)
                time.current.textContent = A.time(
                  +e.target.value,
                  sync.get().dur,
                );
            }}
          />
          <span
            ref={time}
            className="shrink-0 pr-1 text-[12px] text-fg-3 tabular"
            data-anim-time=""
          />
        </div>
      )}
      {viewRow && (
        <div
          className={cx(
            "flex items-center gap-1",
            (rows.length > 0 || has) &&
              "-mx-2.5 mt-2 border-t border-line px-2.5 pt-2",
          )}
          data-anim-row="view"
        >
          {showSockets && (
            <Menu
              width="w-[min(320px,calc(100vw-32px))]"
              trigger={
                <Button
                  size="sm"
                  variant="ghost"
                  aria-label={C.sockets}
                  className={cx("max-w-[180px] min-w-0", on(!!s.socket))}
                  style={{ flexShrink: 1 }}
                  data-sockets=""
                >
                  <Crosshair className="size-4 shrink-0" />
                  <span className="min-w-0 truncate">
                    {s.socket ?? C.sockets}
                  </span>
                  <ChevronDown className="size-3.5 shrink-0 text-fg-3" />
                </Button>
              }
              items={[
                { type: "label", label: C.socketsCount(sockets.length) },
                {
                  label: C.noSocket,
                  hint: C.noSocketHint,
                  checked: !s.socket,
                  onSelect: () => sync.setCompose({ socket: null }),
                },
                ...sockets.map((r) => ({
                  label: r.name,
                  hint: r.missing
                    ? C.notInModel
                    : [
                        r.bone ? C.bone : C.empty,
                        r.attached.length
                          ? C.holds(r.attached.join(t.asset.listSep))
                          : "",
                      ]
                        .filter(Boolean)
                        .join(t.common.sep),
                  disabled: r.missing,
                  checked: r.name === s.socket,
                  onSelect: () =>
                    sync.setCompose({
                      socket: r.name === s.socket ? null : r.name,
                    }),
                })),
                ...(more > 0
                  ? [
                      { type: "separator" as const },
                      {
                        label: C.allNodes,
                        hint: s.allNodes ? undefined : C.allNodesHint(more),
                        checked: s.allNodes,
                        onSelect: () =>
                          sync.setCompose({ allNodes: !s.allNodes }),
                      },
                    ]
                  : []),
              ]}
            />
          )}
          {hasAttach && (
            <Tip content={s.attach ? C.attachOff : C.attachOn}>
              <Button
                size="sm"
                variant="ghost"
                aria-pressed={s.attach}
                onClick={() => sync.setCompose({ attach: !s.attach })}
                className={cx(on(s.attach))}
                data-compose-attach=""
              >
                <BoxIcon className="size-4" />
                {C.attach}
              </Button>
            </Tip>
          )}
          {hasRefs && (
            <Tip content={s.refs ? C.refsOff : C.refsOn}>
              <Button
                size="sm"
                variant="ghost"
                aria-pressed={s.refs}
                onClick={() => sync.setCompose({ refs: !s.refs })}
                className={cx(on(s.refs))}
                data-compose-refs=""
              >
                <Ruler className="size-4" />
                {C.refs}
              </Button>
            </Tip>
          )}
          {skeleton && (
            <Tip content={A.skeleton}>
              <Button
                size="sm"
                variant="ghost"
                aria-label={A.skeleton}
                aria-pressed={s.skeleton}
                onClick={() => sync.setSkeleton(!s.skeleton)}
                className={cx(on(s.skeleton))}
              >
                <Bone className="size-4" />
                {A.bones}
              </Button>
            </Tip>
          )}
        </div>
      )}
      {warns > 0 && (
        <p className="mt-1.5 text-[12px] text-wait">{A.warnSum(warns)}</p>
      )}
    </div>
  );
}

// FrameChip：意见挂的那一帧。在抽屉里时点一下跳到那一帧（旧版本先切到那一版）；onRemove 给意见框里取消挂帧用。
export function FrameChip({
  frame,
  onRemove,
  className,
}: {
  frame: Frame;
  onRemove?: () => void;
  className?: string;
}) {
  const ctx = useFrameCtx();
  const A = t.asset.anim;
  const text = A.frame(frame);
  return (
    <span
      className={cx(
        "inline-flex max-w-full min-w-0 items-center gap-1 rounded-md bg-accent-soft/70 px-1.5 py-0.5 text-[12px] text-accent",
        className,
      )}
      data-frame-chip=""
    >
      <MapPin className="size-3.5 shrink-0" />
      {ctx ? (
        <Tip content={A.jump}>
          <button
            type="button"
            className="min-w-0 truncate font-mono hover:underline"
            onClick={() => ctx.jump(frame)}
            aria-label={`${A.jump}${t.common.sep}${text}`}
          >
            {text}
          </button>
        </Tip>
      ) : (
        <span className="min-w-0 truncate font-mono">{text}</span>
      )}
      {onRemove && (
        <button
          type="button"
          className="ml-0.5 shrink-0 rounded hover:bg-accent/15"
          onClick={onRemove}
          aria-label={A.unmark}
        >
          <X className="size-3.5" />
        </button>
      )}
    </span>
  );
}
