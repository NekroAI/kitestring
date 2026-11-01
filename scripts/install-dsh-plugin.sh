#!/usr/bin/env bash
# 把 kitestring-dsh 装进一个 DSH profile：链接插件包，并在 profile 的 cordis.patch.yml 里插一条启用记录。
# 用法：scripts/install-dsh-plugin.sh [--profile web] [--dev] [--dry-run]
#   默认链接 $KS_HOME/dsh-plugin（scripts/deploy.sh 部署时放的稳定副本），在用的 Host 只随部署更新；
#   --dev 改为链接仓库工作区的 dsh-plugin/（改代码会被热加载进 Host，只用于开发调试）。
#   --dry-run 只打印要做的事，不改任何文件（也不写备份）。
# 重复运行是安全的：已装过就只报告，不会重复追加。
set -euo pipefail

PROFILE=web
DRY=0
DEV=0
while [ $# -gt 0 ]; do
  case "$1" in
    --profile)
      PROFILE="${2:?--profile 后面要写 profile 名}"
      shift 2
      ;;
    --dev)
      DEV=1
      shift
      ;;
    --dry-run)
      DRY=1
      shift
      ;;
    -h | --help)
      sed -n '2,7p' "$0"
      exit 0
      ;;
    *)
      echo "未知参数：$1" >&2
      exit 2
      ;;
  esac
done

DSH_HOME="${DSH_HOME:-$HOME/.dsh}"
KS_HOME="${KS_HOME:-$HOME/.kitestring}"
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if [ "$DEV" = 1 ]; then
  PLUGIN="$REPO/dsh-plugin"
else
  PLUGIN="$KS_HOME/dsh-plugin"
fi
PATCH="$DSH_HOME/profiles/$PROFILE/cordis.patch.yml"

if [ ! -f "$PLUGIN/index.js" ]; then
  if [ "$DEV" = 1 ]; then
    echo "找不到插件目录：$PLUGIN" >&2
  else
    echo "还没有插件副本：${PLUGIN}。先运行 scripts/deploy.sh（或 make deploy）部署一次；开发调试可加 --dev 直接链接仓库。" >&2
  fi
  exit 1
fi
[ -f "$PATCH" ] || { echo "profile 还没初始化：$PATCH 不存在。先运行一次 dsh --profile $PROFILE" >&2; exit 1; }

run() { if [ "$DRY" = 1 ]; then printf '[dry-run] %s\n' "$*"; else "$@"; fi; }

echo "[install] profile=$PROFILE plugin=$PLUGIN"
[ "$DEV" = 1 ] && echo "[install] --dev：链接的是仓库工作区，保存代码就会热加载进这个 profile 的 Host"
run dsh plugin --profile "$PROFILE" add "link:$PLUGIN"

if grep -q 'name: kitestring-dsh' "$PATCH"; then
  echo "[install] $PATCH 已有 kitestring-dsh，跳过"
else
  ENTRY='- insert:
    - id: kitestring
      name: kitestring-dsh
      config:
        standby: true'
  # 原文件最后一行没有换行时先补一个，免得插入的记录粘在上一行末尾
  NEED_NL=0
  if [ -s "$PATCH" ] && [ "$(tail -c 1 "$PATCH" | od -An -c | tr -d ' ')" != '\n' ]; then
    NEED_NL=1
  fi
  # 新建的 profile 里是一行空数组 []，直接追加会变成非法 YAML（DSH 启动时报解析错误），先去掉它
  EMPTY=0
  grep -qx '[[:space:]]*\[\][[:space:]]*' "$PATCH" && EMPTY=1
  if [ "$DRY" = 1 ]; then
    echo "[dry-run] 备份 $PATCH -> $PATCH.bak-<时间>"
    [ "$EMPTY" = 1 ] && echo "[dry-run] 去掉空数组 []"
    [ "$NEED_NL" = 1 ] && echo "[dry-run] 原文件末尾没有换行，先补一个"
    echo "[dry-run] 追加到 ${PATCH}："
    printf '%s\n' "$ENTRY"
  else
    cp "$PATCH" "$PATCH.bak-$(date +%Y%m%d-%H%M%S)"
    if [ "$EMPTY" = 1 ]; then
      grep -vx '[[:space:]]*\[\][[:space:]]*' "$PATCH" >"$PATCH.tmp" || true
      mv "$PATCH.tmp" "$PATCH"
    fi
    [ "$NEED_NL" = 1 ] && [ "$EMPTY" = 0 ] && printf '\n' >>"$PATCH"
    printf '%s\n' "$ENTRY" >>"$PATCH"
    echo "[install] 已写入 $PATCH"
  fi
fi

echo
echo "下一步：web profile 开着 HMR，几秒内自动生效；否则重启 dsh --profile ${PROFILE}。"
echo "纸鸢会话墙出现 DSH 会话就说明接上了；排障时在插件配置里打开 debug: true，看 $KS_HOME/logs/dsh-plugin.log。"
