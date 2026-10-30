import { Link } from "react-router-dom";
import { Code2, Keyboard, LogOut, Settings } from "lucide-react";
import type { User } from "@teamie/api-client";
import type { Connection } from "./chat";
import { Avatar } from "./ui";
import { usePopover } from "./popover";
import { ICON } from "./icons";

export function AccountMenu({
  user,
  busy,
  active,
  connection,
  connectionLabel,
  onSignOut,
  onNavigate,
  onShortcuts,
}: {
  user: User;
  busy: boolean;
  active: boolean;
  connection: Connection;
  connectionLabel: string;
  onSignOut: () => void;
  onNavigate: () => void;
  onShortcuts?: () => void;
}) {
  const { expanded, setOpen, panel, trigger, popover, id, onBlur } =
    usePopover(active);

  function navigate() {
    setOpen(false);
    onNavigate();
  }

  return (
    <div className="account-panel" ref={panel} onBlur={onBlur}>
      <button
        ref={trigger}
        className="account-trigger"
        aria-label={`账号菜单：${user.displayName}`}
        title={`${user.displayName} · ${connectionLabel}`}
        aria-expanded={expanded}
        aria-controls={id}
        onClick={() => setOpen(!expanded)}
      >
        <Avatar name={user.displayName} seed={user.id} />
        <span
          className={`rail-connection-status ${connection}`}
          role="status"
          aria-label={connectionLabel}
        >
          <span className="status-dot" aria-hidden="true" />
          <span className="sr-only">{connectionLabel}</span>
        </span>
      </button>
      {expanded && (
        <nav
          id={id}
          className="account-menu"
          aria-label="个人账户"
          ref={popover}
        >
          <Link to="/app/settings" onClick={navigate}>
            <Settings size={ICON.md} aria-hidden="true" />
            账号设置
          </Link>
          <Link to="/app/developers" onClick={navigate}>
            <Code2 size={ICON.md} aria-hidden="true" />
            开发者平台
          </Link>
          {onShortcuts && (
            <button
              type="button"
              onClick={() => {
                trigger.current?.focus({ preventScroll: true });
                setOpen(false);
                onNavigate();
                onShortcuts();
              }}
            >
              <Keyboard size={ICON.md} aria-hidden="true" />
              键盘快捷键
            </button>
          )}
          <button
            className="danger-text"
            disabled={busy}
            onClick={() => {
              setOpen(false);
              onNavigate();
              onSignOut();
            }}
          >
            <LogOut size={ICON.md} aria-hidden="true" />
            退出登录
          </button>
        </nav>
      )}
    </div>
  );
}
