#!/bin/sh
# 从 GitHub Release 安装 ks：判断系统与架构、下载、校验 SHA256、装到固定目录。
# 用法：curl -fsSL https://raw.githubusercontent.com/NekroAI/kitestring/main/scripts/install.sh | sh
# 环境变量：KS_VERSION（如 v0.1.0，默认最新版）、KS_INSTALL_DIR（默认 ~/.local/bin）。
# 重跑即升级；ks install 写入的配置引用的是安装目录里的绝对路径，升级后重启常驻服务即可。
set -eu

REPO="NekroAI/kitestring"
DIR="${KS_INSTALL_DIR:-$HOME/.local/bin}"

fail() { echo "ks install: $*" >&2; exit 1; }

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) fail "暂不支持 $(uname -s)，目前只提供 macOS 与 Linux 版本" ;;
esac
case "$(uname -m)" in
  arm64 | aarch64) arch=arm64 ;;
  x86_64 | amd64) arch=amd64 ;;
  *) fail "暂不支持 $(uname -m) 架构" ;;
esac

command -v curl >/dev/null 2>&1 || fail "需要 curl"
if command -v sha256sum >/dev/null 2>&1; then
  sha() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  sha() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
  fail "需要 sha256sum 或 shasum 校验下载文件"
fi

# 最新版从 releases/latest 的跳转地址里取，不走 API，避免匿名请求被限流。
tag="${KS_VERSION:-}"
if [ -z "$tag" ]; then
  url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest") ||
    fail "查询最新版本失败"
  tag="${url##*/}"
  case "$tag" in v*) ;; *) fail "还没有发布版本（${url}）" ;; esac
fi
v="${tag#v}"

name="kitestring-$v-$os-$arch.tar.gz"
base="https://github.com/$REPO/releases/download/$tag"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "下载 ks ${tag}（${os}-${arch}）…"
curl -fSL --progress-bar -o "$tmp/$name" "$base/$name" || fail "下载 $name 失败"
curl -fsSL -o "$tmp/SHA256SUMS.txt" "$base/SHA256SUMS.txt" || fail "下载 SHA256SUMS.txt 失败"

want=$(grep " $name\$" "$tmp/SHA256SUMS.txt" | cut -d' ' -f1)
[ -n "$want" ] || fail "SHA256SUMS.txt 里没有 $name"
[ "$(sha "$tmp/$name")" = "$want" ] || fail "$name 校验失败，文件可能不完整"

tar -xzf "$tmp/$name" -C "$tmp"
mkdir -p "$DIR"
# 先写临时文件再改名：正在运行的 daemon 不受影响，也不会留下半个文件。
cp "$tmp/ks" "$DIR/.ks.new"
chmod 755 "$DIR/.ks.new"
mv -f "$DIR/.ks.new" "$DIR/ks"

echo "✓ 已安装 $("$DIR/ks" version) → $DIR/ks"
case ":$PATH:" in
  *":$DIR:"*) ;;
  *) echo "  $DIR 不在 PATH 中，请把它加入 shell 配置，例如：export PATH=\"$DIR:\$PATH\"" ;;
esac
cat <<EOF

接下来：
  1. 在 Kitestring 网页「设置 → 设备」中新增设备，复制 token
  2. ks login --hub http://<hub 地址>:7310 --token kst_xxx
  3. ks install            # 写入 Claude Code / Codex 的 hook、MCP 与 daemon 常驻服务（--dry-run 先预览）
EOF
if [ "$os" = darwin ]; then
  echo "已接入过的设备：再次运行 ks install，即用新版本重启常驻服务"
else
  echo "已接入过的设备：systemctl --user restart kitestring-daemon（Hub 所在设备再重启 kitestring-hub）"
fi
