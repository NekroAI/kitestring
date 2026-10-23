import { useEffect } from "react";
import { NavLink, Outlet } from "react-router";
import {
  FolderKanban,
  LayoutDashboard,
  ListTodo,
  Monitor,
  PanelLeftClose,
  PanelLeftOpen,
  Settings2,
  WifiOff,
} from "lucide-react";
import { useLiveUpdates } from "../api/queries";
import { t } from "../i18n";
import { cx, relTime, useNow, usePref } from "../lib/util";
import { Tip } from "../ui/base";
import { useWorkbench } from "./inbox";
import { usePanelParams } from "./nav";
import { PlanPanel } from "./plans";
import { ReqPanel } from "./reqDrawer";
import { SessionPanel } from "./SessionPanel";

// 页面切换不做进出场动画：路由直接切换，避免闪烁与白屏。

const nav = [
  { to: "/", label: t.nav.home, icon: LayoutDashboard, end: true },
  { to: "/projects", label: t.nav.projects, icon: FolderKanban },
  { to: "/requirements", label: t.nav.requirements, icon: ListTodo },
  { to: "/settings", label: t.nav.settings, icon: Settings2 },
];

// Logo：折纸菱形鸢，四个折面靠明暗分出折痕。
export function Logo({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 128 128" className={className} aria-hidden>
      <g transform="rotate(-10 64 64)">
        <polygon points="64,10 22,54 64,54" fill="#D9503D" />
        <polygon points="64,10 64,54 106,54" fill="#EE7A60" />
        <polygon points="22,54 64,118 64,54" fill="#A9311F" />
        <polygon points="64,54 64,118 106,54" fill="#C2402D" />
      </g>
    </svg>
  );
}

function Count({ n }: { n: number }) {
  if (!n) return null;
  return (
    <span
      key={n}
      className="pop flex h-[18px] min-w-[18px] items-center justify-center rounded-full bg-seal px-1 text-[11px] leading-none font-semibold text-seal-fg tabular"
    >
      {n > 99 ? "99+" : n}
    </span>
  );
}

// 侧边栏可折叠成图标栏（R-18）：⌘\ 或底部按钮切换，记在本机。折叠后名称在悬停提示里。
function Sidebar({
  todo,
  connected,
  collapsed,
  onToggle,
}: {
  todo: number;
  connected: boolean;
  collapsed: boolean;
  onToggle: () => void;
}) {
  const wb = useWorkbench();
  const now = useNow();
  const c = collapsed;
  return (
    <aside
      className={cx(
        "fixed inset-y-0 left-0 z-30 hidden flex-col overflow-hidden border-r border-line bg-sunken/60 py-4 transition-[width] duration-200 ease-out lg:flex",
        c ? "w-16 px-2.5" : "w-[232px] px-3",
      )}
    >
      <div
        className={cx(
          "mb-6 flex items-center gap-2.5",
          c ? "justify-center" : "px-2",
        )}
      >
        <Logo className="size-8 shrink-0" />
        {!c && (
          <div className="leading-tight whitespace-nowrap">
            <div className="font-display text-[17px] font-semibold tracking-wider text-fg">
              {t.app.name}
            </div>
            <div className="text-[11px] text-fg-3">Kitestring</div>
          </div>
        )}
      </div>
      <nav className="space-y-0.5">
        {nav.map((n) => (
          <Tip key={n.to} side="right" content={c ? n.label : null}>
            {/* 外面包一层：Tip 的 asChild 会把 NavLink 的函数 className 拼成字符串 */}
            <div>
              <NavLink
                to={n.to}
                end={n.end}
                viewTransition
                aria-label={n.label}
                className={({ isActive }) =>
                  cx(
                    "relative flex h-9 items-center gap-2.5 rounded-lg text-sm font-medium whitespace-nowrap transition-colors",
                    c ? "justify-center" : "px-2.5",
                    isActive
                      ? "bg-surface text-fg shadow-xs"
                      : "text-fg-2 hover:bg-hover hover:text-fg",
                  )
                }
              >
                <n.icon className="size-[18px] shrink-0" />
                {!c && <span className="flex-1">{n.label}</span>}
                {n.to === "/" &&
                  (c ? (
                    todo > 0 && (
                      <span className="absolute top-1.5 right-2 size-2 rounded-full bg-seal" />
                    )
                  ) : (
                    <Count n={todo} />
                  ))}
              </NavLink>
            </div>
          </Tip>
        ))}
      </nav>

      {!c && (
        <div className="mt-8 px-2.5 text-xs font-medium whitespace-nowrap text-fg-3">
          {t.settings.sections.devices}
        </div>
      )}
      <div
        className={cx(
          "space-y-0.5",
          c ? "mt-6 border-t border-line pt-4" : "mt-1.5",
        )}
      >
        {wb.devices
          .filter((d) => !d.revoked)
          .map((d) => {
            const n = wb.sessions.filter(
              (s) => s.device_id === d.id && s.state !== "ended",
            ).length;
            const status = d.online
              ? t.device.online
              : d.last_seen
                ? t.device.lastSeen(relTime(d.last_seen, now))
                : t.device.never;
            return (
              <Tip
                key={d.id}
                side="right"
                content={
                  c
                    ? `${d.name} · ${status}${n ? ` · ${t.device.sessions(n)}` : ""}`
                    : status
                }
              >
                <div
                  className={cx(
                    "flex items-center gap-2.5 rounded-lg py-1.5 text-[13px] whitespace-nowrap",
                    c ? "justify-center" : "px-2.5",
                  )}
                >
                  <span className="relative shrink-0">
                    <Monitor className="size-4 text-fg-3" />
                    <span
                      className={cx(
                        "absolute -right-0.5 -bottom-0.5 size-2 rounded-full border-2 border-bg",
                        d.online ? "bg-run" : "bg-fg-3/50",
                      )}
                    />
                  </span>
                  {!c && (
                    <span
                      className={cx(
                        "min-w-0 flex-1 truncate",
                        d.online ? "text-fg-2" : "text-fg-3",
                      )}
                    >
                      {d.name}
                    </span>
                  )}
                  {!c && n > 0 && (
                    <span className="text-xs text-fg-3 tabular">{n}</span>
                  )}
                </div>
              </Tip>
            );
          })}
      </div>

      <div
        className={cx(
          "mt-auto flex items-center gap-2",
          c ? "flex-col" : "px-1",
        )}
      >
        <Tip side="right" content={connected ? t.conn.live : t.conn.offline}>
          <span
            className={cx(
              "flex h-8 items-center gap-1.5 px-1.5 text-xs",
              connected ? "text-fg-3" : "text-danger",
            )}
            aria-label={connected ? t.conn.live : t.conn.offline}
          >
            <span
              className={cx(
                "size-1.5 shrink-0 rounded-full",
                connected ? "breathe bg-run" : "animate-pulse bg-danger",
              )}
            />
            {!connected && !c && (
              <span className="whitespace-nowrap">{t.conn.offlineShort}</span>
            )}
          </span>
        </Tip>
        <Tip
          side="right"
          content={`${c ? t.nav.expand : t.nav.collapse} · ⌘\\`}
        >
          <button
            onClick={onToggle}
            aria-label={c ? t.nav.expand : t.nav.collapse}
            className={cx(
              "flex size-8 items-center justify-center rounded-lg text-fg-3 transition hover:bg-hover hover:text-fg",
              !c && "ml-auto",
            )}
          >
            {c ? (
              <PanelLeftOpen className="size-[18px]" />
            ) : (
              <PanelLeftClose className="size-[18px]" />
            )}
          </button>
        </Tip>
      </div>
    </aside>
  );
}

function TabBar({ todo }: { todo: number }) {
  return (
    <nav className="pb-safe fixed inset-x-0 bottom-0 z-30 border-t border-line bg-surface/90 backdrop-blur-xl lg:hidden">
      <div className="mx-auto grid max-w-md grid-cols-4">
        {nav.map((n) => (
          <NavLink
            key={n.to}
            to={n.to}
            end={n.end}
            viewTransition
            className={({ isActive }) =>
              cx(
                "flex flex-col items-center gap-0.5 pt-2 pb-1.5 text-[11px] font-medium transition-colors",
                isActive ? "text-accent" : "text-fg-3",
              )
            }
          >
            <span className="relative">
              <n.icon className="size-[22px]" strokeWidth={1.8} />
              {n.to === "/" && (
                <span className="absolute -top-1.5 -right-2.5">
                  <Count n={todo} />
                </span>
              )}
            </span>
            {n.label}
          </NavLink>
        ))}
      </div>
    </nav>
  );
}

function MobileHeader({ connected }: { connected: boolean }) {
  return (
    <header className="pt-safe sticky top-0 z-20 border-b border-line/70 bg-bg/85 backdrop-blur-xl lg:hidden">
      <div className="flex h-12 items-center gap-2 px-4">
        <Logo className="size-6" />
        <span className="font-display text-[17px] font-semibold tracking-wider text-fg">
          {t.app.name}
        </span>
        {!connected && (
          <span className="ml-auto flex items-center gap-1.5 text-xs text-danger">
            <WifiOff className="size-4" />
            {t.conn.offline}
          </span>
        )}
      </div>
    </header>
  );
}

export function Shell() {
  const connected = useLiveUpdates(true);
  const wb = useWorkbench();
  const { session, req, plan, close } = usePanelParams();
  const [collapsed, setCollapsed] = usePref("ks.sidebar.collapsed", false);
  // 侧边栏宽度给其它固定定位的元素用（如工作台底部的批量操作条）
  useEffect(
    () =>
      document.documentElement.style.setProperty(
        "--sidebar-w",
        collapsed ? "4rem" : "232px",
      ),
    [collapsed],
  );
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === "\\") {
        e.preventDefault();
        setCollapsed(!collapsed);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [collapsed, setCollapsed]);

  useEffect(() => {
    document.title = wb.todo
      ? `(${wb.todo}) ${t.app.fullName}`
      : t.app.fullName;
  }, [wb.todo]);

  return (
    <div className="min-h-dvh">
      <Sidebar
        todo={wb.todo}
        connected={connected}
        collapsed={collapsed}
        onToggle={() => setCollapsed(!collapsed)}
      />
      <MobileHeader connected={connected} />
      <main
        className={cx(
          "pb-24 transition-[padding] duration-200 ease-out lg:pb-10",
          collapsed ? "lg:pl-16" : "lg:pl-[232px]",
        )}
      >
        <div className="mx-auto max-w-[1360px] px-4 pt-5 sm:px-6 lg:px-10 lg:pt-9">
          <Outlet />
        </div>
      </main>
      <TabBar todo={wb.todo} />
      <SessionPanel sessionKey={session} onClose={close} />
      <ReqPanel refId={req} onClose={close} />
      <PlanPanel code={plan} onClose={close} />
    </div>
  );
}
