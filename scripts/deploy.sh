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
#   KS_KEEP_BACKUPS   $KS_HOME/backups 里 ks-* 和 hub-*.db 各保留最近几份，默认 10
#   KS_VERIFY_TIMEOUT 部署后等待服务就绪的秒数，默认 30
set -euo pipefail

DRY=0
TARGET=""
for a in "$@"; do
  case "$a" in
    --dry-run) DRY=1 ;;
    -h | --help) sed -n '2,17p' "$0"; exit 0 ;;
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
KS_KEEP_BACKUPS="${KS_KEEP_BACKUPS:-10}"
export KS_HOME

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TS="$(date +%Y%m%d-%H%M%S)"
BACKUP_DIR="$KS_HOME/backups"
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
run rm -f "$BUILD/bin/ks"
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

# 备份保留策略：只清 $KS_HOME/backups，ks-* 和 hub-*.db 各保留最近 KS_KEEP_BACKUPS 份（文件名带时间，按名字排序）
STEP="清理旧备份"
prune_backups() {
  local pattern="$1" f
  [ -d "$BACKUP_DIR" ] || return 0
  # shellcheck disable=SC2010
  # 一份都没有时 grep 不匹配会以 1 退出，pipefail 下会让部署中断（例如第一次部署插件副本时还没有 dsh-plugin-*）
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
