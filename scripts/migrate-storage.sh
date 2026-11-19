#!/usr/bin/env bash
# 纸鸢存储搬迁（R-354）：把附件目录或部署备份目录完整拷贝到新位置（如外置硬盘），核对文件数和 sha256，
# 写下核对记录；核对通过才提示下一步。删除旧副本是单独一步，要显式写 --confirm-delete-old。
#
# 用法（每次处理一个目录；附件和备份各跑一遍）：
#   scripts/migrate-storage.sh copy       <旧目录> <新目录> [--dry-run]
#       拷贝旧目录的全部内容到新目录，然后核对。新目录可以已存在；不存在时只建这一层，上一层（如外置硬盘的挂载点）必须已存在。
#   scripts/migrate-storage.sh verify     <旧目录> <新目录>
#       只核对：旧目录里每个文件在新目录里都有、sha256 一致才算通过。新目录多出来的文件（切换后新传的附件、新的部署备份）列出来但不算不通过。
#   scripts/migrate-storage.sh delete-old <旧目录> <新目录> --confirm-delete-old [--dry-run]
#       先重新核对，通过了才删除旧目录；不写 --confirm-delete-old 不删。
#
# 核对记录写到 $KS_HOME/migrations/（KS_HOME 默认 ~/.kitestring）：<时间>-<目录名>.txt 是结果摘要，
# 同名的 .src.sha256 / .dst.sha256 是两边逐个文件的清单。退出码：0 通过，1 核对不通过或出错，2 参数不对。
#
# 搬迁顺序见 docs/install.zh-CN.md「附件目录」：拷贝并核对 -> 切换配置（ks install --hub --hub-files-dir、$KS_HOME/backup-dir）
# -> 重启后确认附件能看 -> 再跑一次 copy 补上期间新增的文件 -> delete-old。
set -euo pipefail

usage() { sed -n '2,17p' "$0"; }

DRY=0
CONFIRM=0
ARGS=()
for a in "$@"; do
  case "$a" in
    --dry-run) DRY=1 ;;
    --confirm-delete-old) CONFIRM=1 ;;
    -h | --help) usage; exit 0 ;;
    -*) echo "未知参数：$a" >&2; exit 2 ;;
    *) ARGS+=("$a") ;;
  esac
done
if [ "${#ARGS[@]}" -ne 3 ]; then
  usage >&2
  exit 2
fi
CMD="${ARGS[0]}"
SRC="${ARGS[1]%/}"
DST="${ARGS[2]%/}"
case "$CMD" in
  copy | verify | delete-old) ;;
  *) echo "未知操作：${CMD}（只有 copy、verify、delete-old）" >&2; exit 2 ;;
esac

KS_HOME="${KS_HOME:-$HOME/.kitestring}"
REC_DIR="$KS_HOME/migrations"
TS="$(date +%Y%m%d-%H%M%S)"

log() { printf '[migrate] %s\n' "$*"; }
die() { log "$*"; exit 1; }

# 绝对路径（目录不存在时按上一层算）
abspath() {
  local d="$1"
  if [ -d "$d" ]; then
    (cd "$d" && pwd -P)
  else
    local parent
    if ! parent="$(cd "$(dirname "$d")" 2>/dev/null && pwd -P)"; then
      parent="$(dirname "$d")"
    fi
    printf '%s/%s\n' "$parent" "$(basename "$d")"
  fi
}

[ -d "$SRC" ] || die "旧目录不存在：$SRC"
SRC_ABS="$(abspath "$SRC")"
DST_ABS="$(abspath "$DST")"
[ "$SRC_ABS" != "$DST_ABS" ] || die "新旧目录是同一个：$SRC_ABS"
case "$DST_ABS/" in "$SRC_ABS/"*) die "新目录不能在旧目录里面：$DST_ABS" ;; esac
case "$SRC_ABS/" in "$DST_ABS/"*) die "旧目录不能在新目录里面：$SRC_ABS" ;; esac

if command -v shasum >/dev/null 2>&1; then
  SHA=(shasum -a 256)
elif command -v sha256sum >/dev/null 2>&1; then
  SHA=(sha256sum)
else
  die "找不到 shasum 或 sha256sum"
fi

# manifest <目录> <输出>：每个普通文件一行「sha256  ./相对路径」，按路径排序
manifest() {
  : >"$2"
  # 空目录时不跑 sha256（没有参数的 shasum 会去读标准输入）
  [ -n "$(cd "$1" && find . -type f | head -n1)" ] || return 0
  (cd "$1" && find . -type f -print0 | LC_ALL=C sort -z | xargs -0 "${SHA[@]}") >"$2"
}

# verify：核对 SRC ⊆ DST（同路径、同 sha256），写记录；通过返回 0
verify() {
  [ -d "$DST" ] || { log "新目录不存在：$DST"; return 1; }
  mkdir -p "$REC_DIR"
  local base rec srcm dstm
  base="$REC_DIR/$TS-$(basename "$SRC_ABS")"
  rec="$base.txt"
  srcm="$base.src.sha256"
  dstm="$base.dst.sha256"
  log "计算 sha256：$SRC_ABS"
  manifest "$SRC" "$srcm" || { log "读旧目录失败"; return 1; }
  log "计算 sha256：$DST_ABS"
  manifest "$DST" "$dstm" || { log "读新目录失败"; return 1; }
  local nsrc ndst
  nsrc="$(wc -l <"$srcm" | tr -d ' ')"
  ndst="$(wc -l <"$dstm" | tr -d ' ')"
  # 每行前 64 位是 sha256，隔两个字符后是路径
  local diff
  diff="$(awk '
    NR == FNR { d[substr($0, 67)] = substr($0, 1, 64); next }
    {
      p = substr($0, 67); h = substr($0, 1, 64)
      if (!(p in d)) { print "缺少\t" p; miss++ }
      else if (d[p] != h) { print "不一致\t" p; bad++ }
      else ok++
      seen[p] = 1
    }
    END {
      for (p in d) if (!(p in seen)) extra++
      printf "#\t%d\t%d\t%d\t%d\n", ok + 0, miss + 0, bad + 0, extra + 0
    }' "$dstm" "$srcm")"
  local summary ok miss bad extra
  summary="$(printf '%s\n' "$diff" | grep '^#' | tail -n1)"
  ok="$(printf '%s' "$summary" | cut -f2)"
  miss="$(printf '%s' "$summary" | cut -f3)"
  bad="$(printf '%s' "$summary" | cut -f4)"
  extra="$(printf '%s' "$summary" | cut -f5)"
  local result="通过"
  if [ "$miss" != 0 ] || [ "$bad" != 0 ] || [ "$ok" != "$nsrc" ]; then
    result="不通过"
  fi
  {
    echo "纸鸢存储搬迁核对记录（R-354）"
    echo "时间：$(date '+%Y-%m-%d %H:%M:%S')"
    echo "旧目录：$SRC_ABS"
    echo "新目录：$DST_ABS"
    echo "旧目录文件数：${nsrc}，大小：$(du -sk "$SRC" | cut -f1) KB"
    echo "新目录文件数：${ndst}，大小：$(du -sk "$DST" | cut -f1) KB"
    echo "sha256 一致：${ok}，缺少：${miss}，不一致：${bad}，新目录多出：$extra"
    echo "旧目录清单 sha256：$("${SHA[@]}" <"$srcm" | cut -c1-64)"
    echo "结果：$result"
    if [ "$result" != "通过" ]; then
      echo
      echo "有问题的文件："
      printf '%s\n' "$diff" | grep -v '^#' | head -n 200
    fi
  } >"$rec"
  cat "$rec"
  log "核对记录：${rec}（清单：${srcm}、${dstm}）"
  [ "$result" = "通过" ]
}

case "$CMD" in
  copy)
    if [ ! -d "$DST" ]; then
      parent="$(dirname "$DST")"
      [ -d "$parent" ] || die "新目录的上一层不存在：${parent}（外置硬盘没挂上？）。不替你创建，免得写到系统盘"
      if [ "$DRY" = 1 ]; then
        log "[dry-run] mkdir $DST"
      else
        mkdir "$DST"
      fi
    fi
    [ "$DRY" = 1 ] || [ -w "$DST" ] || die "新目录不能写：$DST"
    nfiles="$(cd "$SRC" && find . -type f | wc -l | tr -d ' ')"
    log "拷贝 $SRC_ABS -> ${DST_ABS}（$nfiles 个文件，$(du -sh "$SRC" | cut -f1)）"
    if [ "$DRY" = 1 ]; then
      log "[dry-run] cp -Rp $SRC_ABS/. $DST_ABS/"
      log "[dry-run] 然后核对文件数和 sha256，写记录到 $REC_DIR/"
      exit 0
    fi
    cp -Rp "$SRC/." "$DST/"
    if verify; then
      log "核对通过。下一步：切换配置（附件：ks install --hub ... --hub-files-dir ${DST_ABS}；备份：把 $DST_ABS 写进 $KS_HOME/backup-dir），重启后确认能用，再跑 delete-old"
    else
      die "核对不通过，不要切换配置；查看记录里的文件后重跑 copy"
    fi
    ;;
  verify)
    verify || exit 1
    ;;
  delete-old)
    if [ "$CONFIRM" != 1 ]; then
      die "删除旧目录要显式写 --confirm-delete-old；先确认已切换到新目录、附件能正常打开"
    fi
    verify || die "核对不通过，不删除旧目录 $SRC_ABS"
    if [ "$DRY" = 1 ]; then
      log "[dry-run] rm -rf $SRC_ABS"
      exit 0
    fi
    rm -rf "${SRC_ABS:?}"
    log "已删除旧目录 $SRC_ABS"
    ;;
esac
