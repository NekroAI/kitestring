<p align="center">
  <img src="web/public/icon.svg" width="88" height="88" alt="纸鸢">
</p>

<h1 align="center">纸鸢 · Kitestring</h1>

<p align="center">
  <strong>风筝飞得再远，线在你手里。</strong><br>
  一块看板管住你所有设备上的 Claude Code 与 Codex 会话：看清每个 agent 在做什么，回答它在等的事，交给它下一件事——在电脑前或手机上都行。
</p>

<p align="center">
  <a href="docs/install.zh-CN.md">安装</a> ·
  <a href="docs/how-it-works.zh-CN.md">工作原理</a> ·
  <a href="README.md">English</a>
</p>

<p align="center">
  <a href="https://github.com/NekroAI/kitestring/actions/workflows/ci.yml"><img src="https://github.com/NekroAI/kitestring/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/NekroAI/kitestring/releases/latest"><img src="https://img.shields.io/github/v/release/NekroAI/kitestring" alt="Release"></a>
  <img src="https://img.shields.io/badge/platform-macOS%20%7C%20Linux-lightgrey" alt="Platform: macOS | Linux">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-AGPL--3.0-blue" alt="License: AGPL-3.0"></a>
</p>

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/screenshot-sessions-dark.png">
  <img src="docs/assets/screenshot-sessions-light.png" alt="会话墙：多台设备上的 agent 会话按项目分组，每个都显示正在做什么、是否在等你。">
</picture>

## 它能做什么

- **一眼看清所有会话**：所有设备上的会话按项目归在一起——运行中、等你处理、待命中还是已完成，以及此刻在做什么。
- **在哪都能答复**：agent 需要你拍板时，手机收到推送；点一个选项，答复就进入那个 agent 的下一轮。
- **事情都记在一处**：agent 边做边记下需求；你确认、交给会话、对着截图验收。

## 为手机和 iPad 而做

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/showcase-dark.png">
  <img src="docs/assets/showcase-light.png" alt="iPad 上是两套待选的设计方案，iPhone 上是等你回复、等你授权的会话。">
</picture>

离开工位也不断线：沙发上、地铁上、躺在床上，照样推进进度。

- **像 App 一样用**：添加到主屏幕后全屏运行，可用 passkey（面容 / 触控 ID）登录。
- **轮到你时会提醒**：iPhone、iPad 和桌面浏览器都能收到系统推送（也支持 Bark、企业微信），点通知直达对应的会话或需求。
- **为触屏打磨**：长按卡片拖到别的列改状态、左右滑看截图、验收时滑动切换需求；该稳的地方不会乱缩放、乱回弹。

### 用 Apple Pencil 直接批注

<p align="center"><img src="docs/assets/annotate.gif" width="760" alt="用笔在截图上圈出数量输入框、画箭头、写批注。"></p>

在 agent 交来的截图上圈、框、画箭头、写字。笔画粗细跟随笔压，用过手写笔后自动忽略手掌；标注好的图会和你的意见一起送回 agent。手指、鼠标、触控板也都能用。

### 随手验收

<table>
  <tr>
    <td width="300"><picture><source media="(prefers-color-scheme: dark)" srcset="docs/assets/phone-review-dark.png"><img src="docs/assets/phone-review-light.png" width="300" alt="手机上的验收界面：带测试结果和截图的时间线，底部是通过 / 退回修改。"></picture></td>
    <td>
      <ul>
        <li><b>拿证据说话</b>：agent 交测试结果、截图、录屏；声称有截图却没附图的证据会被拒收。</li>
        <li><b>一下通过或退回</b>：写几句或贴张图就能退回；通过后自动滑到下一条。</li>
        <li><b>看图选方案</b>：agent 交两三套带效果图的方案，点一下选定。</li>
        <li><b>批准方案可附言</b>：批准或拒绝都能附上说明和图片。</li>
      </ul>
    </td>
  </tr>
</table>

## 更多亮点

- **交出下一件事**：把需求交给会话；待办会在它空闲时整批送达，挂着 `ks wait` 的会话几秒内就开工。
- **分阶段的计划**：大目标拆成阶段，上一阶段你验收通过后，下一阶段才解锁。
- **每个会话实时可见**：最近的工具调用实时滚动，小圆环显示上下文占用；接入你自己的模型后，还有一句话进展摘要。
- **跨设备归成一个项目**：按 git remote 归并，笔记本和 Mac mini 上的同一个仓库就是同一个项目。
- **永不挡路**：hook 1.5 秒超时、出错放行；断网的设备先缓存事件，恢复后补传。

## 工作原理

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/architecture-dark.zh-CN.svg">
  <img src="docs/assets/architecture-light.zh-CN.svg" alt="每台设备上的 Claude Code 与 Codex 连到 ks daemon；daemon 与 ks hub 通信；你在任意浏览器或手机上使用 Hub 的网页看板。">
</picture>

每台设备上的小 daemon 通过官方的 hooks 与 MCP 感知你的 agent，汇报给一台常开机器上的 Hub；你在任意浏览器里用 Hub 的网页看板。细节见[工作原理](docs/how-it-works.zh-CN.md)。

## 上手

**1. 安装 `ks`**：Hub 和每台写代码的设备都装（macOS 或 Linux）：

```bash
curl -fsSL https://raw.githubusercontent.com/NekroAI/kitestring/main/scripts/install.sh | sh
```

**2. 启动 Hub**：在一台常开的机器上运行，然后打开 `http://<hub>:7310`：

```bash
ks hub passwd && ks install --hub --claude=false --codex=false --daemon=false
```

**3. 接入每台设备**：token 在网页「设置 → 设备」里新增：

```bash
ks login --hub http://<hub>:7310 --token kst_xxx && ks install
```

打开 Claude Code 或 Codex，会话就会出现在看板上。升级、Codex 设置与从源码构建见[安装说明](docs/install.zh-CN.md)。

## 不碰你的订阅

Kitestring 只使用 Claude Code / Codex 官方文档化的扩展点：不碰登录凭证、不代理模型流量、不消耗订阅额度，也不会自己驱动 agent。[详细说明](docs/how-it-works.zh-CN.md#账号安全边界)。

## 参与贡献

欢迎 issue 与 PR，见 [CONTRIBUTING.md](CONTRIBUTING.md)；安全问题请按 [SECURITY.md](SECURITY.md) 私下报告。

## 许可证

[AGPL-3.0-or-later](LICENSE)
