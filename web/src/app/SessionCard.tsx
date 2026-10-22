import { memo, type ReactNode } from "react";
import {
  Bot,
  CheckCircle2,
  Hourglass,
  Eye,
  FileCode2,
  FileText,
  FolderGit2,
  GitBranch,
  Globe,
  HelpCircle,
  Layers,
  ListChecks,
  MessageSquare,
  Monitor,
  Plug,
  Search,
  ShieldAlert,
  ShieldOff,
  Terminal,
  Wrench,
  type LucideIcon,
} from "lucide-react";
import type { Decision, Session } from "../api/client";
import { t } from "../i18n";
import { cx, relTime, shortPath } from "../lib/util";
import { Card, Tip } from "../ui/base";
import {
  displayState,
  PhaseBadge,
  Sparkline,
  StatusPill,
  ToolMark,
  stateStyle,
} from "../ui/marks";
import { DecisionComposer } from "./DecisionCard";
import { useOpenReq, useOpenSession } from "./nav";

// 工具名 → 图标。
export function toolIcon(label: string): LucideIcon {
  const name = label.split(" · ")[0];
  if (/^(Bash|shell|exec_command|local_shell)$/.test(name)) return Terminal;
  if (/^(Edit|Write|MultiEdit|NotebookEdit|apply_patch)$/.test(name))
    return FileCode2;
  if (name === "Read") return FileText;
  if (/^(Grep|Glob)$/.test(name)) return Search;
  if (/^Web/.test(name)) return Globe;
  if (/^(Task|Agent)$/.test(name)) return Bot;
  if (name === "AskUserQuestion") return HelpCircle;
  if (name === "ExitPlanMode") return ListChecks;
  if (name.includes("__")) return Plug;
  return Wrench;
}

// 拆分“工具 · 简述”。
function splitTool(label: string): [string, string] {
  const [name, ...rest] = label.split(" · ");
  return [name.replace(/^mcp__/, ""), rest.join(" · ")];
}

export function sessionTitle(s: Session): string {
  return (
    s.title || s.project_name || shortPath(s.cwd, 28) || t.session.noProject
  );
}

function Meta({
  icon: Icon,
  tip,
  children,
  className,
}: {
  icon: LucideIcon;
  tip: string;
  children: ReactNode;
  className?: string;
}) {
  return (
    <Tip content={tip}>
      <span className={cx("inline-flex min-w-0 items-center gap-1", className)}>
        <Icon className="size-3.5 shrink-0" />
        <span className="truncate">{children}</span>
      </span>
    </Tip>
  );
}

// 等你处理时的提示块：在问你 / 请你确认计划 / 请求执行 / 回复要点。
export function Callout({ s }: { s: Session }) {
  const ds = displayState(s);
  if (ds === "waiting_approval") {
    return (
      <div className="flex items-start gap-2.5 rounded-xl bg-wait-soft px-3 py-2.5">
        <ShieldAlert className="mt-0.5 size-4 shrink-0 text-wait" />
        <div className="min-w-0 flex-1">
          <div className="font-mono text-[12px] leading-relaxed break-all text-fg">
            {s.state_detail}
          </div>
          <div className="mt-0.5 text-xs text-fg-3">
            {t.session.approvalHint}
          </div>
        </div>
      </div>
    );
  }
  if (ds === "waiting_answer") {
    const [name, brief] = splitTool(s.state_detail);
    const plan = name === "ExitPlanMode";
    const Icon = plan ? ListChecks : HelpCircle;
    return (
      <div className="flex items-start gap-2.5 rounded-xl bg-reply-soft px-3 py-2.5">
        <Icon className="mt-0.5 size-4 shrink-0 text-reply" />
        <div className="min-w-0 flex-1 text-sm">
          <div className="text-xs font-medium text-reply">
            {plan ? t.session.planConfirm : t.session.asking}
          </div>
          {brief && (
            <div className="mt-0.5 leading-relaxed text-fg">{brief}</div>
          )}
          <div className="mt-0.5 text-xs text-fg-3">
            {t.session.approvalHint}
          </div>
        </div>
      </div>
    );
  }
  if (ds === "background") {
    return (
      <Tip content={t.session.background}>
        <div className="flex items-center gap-2.5 rounded-xl bg-accent-soft px-3 py-2">
          <Hourglass className="size-4 shrink-0 animate-pulse text-accent" />
          <span className="min-w-0 flex-1 truncate text-[13px] text-fg">
            {s.state_detail}
          </span>
        </div>
      </Tip>
    );
  }
  if (ds === "waiting_reply" && (s.digest_done || s.digest_ask)) {
    return (
      <div className="space-y-1.5 rounded-xl bg-reply-soft px-3 py-2.5 text-sm">
        {s.digest_done && (
          <div className="flex items-start gap-2">
            <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-run" />
            <span className="leading-relaxed text-fg">{s.digest_done}</span>
          </div>
        )}
        {s.digest_ask && (
          <div className="flex items-start gap-2">
            <HelpCircle className="mt-0.5 size-4 shrink-0 text-reply" />
            <span className="leading-relaxed font-medium text-fg">
              {s.digest_ask}
            </span>
          </div>
        )}
      </div>
    );
  }
  return null;
}

function ActionLine({ s, now }: { s: Session; now: number }) {
  if (!s.last_tool || s.state === "ended") return null;
  const Icon = toolIcon(s.last_tool);
  const [name, brief] = splitTool(s.last_tool);
  const live = s.state === "running";
  return (
    <Tip content={t.session.action}>
      <div
        className={cx(
          "flex min-w-0 items-center gap-2 rounded-lg px-2.5 py-1.5 text-[13px]",
          live ? "bg-surface-2" : "bg-transparent px-0",
        )}
      >
        <Icon
          className={cx("size-4 shrink-0", live ? "text-run" : "text-fg-3")}
        />
        <span className="shrink-0 font-medium text-fg-2">{name}</span>
        {brief && (
          <span className="min-w-0 flex-1 truncate font-mono text-[12px] text-fg-3">
            {brief}
          </span>
        )}
        <span className="ml-auto shrink-0 text-xs text-fg-3 tabular">
          {relTime(s.last_tool_at, now)}
        </span>
      </div>
    </Tip>
  );
}

// SessionCard：会话卡片。
// - 普通形态：一眼看出它在做什么、做了多久、顺不顺利。
// - wide（要你处理）：附带提问 / 授权请求 / 回复要点，并就地回答待决策。
export const SessionCard = memo(function SessionCard({
  s,
  now,
  decisions = [],
  wide,
}: {
  s: Session;
  now: number;
  decisions?: Decision[];
  wide?: boolean;
}) {
  const open = useOpenSession();
  const openReq = useOpenReq();
  const ds = displayState(s);
  const st = stateStyle[ds] ?? stateStyle.idle;
  const ended = s.state === "ended";
  const perm = t.permMode[s.permission_mode];
  const reqs = s.requirements.filter(
    (r) => r.status !== "done" && r.status !== "shelved",
  );
  // 有回复要点时，要点本身就是进展，标题下只放你最后说的话，避免重复。
  const hasDigest = ds === "waiting_reply" && !!(s.digest_done || s.digest_ask);
  const useSummary = !!s.summary && !hasDigest;
  const headline = useSummary ? s.summary : s.last_prompt;
  const showPrompt = useSummary && !!s.last_prompt;

  const body = (
    <div className="flex min-w-0 flex-1 flex-col gap-3">
      {/* 标题行 */}
      <div className="flex items-start gap-3">
        <ToolMark tool={s.tool} />
        <div className="min-w-0 flex-1">
          <div className="flex min-w-0 items-center gap-2">
            <span className="truncate text-[15px] font-semibold text-fg">
              {sessionTitle(s)}
            </span>
            <PhaseBadge phase={s.phase} />
            <span className="ml-auto shrink-0 pl-1">
              <StatusPill state={ds} since={s.state_since} now={now} />
            </span>
          </div>
          <div className="mt-1 flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-0.5 text-xs text-fg-3">
            {s.title && s.project_name && (
              <Meta icon={FolderGit2} tip={s.cwd}>
                {s.project_name}
              </Meta>
            )}
            <Meta icon={Monitor} tip={t.session.device}>
              {s.device_name}
            </Meta>
            {s.branch && (
              <Meta
                icon={GitBranch}
                tip={s.dirty ? t.session.dirtyTip(s.dirty) : t.session.branch}
                className="font-mono"
              >
                {s.branch}
                {s.dirty > 0 && (
                  <span className="ml-1 font-sans font-medium text-wait tabular">
                    ±{s.dirty}
                  </span>
                )}
              </Meta>
            )}
          </div>
        </div>
      </div>

      {/* 在做什么 */}
      {headline && (
        <div className="min-w-0">
          <p
            className={cx(
              "line-clamp-2 text-sm leading-relaxed",
              useSummary ? "text-fg" : "text-fg-2",
            )}
          >
            {!useSummary && (
              <Tip content={t.session.you}>
                <MessageSquare className="mr-1.5 mb-0.5 inline size-3.5 text-fg-3" />
              </Tip>
            )}
            {headline}
          </p>
          {showPrompt && (
            <p className="mt-1 line-clamp-1 text-[13px] text-fg-3">
              <Tip content={t.session.you}>
                <MessageSquare className="mr-1.5 mb-0.5 inline size-3.5" />
              </Tip>
              {s.last_prompt}
            </p>
          )}
        </div>
      )}

      <Callout s={s} />
      {!(
        ds === "waiting_approval" ||
        ds === "waiting_answer" ||
        ds === "background"
      ) && <ActionLine s={s} now={now} />}

      {/* 底部指标 */}
      <div className="mt-auto flex items-center gap-3 border-t border-line pt-2.5 text-xs text-fg-3">
        <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-3 gap-y-1.5">
          {s.turns > 0 && (
            <Tip content={t.session.turnsTip}>
              <span className="tabular">{t.session.turns(s.turns)}</span>
            </Tip>
          )}
          {s.turn_tools > 0 && !ended && (
            <Meta
              icon={Wrench}
              tip={t.session.toolsTip(s.turn_tools)}
              className="tabular"
            >
              {s.turn_tools}
            </Meta>
          )}
          {s.subagents > 0 && (
            <Meta
              icon={Layers}
              tip={t.session.subagentsTip(s.subagents)}
              className="text-accent tabular"
            >
              {s.subagents}
            </Meta>
          )}
          {perm && (
            <Tip content={perm}>
              <span className="inline-flex" aria-label={perm}>
                {s.permission_mode === "plan" ? (
                  <Eye className="size-3.5" />
                ) : (
                  <ShieldOff className="size-3.5" />
                )}
              </span>
            </Tip>
          )}
          {s.worktree && (
            <Tip content={t.session.worktree}>
              <span className="rounded border border-line px-1 font-mono text-[10px]">
                wt
              </span>
            </Tip>
          )}
          {reqs.map((r) => (
            <Tip
              key={r.id}
              content={`${r.code} ${r.title} · ${t.reqStatus[r.status]}`}
            >
              <button
                onClick={(e) => {
                  e.stopPropagation();
                  openReq(r.code);
                }}
                className="inline-flex max-w-[200px] items-center gap-1 rounded-md bg-accent-soft px-1.5 py-0.5 text-accent hover:brightness-95"
              >
                <span className="shrink-0 font-mono text-[11px] font-semibold whitespace-nowrap">
                  {r.code}
                </span>
                <span className="truncate">{r.title}</span>
              </button>
            </Tip>
          ))}
          {!s.turns && !reqs.length && (
            <span>{relTime(s.last_active_at, now)}</span>
          )}
        </div>
        <Sparkline data={s.activity} state={ds} />
      </div>
    </div>
  );

  const clickable = {
    role: "button" as const,
    tabIndex: 0,
    onClick: () => open(s.key),
    onKeyDown: (e: React.KeyboardEvent) =>
      e.key === "Enter" && e.target === e.currentTarget && open(s.key),
  };

  if (wide && decisions.length) {
    // 有待决策：上方会话信息，下方就地作答（宽屏两列）。
    return (
      <Card className={cx("overflow-hidden", st.ring)}>
        <div
          {...clickable}
          className="cursor-pointer p-4 transition-colors hover:bg-hover/40 sm:p-5"
        >
          {body}
        </div>
        <div
          className={cx(
            "@container border-t border-line bg-surface-2/60 p-3 sm:p-4",
          )}
        >
          <div
            className={cx(
              "grid grid-cols-1 items-start gap-3",
              decisions.length > 1 && "@3xl:grid-cols-2",
            )}
          >
            {decisions.map((d) => (
              <div key={d.id} className="min-w-0">
                <DecisionComposer d={d} embedded />
              </div>
            ))}
          </div>
        </div>
      </Card>
    );
  }

  return (
    <Card
      {...clickable}
      className={cx(
        "flex h-full cursor-pointer flex-col p-4 transition-[box-shadow,transform] duration-200 hover:-translate-y-px hover:shadow-pop",
        st.ring,
        ended && "opacity-65 hover:opacity-100",
      )}
    >
      {body}
    </Card>
  );
});
