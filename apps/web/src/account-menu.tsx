import { Link } from "react-router-dom";
import {
  Code2,
  Keyboard,
  LogOut,
  Megaphone,
  Settings,
  ShieldCheck,
} from "lucide-react";
import type { User } from "@teamie/api-client";
import type { Connection } from "./chat";
import { canConfigureModelGateway } from "./agent-management";
import { Avatar } from "./ui";
import { usePopover } from "./popover";
import { ICON } from "./icons";

export function AccountMenu({
  user,
  busy,
  active,
  selected = false,
  connection,
  connectionLabel,
  onSignOut,
  onNavigate,
  onShortcuts,
  onAnnouncements,
  hasUnreadAnnouncement = false,
}: {
  user: User;
  busy: boolean;
  active: boolean;
  /** The current page is one of the menu's destinations. */
  selected?: boolean;
  connection: Connection;
  connectionLabel: string;
  onSignOut: () => void;
  onNavigate: () => void;
  onShortcuts?: () => void;
  onAnnouncements?: () => void;
  hasUnreadAnnouncement?: boolean;
}) {
  const { expanded, setOpen, panel, trigger, popover, id, onBlur } =
    usePopover(active);

  function navigate() {
    setOpen(false);
    onNavigate();
  }

  function runAction(action: () => void) {
    trigger.current?.focus({ preventScroll: true });
    setOpen(false);
    onNavigate();
    action();
  }

  return (
    <div className="account-panel" ref={panel} onBlur={onBlur}>
      <button
        ref={trigger}
        className={`account-trigger ${selected ? "selected" : ""}`}
        aria-label={`账号菜单：${user.displayName}`}
        aria-current={selected ? "page" : undefined}
        title={`${user.displayName} · ${connectionLabel}`}
        aria-expanded={expanded}
        aria-controls={id}
        onClick={() => setOpen(!expanded)}
      >
        <Avatar name={user.displayName} seed={user.id} />
        {/* Connection changes are announced by the shell's live region. */}
        <span className={`rail-connection-status ${connection}`} aria-hidden="true">
          <span className="status-dot" />
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
          {canConfigureModelGateway(user) && (
            <Link to="/app/admin" onClick={navigate}>
              <ShieldCheck size={ICON.md} aria-hidden="true" />
              平台管理
            </Link>
          )}
          {onShortcuts && (
            <button type="button" onClick={() => runAction(onShortcuts)}>
              <Keyboard size={ICON.md} aria-hidden="true" />
              键盘快捷键
            </button>
          )}
          {onAnnouncements && (
            <button type="button" onClick={() => runAction(onAnnouncements)}>
              <Megaphone size={ICON.md} aria-hidden="true" />
              平台公告
              {hasUnreadAnnouncement && (
                <span className="account-menu-dot" aria-hidden="true" />
              )}
            </button>
          )}
          <div className="account-menu-separator" role="separator" />
          <button
            type="button"
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
