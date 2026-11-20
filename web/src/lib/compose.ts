// 3D 查看器的组合预览（R-407）：挂点、挂件、比例参照的纯逻辑（不引 three.js），查看器（app/viewer3d.tsx）和播放控件（app/animBar.tsx）共用，node --test 可测。
// 配置来自 asset.json 的 display.attach / display.refs，Hub 在素材详情的每一版里解析好（compose，找到的素材带 GLB）；纸鸢这边不做编辑。

/** Hub 给的组合预览一项（与后端 store.AssetCompose 一致）。 */
export type ComposeItem = {
  kind: "attach" | "ref" | "sound"; // sound：动作事件引用的音效素材（R-408），file 是音频文件
  name?: string; // 挂件名（不写时取 socket）；动作事件（R-408）按它引用
  socket?: string;
  asset: string;
  variant?: string;
  clips?: string[] | null;
  offset?: number[] | null;
  code?: string;
  title?: string;
  file?: { id: string; name: string } | null;
  missing?: "asset" | "glb" | "audio" | "";
};

/** GLB 里一个不带网格的节点。bone：蒙皮骨骼；empty：空节点（不是骨骼，自己和下面都没有网格）。 */
export type NodeInfo = { name: string; bone: boolean; empty: boolean };

// SOCKET_RE：名字里带挂点通用词（与后端 protocol.IsSocketName 一致）。不按 crate、weapon 这类具体名字猜，各引擎命名不一。
export const SOCKET_RE =
  /(socket|attach|mount|slot|hardpoint|anchor|hand|grip|muzzle|holster|^s_|^sk_)/i;

/** 默认列出的挂点：空节点、名字带通用词的，或挂件引用到的；其余要切「全部节点」。 */
export function isSocket(n: NodeInfo, used: ReadonlySet<string>): boolean {
  return n.empty || SOCKET_RE.test(n.name) || used.has(n.name);
}

/** 挂点列表的一行。 */
export type SocketRow = {
  name: string;
  bone: boolean;
  attached: string[]; // 挂在这里的挂件名
  missing?: boolean; // 挂件引用了、模型里没有
};

// socketRows：挂点列表。挂件引用到的排前面（模型里没有的也列出、标出来），其余按模型里的顺序；all 时列出全部骨骼和空节点。
export function socketRows(
  nodes: NodeInfo[],
  items: ComposeItem[],
  all: boolean,
): SocketRow[] {
  const att = items.filter((x) => x.kind === "attach" && x.socket);
  const used = new Set(att.map((x) => x.socket!));
  const byName = new Map(nodes.map((n) => [n.name, n]));
  const on = (s: string) =>
    att.filter((x) => x.socket === s).map((x) => x.name || s);
  const rows: SocketRow[] = [];
  const seen = new Set<string>();
  for (const s of used) {
    seen.add(s);
    const n = byName.get(s);
    rows.push({ name: s, bone: !!n?.bone, attached: on(s), missing: !n });
  }
  for (const n of nodes) {
    if (!n.name || seen.has(n.name)) continue;
    if (!all && !isSocket(n, used)) continue;
    seen.add(n.name);
    rows.push({ name: n.name, bone: n.bone, attached: [] });
  }
  return rows;
}

/** 「全部节点」比默认列表多出来几个（0 时不必给开关）。 */
export function hiddenNodes(nodes: NodeInfo[], items: ComposeItem[]): number {
  return (
    socketRows(nodes, items, true).length -
    socketRows(nodes, items, false).length
  );
}

// attachShown：挂件此刻显示不显示。总开关关着时都不显示；写了 clips 的只在这些动作里显示（静止姿势也不显示），没写的一直显示。
export function attachShown(
  item: ComposeItem,
  clip: string | null,
  on: boolean,
): boolean {
  if (!on) return false;
  const clips = item.clips ?? [];
  if (!clips.length) return true;
  return !!clip && clips.includes(clip);
}

/** 当前动作里按 clips 隐藏的一个挂件：名字和它在哪些动作里显示。 */
export type HiddenAttach = { name: string; clips: string[] };

// hiddenAttach（R-414）：挂件开关开着、但因为当前动作（或静止姿势）不在 clips 里而隐藏的挂件，播放控件据此提示「只在哪些动作里显示」，
// 免得以为没挂上。素材找不到的不算（另有提示）；这个动作的事件里挂上 / 摘下它时由事件决定显示，也不算。
// evNames：当前动作的动作事件里挂上 / 摘下引用到的挂件名。
export function hiddenAttach(
  items: ComposeItem[],
  clip: string | null,
  on: boolean,
  evNames: ReadonlySet<string> = new Set(),
): HiddenAttach[] {
  if (!on) return [];
  const out: HiddenAttach[] = [];
  for (const x of items) {
    if (x.kind !== "attach" || !x.file || x.missing) continue;
    const name = x.name || x.socket || "";
    if (clip && evNames.has(name)) continue;
    if (attachShown(x, clip, true)) continue;
    out.push({ name, clips: x.clips ?? [] });
  }
  return out;
}

/** 轴对齐包围盒（米）。 */
export type Box = {
  min: [number, number, number];
  max: [number, number, number];
};

// refPosition：参照素材的位置（参照模型原点放在哪，相对主模型原点）。写了 offset 就用它；
// 没写时排在主模型（和前面已放的参照）右侧，留一点间隙，底面对齐，前后居中。
export function refPosition(
  main: Box,
  ref: Box,
  offset?: number[] | null,
): [number, number, number] {
  if (offset && offset.length === 3 && offset.every(Number.isFinite))
    return [offset[0], offset[1], offset[2]];
  const size = Math.max(
    main.max[0] - main.min[0],
    main.max[1] - main.min[1],
    main.max[2] - main.min[2],
  );
  const gap = Math.max(0.1, size * 0.15);
  const zc = (main.min[2] + main.max[2]) / 2 - (ref.min[2] + ref.max[2]) / 2;
  return [main.max[0] + gap - ref.min[0], main.min[1] - ref.min[1], zc];
}

/** 查看器里给的提示（找不到挂点 / 素材，不报错）。 */
export type ComposeNote =
  | { kind: "socket"; name: string; socket: string }
  | { kind: "asset"; asset: string }
  | { kind: "glb"; asset: string }
  | { kind: "audio"; asset: string }
  | { kind: "load"; asset: string };

/** 配置里写的素材 id（带变体时「id@变体名」）。 */
export const targetOf = (x: Pick<ComposeItem, "asset" | "variant">) =>
  x.variant ? `${x.asset}@${x.variant}` : x.asset;

// composeNotes：Hub 解析时就知道的问题（素材找不到、不是 GLB），同一素材只提示一次；挂点找不到要等模型加载后由查看器补上。
export function composeNotes(items: ComposeItem[]): ComposeNote[] {
  const out: ComposeNote[] = [];
  const seen = new Set<string>();
  for (const x of items) {
    if (!x.missing) continue;
    const a = targetOf(x);
    const k = x.missing + ":" + a;
    if (seen.has(k)) continue;
    seen.add(k);
    out.push({ kind: x.missing, asset: a });
  }
  return out;
}

/** 素材详情里一版的组合预览（没有时空数组）。 */
export const composeOf = (v?: { compose?: ComposeItem[] | null } | null) =>
  v?.compose ?? [];
