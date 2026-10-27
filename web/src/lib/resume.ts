// 「复制恢复命令」的拼接（R-240）。非默认 Claude 配置目录的会话，默认的 claude --resume 打不开或会用错账号：
// 配了启动命令用启动命令，没配就带上 CLAUDE_CONFIG_DIR。

type ResumeSession = { tool: string; session_id: string; profile?: string; profile_dir?: string; launcher?: string }

const shellQuote = (s: string) => (/^[A-Za-z0-9_./~-]+$/.test(s) ? s : `'${s.replace(/'/g, `'\\''`)}'`)

export function resumeCommand(s: ResumeSession): string {
  // DSH 的会话在 Host 里，从 Web / 桌面端的会话列表打开，没有按 session id 恢复的稳定 CLI；返回空串让菜单隐藏这一项。
  if (s.tool === 'dsh') return ''
  if (s.tool === 'codex') return `codex resume ${s.session_id}`
  if (s.launcher) return `${s.launcher} --resume ${s.session_id}`
  if (s.profile_dir) return `CLAUDE_CONFIG_DIR=${shellQuote(s.profile_dir)} claude --resume ${s.session_id}`
  return `claude --resume ${s.session_id}`
}

// 没有启动命令、靠 CLAUDE_CONFIG_DIR 恢复时，菜单上要提示：这个目录里得已有登录凭证。
export const resumeNeedsLogin = (s: ResumeSession): boolean => s.tool !== 'codex' && s.tool !== 'dsh' && !s.launcher && !!s.profile_dir
