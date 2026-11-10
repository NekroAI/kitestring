// 「接入设备」指引里的命令（R-268）：按平台和勾选的工具拼出能直接复制的完整命令。
// 以 scripts/install.sh 与 internal/install 的实际行为为准，改那边时同步这里。

export type Platform = 'mac' | 'linux' | 'windows'
export type Tool = 'claude' | 'codex' | 'dsh'

export const INSTALL_URL = 'https://raw.githubusercontent.com/NekroAI/kitestring/main/scripts/install.sh'
export const INSTALL_PS_URL = 'https://raw.githubusercontent.com/NekroAI/kitestring/main/scripts/install.ps1'
export const DOCS_URL = 'https://github.com/NekroAI/kitestring/blob/main/docs/install.zh-CN.md#3-每台开发设备'
export const DSH_VERSION = '0.2.1-rc.4'

// 安装 ks：install.sh 装到 ~/.local/bin，不改 shell 配置，所以这里把 PATH 一并写进去。
// Windows 在 PowerShell 里运行 install.ps1：装到 ~\.local\bin，自己写进用户的 PATH 和当前窗口的 $env:Path。
export function installCmd(p: Platform): string {
  if (p === 'windows') return `irm ${INSTALL_PS_URL} | iex`
  const rc = p === 'mac' ? '~/.zshrc' : '~/.bashrc'
  return [`curl -fsSL ${INSTALL_URL} | sh`, `echo 'export PATH="$HOME/.local/bin:$PATH"' >> ${rc}`, 'export PATH="$HOME/.local/bin:$PATH"'].join('\n')
}

// 令牌只在创建时显示一次；重开指引时没有令牌，写占位。
export const TOKEN_PLACEHOLDER = '<创建时复制的令牌>'

export function loginCmd(hub: string, token: string | null): string {
  return `ks login --hub ${hub} --token ${token || TOKEN_PLACEHOLDER}`
}

// ks install 默认接入 Claude Code 和 Codex 并注册 daemon 服务；没勾的工具显式关掉，
// 免得没装 claude 时 MCP 注册报错。DSH 不走 ks install。
export function toolsCmd(tools: readonly Tool[]): string {
  const flags: string[] = []
  if (!tools.includes('claude')) flags.push('--claude=false')
  if (!tools.includes('codex')) flags.push('--codex=false')
  return ['ks install', ...flags].join(' ')
}

// systemd 用户服务默认随登录启停；服务器上要开机就启动、退出登录也不停，需要 linger。
export const LINGER_CMD = 'sudo loginctl enable-linger "$USER"'

// 没有 systemd（Docker 容器等）时手动起 daemon。
export const NOHUP_CMD = 'nohup ks daemon >> ~/.kitestring/daemon.log 2>&1 &'

// DSH 插件不在发布包里：按本机 ks 的版本取同一标签的源码，放到 ~/.kitestring/dsh-plugin 再启用。
// 新建的 DSH profile 里 cordis.patch.yml 是一行 []，0.2.0 的启用脚本会把记录接在它后面写坏文件，
// 所以先删掉这一行（修好的脚本已自己处理，这行之后多余但无害；sed -i.bak 在 macOS 和 Linux 上写法一致）。
export function dshCmd(): string {
  return [
    `v=$(ks version | awk '{print $2}')`,
    'git clone --depth 1 --branch "v$v" https://github.com/NekroAI/kitestring.git ~/.kitestring/src',
    'mkdir -p ~/.kitestring/dsh-plugin && cp -R ~/.kitestring/src/dsh-plugin/. ~/.kitestring/dsh-plugin/',
    "sed -i.bak '/^\\[\\]$/d' ~/.dsh/profiles/web/cordis.patch.yml",
    'bash ~/.kitestring/src/scripts/install-dsh-plugin.sh --profile web',
  ].join('\n')
}

export const STATUS_CMD = 'ks status'

export function logHint(p: Platform): string {
  if (p === 'windows') return '~\\.kitestring\\logs\\daemon.log'
  return p === 'mac' ? '~/.kitestring/logs/daemon.log' : 'journalctl --user -u kitestring-daemon -n 50'
}

// 本机回环地址：别的设备用这个地址连不上 Hub。
export function isLoopback(hostname: string): boolean {
  const h = hostname.toLowerCase().replace(/^\[|\]$/g, '')
  return h === 'localhost' || h.endsWith('.localhost') || h === '::1' || /^127\./.test(h) || h === '0.0.0.0'
}

export function defaultPlatform(ua: string): Platform {
  if (/Windows/i.test(ua)) return 'windows'
  return /Mac|iPhone|iPad/i.test(ua) ? 'mac' : 'linux'
}

// DSH 插件的接入命令依赖 bash 和 git 源码目录，目前只给 macOS / Linux。
export const dshSupported = (p: Platform): boolean => p !== 'windows'
