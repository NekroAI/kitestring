import assert from "node:assert/strict";
import { test } from "node:test";
import {
  attachShown,
  composeNotes,
  hiddenNodes,
  refPosition,
  socketRows,
  type ComposeItem,
  type NodeInfo,
} from "./compose.ts";

// 机械狗：骨骼 root / chassis / arm_main、货叉下的空节点 crate、名字带 hand 的骨骼
const nodes: NodeInfo[] = [
  { name: "root", bone: true, empty: false },
  { name: "chassis", bone: true, empty: false },
  { name: "arm_main", bone: true, empty: false },
  { name: "crate", bone: false, empty: true },
  { name: "hand_r", bone: true, empty: false },
  { name: "Dog_Rig", bone: false, empty: false },
];
const box: ComposeItem = {
  kind: "attach",
  name: "cargo",
  socket: "crate",
  asset: "core:prop.supply_crate",
  clips: ["carry_idle", "carry_walk"],
  file: { id: "f1", name: "box.glb" },
};

test("挂点列表：默认列挂件引用的、空节点、带通用词的；全部节点列出骨骼", () => {
  const typo: ComposeItem = { kind: "attach", socket: "crate_typo", asset: "core:p.x" };
  const rows = socketRows(nodes, [box, typo], false);
  assert.deepEqual(
    rows.map((r) => r.name),
    ["crate", "crate_typo", "hand_r"],
  );
  assert.deepEqual(rows[0].attached, ["cargo"]);
  assert.equal(rows[1].missing, true);
  assert.equal(rows[2].bone, true);
  const all = socketRows(nodes, [box], true).map((r) => r.name);
  assert.deepEqual(all, ["crate", "root", "chassis", "arm_main", "hand_r", "Dog_Rig"]);
  assert.equal(hiddenNodes(nodes, [box]), 4);
  // 没有挂件时默认只有空节点和带通用词的
  assert.deepEqual(
    socketRows(nodes, [], false).map((r) => r.name),
    ["crate", "hand_r"],
  );
});

test("挂件按动作显示：写了 clips 的只在这些动作里，静止姿势不显示；总开关关时都不显示", () => {
  assert.equal(attachShown(box, "carry_walk", true), true);
  assert.equal(attachShown(box, "idle", true), false);
  assert.equal(attachShown(box, null, true), false);
  assert.equal(attachShown(box, "carry_walk", false), false);
  const always: ComposeItem = { ...box, clips: null };
  assert.equal(attachShown(always, null, true), true);
  assert.equal(attachShown({ ...box, clips: [] }, "idle", true), true);
});

test("比例参照：写了 offset 用它，没写时放在右侧、底面对齐、前后居中", () => {
  const main = { min: [-0.5, 0, -0.3], max: [0.5, 0.8, 0.3] } as const;
  const ref = { min: [-0.2, -0.1, -0.1], max: [0.2, 1.7, 0.3] } as const;
  assert.deepEqual(
    refPosition(
      { min: [...main.min], max: [...main.max] },
      { min: [...ref.min], max: [...ref.max] },
      [1, 0, 2],
    ),
    [1, 0, 2],
  );
  const p = refPosition(
    { min: [...main.min], max: [...main.max] },
    { min: [...ref.min], max: [...ref.max] },
  );
  assert.ok(Math.abs(p[0] - (0.5 + 0.15 + 0.2)) < 1e-9, `x ${p[0]}`);
  assert.ok(Math.abs(p[1] - 0.1) < 1e-9); // 参照底面（-0.1）抬到主模型底面（0）
  assert.ok(Math.abs(p[2] - -0.1) < 1e-9); // 参照中心 z=0.1 挪到 0
  // offset 写得不对时退回自动摆放
  assert.notDeepEqual(
    refPosition(
      { min: [...main.min], max: [...main.max] },
      { min: [...ref.min], max: [...ref.max] },
      [1, 2],
    ),
    [1, 2],
  );
});

test("查看器提示：素材找不到、不是 GLB，同一素材只提示一次", () => {
  const items: ComposeItem[] = [
    { kind: "attach", socket: "a", asset: "core:p.none", missing: "asset" },
    { kind: "ref", asset: "core:p.none", missing: "asset" },
    { kind: "ref", asset: "core:p.pic", variant: "red", missing: "glb" },
    box,
  ];
  assert.deepEqual(composeNotes(items), [
    { kind: "asset", asset: "core:p.none" },
    { kind: "glb", asset: "core:p.pic@red" },
  ]);
});
