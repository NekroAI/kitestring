import assert from "node:assert/strict";
import { test } from "node:test";
import {
  attachPhase,
  crossed,
  eventNotes,
  eventPoints,
  flashes,
  hasSound,
  jsonEvents,
  parseSound,
} from "./events.ts";

// 机械狗装箱：1.8 秒挂上 cargo，2.6 秒卡扣声和火花；卸箱：0.5 秒摘下
const meta = {
  display: {
    events: {
      load: [
        { t: 2.6, do: "sound", asset: "core:sfx.latch" },
        { t: 1.8, do: "attach", name: " cargo " },
        { t: 2.6, do: "mark", socket: "crate", label: "卡扣" },
        { t: -1, do: "attach", name: "x" }, // 跳过：负数
        { t: 1, do: "explode" }, // 跳过：不认识
        { t: 1, do: "sound" }, // 跳过：没写素材
      ],
      unload: [{ t: 0.5, do: "detach", name: "cargo" }],
      bad: "not a list",
    },
  },
};

test("jsonEvents：按时间排，宽松跳过格式不对的项", () => {
  const ev = jsonEvents(meta);
  assert.deepEqual(Object.keys(ev).sort(), ["load", "unload"]);
  assert.deepEqual(
    ev.load.map((e) => [e.t, e.do]),
    [
      [1.8, "attach"],
      [2.6, "sound"],
      [2.6, "mark"],
    ],
  );
  assert.equal(ev.load[0].name, "cargo");
  assert.equal(ev.load[2].label, "卡扣");
  assert.deepEqual(jsonEvents(null), {});
  assert.deepEqual(jsonEvents({ display: { events: [1] } }), {});
  assert.equal(hasSound(ev), true);
  assert.equal(hasSound({ unload: ev.unload }), false);
});

test("attachPhase：只由时间算，往回拖也对；摘下时记住放地面的时刻", () => {
  const ev = jsonEvents(meta);
  // 装箱：挂上之前在地上（位置取 1.8 秒那一刻），之后挂着
  assert.deepEqual(attachPhase(ev.load, "cargo", 0), { on: false, at: 1.8 });
  assert.deepEqual(attachPhase(ev.load, "cargo", 1.79), { on: false, at: 1.8 });
  assert.deepEqual(attachPhase(ev.load, "cargo", 1.8), { on: true, at: 1.8 });
  assert.deepEqual(attachPhase(ev.load, "cargo", 2.5), { on: true, at: 1.8 });
  // 往回拖到 1 秒：又在地上
  assert.deepEqual(attachPhase(ev.load, "cargo", 1), { on: false, at: 1.8 });
  // 卸箱：开头挂着，0.5 秒后放到地上
  assert.deepEqual(attachPhase(ev.unload, "cargo", 0.2), { on: true, at: 0.5 });
  assert.deepEqual(attachPhase(ev.unload, "cargo", 0.9), {
    on: false,
    at: 0.5,
  });
  // 没写这个挂件的动作：照 clips 规则
  assert.equal(attachPhase(ev.load, "other", 1), null);
  assert.equal(attachPhase(undefined, "cargo", 1), null);
  // 多次挂上摘下
  const multi = [
    { t: 1, do: "attach" as const, name: "c" },
    { t: 2, do: "detach" as const, name: "c" },
    { t: 3, do: "attach" as const, name: "c" },
  ];
  assert.deepEqual(attachPhase(multi, "c", 2.5), { on: false, at: 2 });
  assert.deepEqual(attachPhase(multi, "c", 3.2), { on: true, at: 3 });
});

test("crossed：播放经过的事件，循环每圈都算，拖进度不补放", () => {
  const ev = jsonEvents(meta).load;
  assert.deepEqual(
    crossed(ev, 2.55, 2.62, 3, true).map((e) => e.do),
    ["sound", "mark"],
  );
  assert.deepEqual(crossed(ev, 2.0, 2.5, 3, true), []);
  // 第二圈：raw 5.5 → 5.65（取余 2.5 → 2.65）
  assert.deepEqual(
    crossed(ev, 5.5, 5.65, 3, true).map((e) => e.t),
    [2.6, 2.6],
  );
  // 跨圈：2.9 → 3.1 经过不到；4.7 → 4.85 经过第二圈的 1.8
  assert.deepEqual(crossed(ev, 2.9, 3.1, 3, true), []);
  assert.deepEqual(
    crossed(ev, 4.7, 4.85, 3, true).map((e) => e.do),
    ["attach"],
  );
  // 不循环：超过时长不再触发
  assert.deepEqual(crossed(ev, 3.5, 3.7, 3, false), []);
  // 跳过的一大段（拖进度、切动作）不补放；往回也不放
  assert.deepEqual(crossed(ev, 0, 2.9, 3, true), []);
  assert.deepEqual(crossed(ev, 2.7, 2.5, 3, true), []);
});

test("flashes：标记在事件后闪 0.5 秒，暂停在那几帧也看得到", () => {
  const ev = jsonEvents(meta).load;
  assert.equal(flashes(ev, 2.59).length, 0);
  const f = flashes(ev, 2.85);
  assert.equal(f.length, 1);
  assert.equal(f[0].e.socket, "crate");
  assert.ok(Math.abs(f[0].k - 0.5) < 1e-9);
  assert.equal(flashes(ev, 3.2).length, 0);
});

test("eventPoints：同一时刻合成一个点，超出时长的不画", () => {
  const ev = jsonEvents(meta).load;
  const p = eventPoints(ev, 3);
  assert.deepEqual(
    p.map((x) => [x.t, x.evs.length]),
    [
      [1.8, 1],
      [2.6, 2],
    ],
  );
  assert.deepEqual(
    eventPoints(ev, 2).map((x) => x.t),
    [1.8],
  );
});

test("eventNotes：动作、时间、挂件名、挂点对不上时提示", () => {
  const ev = jsonEvents({
    display: {
      events: {
        load: [
          { t: 3.5, do: "attach", name: "cargo" },
          { t: 1, do: "detach", name: "ghost" },
          { t: 1, do: "mark", socket: "muzzle" },
        ],
        jump: [{ t: 0.1, do: "mark", socket: "crate" }],
      },
    },
  });
  const notes = eventNotes(
    ev,
    [{ name: "load", duration: 3 }],
    new Set(["cargo"]),
    new Set(["crate"]),
  );
  assert.deepEqual(notes, [
    { kind: "evAttach", clip: "load", name: "ghost" },
    { kind: "evSocket", clip: "load", socket: "muzzle" },
    { kind: "evTime", clip: "load", t: 3.5, dur: 3 },
    { kind: "evClip", clip: "jump" },
  ]);
});

test("声音开关默认静音", () => {
  assert.equal(parseSound(null), false);
  assert.equal(parseSound("off"), false);
  assert.equal(parseSound("on"), true);
});
