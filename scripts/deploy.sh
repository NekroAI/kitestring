#!/usr/bin/env bash
# 纸鸢本机部署：构建 -> 备份 -> 替换 -> 重启 -> 核对，任一步不对就回滚并以非零状态退出。
# 替换时一并更新 $KS_HOME/dsh-plugin（DSH 插件的稳定副本，install-dsh-plugin.sh 链接的就是它），
# 回滚时恢复成上一份；这样仓库工作区里的开发改动不会被热加载进在用的 DSH Host。
#
# 用法：scripts/deploy.sh [--dry-run] [<git 提交>]
#   不带提交参数时部署当前工作区（HEAD）；带提交参数时在临时 worktree 里构建该提交。
#
# 环境变量（演示 / 试跑时覆盖）：
#   KS_HOME           本机数据目录，默认 ~/.kitestring
#   KS_BIN            安装路径，默认 ~/.local/bin/ks
#   KS_HUB_URL        Hub 地址，默认 http://127.0.0.1:7310
#   LAUNCHD_PREFIX    launchd 域，默认 gui/$(id -u)
#   LAUNCHD_HUB       hub 服务名，默认 ai.nekro.kitestring.hub
#   LAUNCHD_DAEMON    daemon 服务名，默认 ai.nekro.kitestring.daemon
#   KS_LAUNCHCTL      launchctl 命令，默认 launchctl（试跑可换成空操作脚本）
#   KS_EXPECT_COMMIT  部署后核对用的期望提交号，默认取构建目录的 HEAD（试跑造失败用）
#   KS_BACKUP_DIR     部署备份目录（旧程序、数据库、DSH 插件副本），见下面「备份目录」
#   KS_KEEP_BACKUPS   备份目录里 ks-*、hub-*.db、dsh-plugin-* 各保留最近几份，默认 10
#   KS_VERIFY_TIMEOUT 部署后等待服务就绪的秒数，默认 30
#   KS_LAUNCHER       Hub 的固定启动器，默认 $KS_HOME/bin/ks-launcher（只用于 macOS，见下面「启动器」）
#   KS_HUB_PLIST      hub 服务的 plist，默认 ~/Library/LaunchAgents/$LAUNCHD_HUB.plist（只读，用来提示是否已经经启动器运行）
#
# 备份范围：只备份程序（ks）、DSH 插件和数据库（hub.db），不备份附件。
# 附件（附件、原型包、缩略图）默认在 $KS_HOME/files；Hub 服务用 --files-dir 或 KS_FILES_DIR
# 指定了别的目录（如外置硬盘，见 docs/install.zh-CN.md「附件目录」）时在那个目录。两种情况都不在部署备份里，需要自行备份。
# 部署不读写附件目录；外置硬盘没挂上时 Hub 照常启动、核对照常通过，只是拒收上传（设置页显示「存储不可用」）。
#
# 备份目录（R-354）：按顺序取 环境变量 KS_BACKUP_DIR、文件 $KS_HOME/backup-dir 第一行写的路径（可用 ~/ 开头），
# 都没有时用 $KS_HOME/backups。放到外置硬盘上时写进 $KS_HOME/backup-dir，之后每次部署都用它，不用每次带环境变量：
#   echo /Volumes/DataSSD/kitestring/backups > ~/.kitestring/backup-dir
# 指定的目录必须已存在且能写，部署不替你创建（外置硬盘没挂上时在挂载点下建目录会写到系统盘）；
# 不可用时部署在构建之前就失败退出，什么都不改，也不改写到默认目录。回滚从这个目录取旧程序。
# 把已有备份搬过去用 scripts/migrate-storage.sh（拷贝并核对文件数和 sha256，见 docs/install.zh-CN.md「附件目录」）。
#
# 启动器（R-354，macOS）：Hub 服务经固定启动器 $KS_HOME/bin/ks-launcher 运行，macOS 的访问授权（外置硬盘）记在它上面。
# 部署在替换程序之前用 scripts/install-launcher.sh 安装（没有时）和核对（可执行）它，绝不替换已有的那份，
# 内容和新构建的不同也只提示——替换后授权失效，要在系统设置里重新允许。见 docs/install.zh-CN.md「Hub 的启动器」。
set -euo pipefail

DRY=0
TARGET=""
for a in "$@"; do
  case "$a" in
    --dry-run) DRY=1 ;;
    -h | --help) sed -n '2,38p' "$0"; exit 0 ;;
    -*) echo "未知参数：$a" >&2; exit 2 ;;
    *) TARGET="$a" ;;
  esac
done

KS_HOME="${KS_HOME:-$HOME/.kitestring}"
KS_BIN="${KS_BIN:-$HOME/.local/bin/ks}"
KS_HUB_URL="${KS_HUB_URL:-http://127.0.0.1:7310}"
LAUNCHD_PREFIX="${LAUNCHD_PREFIX:-gui/$(id -u)}"
LAUNCHD_HUB="${LAUNCHD_HUB:-ai.nekro.kitestring.hub}"
LAUNCHD_DAEMON="${LAUNCHD_DAEMON:-ai.nekro.kitestring.daemon}"
KS_LAUNCHCTL="${KS_LAUNCHCTL:-launchctl}"
KS_VERIFY_TIMEOUT="${KS_VERIFY_TIMEOUT:-30}"
KS_LAUNCHER="${KS_LAUNCHER:-$KS_HOME/bin/ks-launcher}"
KS_HUB_PLIST="${KS_HUB_PLIST:-$HOME/Library/LaunchAgents/$LAUNCHD_HUB.plist}"
KS_KEEP_BACKUPS="${KS_KEEP_BACKUPS:-10}"
export KS_HOME

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TS="$(date +%Y%m%d-%H%M%S)"
# 备份目录（R-354）：KS_BACKUP_DIR > $KS_HOME/backup-dir > $KS_HOME/backups
BACKUP_DIR_FILE="$KS_HOME/backup-dir"
BACKUP_DIR_FROM=""
if [ -n "${KS_BACKUP_DIR:-}" ]; then
  BACKUP_DIR="$KS_BACKUP_DIR"
  BACKUP_DIR_FROM="环境变量 KS_BACKUP_DIR"
elif [ -f "$BACKUP_DIR_FILE" ] && [ -n "$(head -n1 "$BACKUP_DIR_FILE" | tr -d '[:space:]')" ]; then
  BACKUP_DIR="$(head -n1 "$BACKUP_DIR_FILE" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')"
  BACKUP_DIR_FROM="$BACKUP_DIR_FILE"
else
  BACKUP_DIR="$KS_HOME/backups"
fi
case "$BACKUP_DIR" in
  \~) BACKUP_DIR="$HOME" ;;
  \~/*) BACKUP_DIR="$HOME/${BACKUP_DIR#??}" ;;
esac
ROLLBACK_BIN="$BACKUP_DIR/ks-$TS"
DB_BACKUP="$BACKUP_DIR/hub-$TS.db"
PLUGIN_DIR="$KS_HOME/dsh-plugin"
PLUGIN_BACKUP="$BACKUP_DIR/dsh-plugin-$TS"
HAD_PLUGIN=0
PLUGIN_REPLACED=0
WT=""
HAD_OLD=0
REPLACED=0
STEP="初始化"

log() { printf '[deploy] %s\n' "$*"; }
# run：真实执行；--dry-run 时只打印
run() {
  if [ "$DRY" = 1 ]; then
    printf '[dry-run] %s\n' "$*"
  else
    "$@"
  fi
}

cleanup() {
  if [ -n "$WT" ] && [ -d "$WT" ]; then
    git -C "$REPO" worktree remove --force "$WT" >/dev/null 2>&1 || true
  fi
}

restart_services() {
  run "$KS_LAUNCHCTL" kickstart -k "$LAUNCHD_PREFIX/$LAUNCHD_HUB"
  run "$KS_LAUNCHCTL" kickstart -k "$LAUNCHD_PREFIX/$LAUNCHD_DAEMON"
}

# verify <期望提交>：核对版本、首页脚本、daemon 连接；失败时在 STEP 里写明哪一步，并返回非零
verify() {
  local want="$1" deadline out js size
  deadline=$(($(date +%s) + KS_VERIFY_TIMEOUT))

  STEP="核对 ks version"
  out="$("$KS_BIN" version 2>&1)" || true
  log "ks version：$out"
  case "$out" in
    *"$want"*) ;;
    *) log "版本里没有目标提交 $want"; return 1 ;;
  esac

  STEP="核对首页 JS 资源"
  local html=""
  while [ "$(date +%s)" -lt "$deadline" ]; do
    html="$(curl -fsS --max-time 5 "$KS_HUB_URL/" 2>/dev/null || true)"
    [ -n "$html" ] && break
    sleep 1
  done
  js="$(printf '%s' "$html" | grep -o 'assets/index-[^"]*\.js' | head -n1 || true)"
  if [ -z "$js" ]; then
    log "首页拿不到或没有引用 assets/index-*.js（$KS_HUB_URL/）"
    return 1
  fi
  size="$(curl -fsS --max-time 10 -o /dev/null -w '%{http_code} %{size_download}' "$KS_HUB_URL/$js" 2>/dev/null || echo "000 0")"
  log "$js -> HTTP/体积：$size"
  case "$size" in
    "200 0" | "200 "*" 0") log "JS 体积为 0"; return 1 ;;
    "200 "*) ;;
    *) log "JS 没有返回 200"; return 1 ;;
  esac

  STEP="核对 ks status connected"
  while [ "$(date +%s)" -lt "$deadline" ]; do
    out="$("$KS_BIN" status 2>&1 || true)"
    if printf '%s' "$out" | grep -Eq '"connected": *true'; then
      log "ks status：connected"
      return 0
    fi
    sleep 1
  done
  log "ks status 没有显示 connected：$out"
  return 1
}

# rollback_plugin：把 DSH 插件副本恢复成部署前的样子（原先没有就删掉新放的）
rollback_plugin() {
  [ "$PLUGIN_REPLACED" = 1 ] || return 0
  rm -rf "$PLUGIN_DIR"
  if [ "$HAD_PLUGIN" = 1 ] && [ -d "$PLUGIN_BACKUP" ]; then
    cp -R "$PLUGIN_BACKUP" "$PLUGIN_DIR"
    log "已恢复 DSH 插件副本 $PLUGIN_BACKUP -> $PLUGIN_DIR"
  else
    log "已删除新放的 DSH 插件副本 ${PLUGIN_DIR}（原先没有）"
  fi
}

rollback() {
  log "开始回滚"
  rollback_plugin || log "恢复 DSH 插件副本失败，请人工处理：$PLUGIN_BACKUP"
  if [ "$HAD_OLD" = 1 ] && [ -f "$ROLLBACK_BIN" ]; then
    rm -f "$KS_BIN"
    cp "$ROLLBACK_BIN" "$KS_BIN"
    log "已恢复旧程序 $ROLLBACK_BIN -> $KS_BIN"
    restart_services || true
    # 回滚后再核对一次（只看服务是否正常，不比提交号）
    local old_ver
    old_ver="$("$KS_BIN" version 2>&1 | grep -o '([0-9a-f]*)' | tr -d '()' | head -n1 || true)"
    if verify "${old_ver:-ks}"; then
      log "回滚后核对通过"
    else
      log "回滚后核对仍失败（失败在：${STEP}），需要人工介入；数据库备份：$DB_BACKUP"
    fi
  else
    log "没有可恢复的旧程序（原先未安装），请人工处理"
  fi
}

fail() {
  log "失败于步骤「${STEP}」"
  if [ "$REPLACED" = 1 ]; then
    rollback
  else
    log "尚未替换线上程序，无需回滚"
  fi
  cleanup
  exit 1
}

trap 'fail' ERR
trap cleanup EXIT
set -E

# 0. 检查备份目录：指定了目录（环境变量或 backup-dir 文件）时必须已存在且能写，不替它创建、不改用默认目录
STEP="检查备份目录"
if [ -n "$BACKUP_DIR_FROM" ]; then
  why=""
  if [ ! -e "$BACKUP_DIR" ]; then
    why="不存在（外置硬盘没挂上？）"
  elif [ ! -d "$BACKUP_DIR" ]; then
    why="不是文件夹"
  elif [ ! -w "$BACKUP_DIR" ]; then
    why="不能写"
  elif [ "$DRY" != 1 ]; then
    probe="$(mktemp "$BACKUP_DIR/.ks-deploy-probe.XXXXXX" 2>/dev/null)" && rm -f "$probe" || why="写不进去"
  fi
  if [ -n "$why" ]; then
    log "备份目录不可用：$BACKUP_DIR ${why}（来自 ${BACKUP_DIR_FROM}）"
    log "部署没有改任何东西。挂上硬盘或改好 ${BACKUP_DIR_FROM} 后重跑；不会改写到默认目录 $KS_HOME/backups"
    trap - ERR
    exit 1
  fi
  log "备份目录：${BACKUP_DIR}（来自 ${BACKUP_DIR_FROM}）"
else
  log "备份目录：${BACKUP_DIR}（默认）"
fi

# 1. 确定构建目录
STEP="确定构建目录"
BUILD="$REPO"
if [ -n "$TARGET" ]; then
  if [ "$DRY" = 1 ]; then
    log "[dry-run] 在临时 worktree 里构建 $TARGET"
  else
    WT="$(mktemp -d "${TMPDIR:-/tmp}/ks-deploy.XXXXXX")"
    git -C "$REPO" worktree add --detach "$WT" "$TARGET" >/dev/null
    BUILD="$WT"
  fi
fi
if [ "$DRY" = 1 ] && [ -n "$TARGET" ]; then
  WANT="$(git -C "$REPO" rev-parse --short "$TARGET")"
else
  WANT="$(git -C "$BUILD" rev-parse --short HEAD)"
fi
log "目标提交：${WANT}；构建目录：${BUILD}；安装路径：${KS_BIN}；Hub：$KS_HUB_URL"

# 2. 构建
STEP="删除旧 bin/ks"
run rm -f "$BUILD/bin/ks" "$BUILD/bin/ks-launcher"
STEP="构建前端（pnpm install --frozen-lockfile && pnpm build）"
if [ "$DRY" = 1 ]; then
  log "[dry-run] cd $BUILD/web && pnpm install --frozen-lockfile && pnpm build"
else
  (cd "$BUILD/web" && pnpm install --frozen-lockfile && pnpm build)
fi
STEP="构建后端（make build）"
if [ "$DRY" = 1 ]; then
  log "[dry-run] make -C $BUILD build"
else
  make -C "$BUILD" build
fi
STEP="核对新构建的提交号"
if [ "$DRY" = 1 ]; then
  log "[dry-run] $BUILD/bin/ks version 应包含 $WANT"
else
  built="$("$BUILD/bin/ks" version)"
  log "新构建：$built"
  case "$built" in
    *"$WANT"*) ;;
    *) log "新构建的提交号不是 $WANT"; false ;;
  esac
fi

# 启动器（R-354）：没有就装，有就只核对、绝不替换；Hub 已经经它运行而它没了时在替换程序之前停下
STEP="安装 / 核对启动器 $KS_LAUNCHER"
if [ "$(uname -s)" = Darwin ]; then
  if [ "$DRY" = 1 ]; then
    "$REPO/scripts/install-launcher.sh" --dry-run "$BUILD/bin/ks-launcher" "$KS_LAUNCHER" "$KS_HUB_PLIST"
  else
    "$REPO/scripts/install-launcher.sh" "$BUILD/bin/ks-launcher" "$KS_LAUNCHER" "$KS_HUB_PLIST"
  fi
else
  log "不是 macOS，跳过启动器"
fi

# 3. 备份
STEP="备份旧程序和数据库"
run mkdir -p "$BACKUP_DIR" "$(dirname "$KS_BIN")"
if [ -f "$KS_BIN" ]; then
  HAD_OLD=1
  run cp "$KS_BIN" "$ROLLBACK_BIN"
  log "旧程序备份：$ROLLBACK_BIN"
fi
if [ -f "$KS_HOME/hub.db" ]; then
  run sqlite3 "$KS_HOME/hub.db" ".backup '$DB_BACKUP'"
  log "数据库备份：$DB_BACKUP"
else
  log "没有 $KS_HOME/hub.db，跳过数据库备份"
fi

# 备份保留策略：只清备份目录 $BACKUP_DIR，ks-*、hub-*.db、dsh-plugin-* 各保留最近 KS_KEEP_BACKUPS 份（文件名带时间，按名字排序）
STEP="清理旧备份"
prune_backups() {
  local pattern="$1" f
  [ -d "$BACKUP_DIR" ] || return 0
  # 一份都没有时 grep 不匹配会以 1 退出，pipefail 下会让部署中断（例如第一次部署插件副本时还没有 dsh-plugin-*）
  # 备份文件名由本脚本生成（名字 + 时间戳），没有特殊字符，用 ls 列名即可
  # shellcheck disable=SC2010,SC2012
  ls -1 "$BACKUP_DIR" | { grep -E "$pattern" || true; } | sort -r | tail -n +"$((KS_KEEP_BACKUPS + 1))" | while IFS= read -r f; do
    if [ "$DRY" = 1 ]; then
      log "[dry-run] 删除旧备份 $BACKUP_DIR/$f"
    else
      rm -rf "${BACKUP_DIR:?}/$f"
      log "删除旧备份 $BACKUP_DIR/$f"
    fi
  done
}
prune_backups '^ks-[0-9]'
prune_backups '^hub-[0-9].*\.db$'
prune_backups '^dsh-plugin-[0-9]'

# 4. 替换并重启（macOS 直接覆盖正在运行的二进制会被 SIGKILL，所以先 rm 再 cp）
STEP="替换 $KS_BIN"
if [ "$DRY" = 1 ]; then
  log "[dry-run] rm $KS_BIN && cp $BUILD/bin/ks $KS_BIN"
else
  REPLACED=1
  rm -f "$KS_BIN"
  cp "$BUILD/bin/ks" "$KS_BIN"
  chmod +x "$KS_BIN"
fi
# DSH 插件副本：先在旁边放好新的，再把旧的挪进备份、新的换上（R-251）
STEP="更新 DSH 插件副本 $PLUGIN_DIR"
if [ ! -f "$BUILD/dsh-plugin/index.js" ]; then
  log "构建目录里没有 dsh-plugin，跳过插件副本"
elif [ "$DRY" = 1 ]; then
  log "[dry-run] 复制 $BUILD/dsh-plugin（index.js、lib、package.json、README.md）-> ${PLUGIN_DIR}；原有的挪到 $PLUGIN_BACKUP"
else
  rm -rf "$PLUGIN_DIR.new"
  mkdir -p "$PLUGIN_DIR.new"
  cp -R "$BUILD/dsh-plugin/index.js" "$BUILD/dsh-plugin/lib" "$BUILD/dsh-plugin/package.json" "$BUILD/dsh-plugin/README.md" "$PLUGIN_DIR.new/"
  if [ -d "$PLUGIN_DIR" ]; then
    mv "$PLUGIN_DIR" "$PLUGIN_BACKUP"
    HAD_PLUGIN=1
    log "旧插件副本备份：$PLUGIN_BACKUP"
  fi
  PLUGIN_REPLACED=1
  mv "$PLUGIN_DIR.new" "$PLUGIN_DIR"
  log "DSH 插件副本：$PLUGIN_DIR"
fi
STEP="重启 hub 和 daemon"
restart_services

# 5. 核对
if [ "$DRY" = 1 ]; then
  log "[dry-run] 核对 ks version 含 ${WANT}；首页 assets/index-*.js 返回 200 且体积大于 0；ks status 显示 connected；失败则回滚并非零退出"
  exit 0
fi
verify "${KS_EXPECT_COMMIT:-$WANT}"
log "部署成功：${WANT}（旧程序留在 ${ROLLBACK_BIN}，数据库备份 ${DB_BACKUP}）"
