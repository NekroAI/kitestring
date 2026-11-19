import assert from "node:assert/strict";
import { test } from "node:test";
import {
  animRows,
  createAnimSync,
  defaultClip,
  glbLoop,
  inferFps,
  jsonAnims,
  stepFrame,
  wrapTime,
  type ClipInfo,
} from "./anim.ts";

const clips: ClipInfo[] = [
  { name: "idle", duration: 2, fps: 30 },
  { name: "run_forward", duration: 0.8, fps: 30 },
  { name: "death", duration: 1.5, fps: 30 },
];

test("asset.json 的 display.animations 三种写法", () => {
  assert.equal(jsonAnims({}), null);
  assert.deepEqual(
    jsonAnims({ display: { animations: ["idle", " ", "run"] } }),
    [
      { name: "idle", label: undefined, loop: undefined },
      { name: "run", label: undefined, loop: undefined },
    ],
  );
  assert.deepEqual(
    jsonAnims({
      display: { animations: [{ name: "run", loop: true, label: "跑" }] },
    }),
    [{ name: "run", label: "跑", loop: true }],
  );
  assert.deepEqual(
    jsonAnims({ display: { animations: { death: { loop: false } } } }),
    [{ name: "death", label: undefined, loop: false }],
  );
});

test("GLB 的循环标记：extras.loop 优先，其次名字后缀", () => {
  assert.equal(glbLoop("walk-loop"), true);
  assert.equal(glbLoop("walk_cycle"), true);
  assert.equal(glbLoop("walk"), undefined);
  assert.equal(glbLoop("walk-loop", { loop: false }), false);
});

test("动作列表以 asset.json 为准，对不上的标出来", () => {
  // 没写 display.animations：按 GLB
  assert.deepEqual(
    animRows(clips, {}).map((r) => r.name),
    ["idle", "run_forward", "death"],
  );
  const g = [...clips, { name: "wave", duration: 1, fps: 30, loop: true }];
  const rows = animRows(g, {
    display: {
      animations: {
        death: { loop: false },
        idle: { loop: true, label: "待机" },
        jump: {},
        wave: { loop: false },
      },
    },
  });
  assert.deepEqual(
    rows.map((r) => [r.name, r.inGlb, r.loop, r.warn]),
    [
      ["death", true, false, undefined],
      ["idle", true, true, undefined],
      ["jump", false, undefined, "missingInGlb"],
      ["wave", true, false, "loopDiff"],
      ["run_forward", true, undefined, "notInJson"],
    ],
  );
  assert.equal(rows[1].label, "待机");
  assert.equal(defaultClip(rows), "idle");
  assert.equal(
    defaultClip(
      animRows(
        [
          { name: "a", duration: 1, fps: 30 },
          { name: "b_idle", duration: 1, fps: 30 },
        ],
        null,
      ),
    ),
    "b_idle",
  );
  assert.equal(defaultClip([]), null);
});

test("帧率、取时间、逐帧", () => {
  assert.equal(inferFps([[0, 1 / 30, 2 / 30, 3 / 30]]), 30);
  assert.equal(inferFps([[0]]), 30);
  assert.equal(wrapTime(2.5, 2, true), 0.5);
  assert.equal(wrapTime(2.5, 2, false), 2);
  assert.equal(wrapTime(1, 0, true), 0);
  assert.equal(stepFrame(0.5, 10, 1, 2), 0.6);
  assert.equal(stepFrame(0, 10, -1, 2), 0);
  assert.equal(stepFrame(2, 10, 1, 2), 2);
});

test("同步时钟：几个查看器按同一个时间取姿势", () => {
  let now = 0;
  const s = createAnimSync(() => now);
  const off = s.register({
    id: "a",
    clips,
    skeleton: true,
    canvas: () => null,
  });
  // 默认选 idle 并播放
  assert.equal(s.get().clip, "idle");
  assert.equal(s.get().playing, true);
  now = 500;
  assert.equal(s.timeAt(), 0.5);
  s.setSpeed(2);
  now = 1000;
  assert.equal(s.timeAt(), 1.5);
  now = 1500;
  assert.equal(s.timeAt(), 0.5); // 2 秒的动作循环
  s.pause();
  now = 9000;
  assert.equal(s.timeAt(), 0.5);
  s.step(1);
  assert.ok(Math.abs(s.timeAt() - (0.5 + 1 / 30)) < 1e-9);
  // 选不循环的动作，播到末尾停住，再播从头开始
  s.select("death", 0);
  s.setLoop(false);
  s.setSpeed(1);
  now += 2000;
  s.settle();
  assert.equal(s.get().playing, false);
  assert.equal(s.timeAt(), 1.5);
  s.play();
  assert.equal(s.timeAt(), 0);
  // 跳到某一帧（点意见）：选动作、停在那一秒
  s.select("run_forward", 0.4, false);
  assert.deepEqual(s.frame(3), { anim: "run_forward", t: 0.4, v: 3 });
  s.select(null);
  assert.equal(s.frame(3), null);
  off();
  assert.equal(s.viewers().length, 0);
});

test("选过动作后加载完不再换成默认动作", () => {
  let now = 0;
  const s = createAnimSync(() => now);
  s.select("run_forward", 0.4, false); // 点意见跳帧时模型还没加载
  s.register({ id: "a", clips, skeleton: false, canvas: () => null });
  assert.equal(s.get().clip, "run_forward");
  assert.equal(s.get().dur, 0.8);
  assert.equal(s.timeAt(), 0.4);
});
