#!/usr/bin/env bash
# 把 kitestring-dsh 装进一个 DSH profile：链接插件包，并在 profile 的 cordis.patch.yml 里插一条启用记录。
# 用法：scripts/install-dsh-plugin.sh [--profile web] [--dry-run]
# 重复运行是安全的：已装过就只报告，不会重复追加。
set -euo pipefail

PROFILE=web
DRY=0
while [ $# -gt 0 ]; do
  case "$1" in
    --profile)
      PROFILE="${2:?--profile 后面要写 profile 名}"
      shift 2
      ;;
    --dry-run)
      DRY=1
      shift
      ;;
    -h | --help)
      sed -n '2,4p' "$0"
      exit 0
      ;;
    *)
      echo "未知参数：$1" >&2
      exit 2
      ;;
  esac
done

DSH_HOME="${DSH_HOME:-$HOME/.dsh}"
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PLUGIN="$REPO/dsh-plugin"
PATCH="$DSH_HOME/profiles/$PROFILE/cordis.patch.yml"

[ -d "$PLUGIN" ] || { echo "找不到插件目录：$PLUGIN" >&2; exit 1; }
[ -f "$PATCH" ] || { echo "profile 还没初始化：$PATCH 不存在。先运行一次 dsh --profile $PROFILE" >&2; exit 1; }

run() { if [ "$DRY" = 1 ]; then printf '[dry-run] %s\n' "$*"; else "$@"; fi; }

echo "[install] profile=$PROFILE plugin=$PLUGIN"
run dsh plugin --profile "$PROFILE" add "link:$PLUGIN"

if grep -q 'name: kitestring-dsh' "$PATCH"; then
  echo "[install] $PATCH 已有 kitestring-dsh，跳过"
else
  cp "$PATCH" "$PATCH.bak-$(date +%Y%m%d-%H%M%S)"
  if [ "$DRY" = 1 ]; then
    echo "[dry-run] 追加到 $PATCH："
    printf '%s\n' "- insert:" "    - id: kitestring" "      name: kitestring-dsh" "      config:" "        standby: true"
  else
    cat >>"$PATCH" <<'YAML'
- insert:
    - id: kitestring
      name: kitestring-dsh
      config:
        standby: true
YAML
    echo "[install] 已写入 $PATCH"
  fi
fi

echo
echo "下一步：web profile 开着 HMR，几秒内自动生效；否则重启 dsh --profile ${PROFILE}。"
echo "看到 /tmp/kitestring-dsh.log（debug: true 时）或纸鸢会话墙出现 DSH 会话，就说明接上了。"
