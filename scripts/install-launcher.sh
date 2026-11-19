#!/usr/bin/env bash
# 安装 / 核对 Hub 的固定启动器 ks-launcher（R-354，只用于 macOS），由 deploy.sh 在替换程序之前调用。
#
# 用法：scripts/install-launcher.sh [--dry-run] <新构建的启动器> <安装路径> [<hub 服务的 plist>]
#
# 启动器授权一次、以后部署都不用重授的前提是它从不变化（macOS 的访问授权按文件哈希认），所以：
#   - 安装路径已有启动器：只核对它是可执行文件，绝不替换（内容和新构建的不同也不替换，只提示）；
#     不是可执行文件时失败退出，交给人处理（替换它就要重新授权）。
#   - 没有：从新构建的复制一份（不覆盖并发装好的那份），提示第一次要在系统设置里允许它。
#   - 新构建里也没有（例如部署的是没有启动器的旧提交）：Hub 服务已经经启动器运行时失败退出（替换后 Hub 起不来），
#     否则只提示。
#   - 给了 plist 且 Hub 服务还没经启动器运行时，提示用 ks install --hub 切换。
set -euo pipefail

DRY=0
if [ "${1:-}" = "--dry-run" ]; then
  DRY=1
  shift
fi
if [ $# -lt 2 ]; then
  sed -n '4p' "$0" >&2
  exit 2
fi
SRC="$1"
DST="$2"
PLIST="${3:-}"

log() { printf '[launcher] %s\n' "$*"; }

uses_launcher=0
if [ -n "$PLIST" ] && [ -f "$PLIST" ] && grep -qF "<string>${DST}</string>" "$PLIST"; then
  uses_launcher=1
fi

if [ -e "$DST" ] || [ -L "$DST" ]; then
  if [ ! -f "$DST" ] || [ ! -x "$DST" ]; then
    log "启动器 ${DST} 不是可执行文件。部署没有替换它（替换后要在系统设置里重新允许）。"
    log "用 chmod 755 ${DST} 修好权限；文件已损坏时删掉它，重跑部署会装一份新的，之后在「系统设置 → 隐私与安全性」重新允许 ks-launcher"
    exit 1
  fi
  if [ -f "$SRC" ] && ! cmp -s "$SRC" "$DST"; then
    log "启动器：${DST}（与新构建的不同，按约定不替换，已有的授权继续有效）"
  else
    log "启动器：$DST"
  fi
elif [ -f "$SRC" ] || [ "$DRY" = 1 ]; then
  if [ "$DRY" = 1 ]; then
    log "[dry-run] 安装启动器 ${SRC} -> $DST"
  else
    mkdir -p "$(dirname "$DST")"
    tmp="$(mktemp "$(dirname "$DST")/.ks-launcher.XXXXXX")"
    cp "$SRC" "$tmp"
    chmod 755 "$tmp"
    # ln 在目标已存在时失败：不覆盖并发装好的那份
    ln "$tmp" "$DST" 2>/dev/null || true
    rm -f "$tmp"
    [ -x "$DST" ] || { log "安装启动器 ${DST} 失败"; exit 1; }
    log "已安装启动器 ${DST}。附件目录在外置硬盘上时，第一次访问会弹出 macOS 的询问，允许即可；也可以先在「系统设置 → 隐私与安全性 → 完全磁盘访问权限」里加入它"
  fi
elif [ "$uses_launcher" = 1 ]; then
  log "Hub 服务经启动器 ${DST} 运行，但它不存在，新构建里也没有 ${SRC}：替换程序后 Hub 会起不来，部署停止"
  exit 1
else
  log "新构建里没有启动器 ${SRC}，跳过（Hub 服务不经启动器运行）"
  exit 0
fi

if [ -n "$PLIST" ] && [ -f "$PLIST" ] && [ "$uses_launcher" = 0 ]; then
  log "Hub 服务还没经启动器运行：部署完成后运行一次 ks install --hub --daemon=false --claude=false --codex=false 切换，之后部署不用再运行"
fi
