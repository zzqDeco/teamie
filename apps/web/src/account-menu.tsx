import { useEffect, useId, useRef, useState } from "react";
import { Link, useLocation } from "react-router-dom";
import { Bot, ChevronUp, LogOut, Settings } from "lucide-react";
import type { User } from "@teamie/api-client";
import { Avatar } from "./ui";

export function AccountMenu({
  user,
  busy,
  onSignOut,
  onNavigate,
}: {
  user: User;
  busy: boolean;
  onSignOut: () => void;
  onNavigate: () => void;
}) {
  const [open, setOpen] = useState(false);
  const panel = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLElement>(null);
  const menuId = useId();
  const location = useLocation();

  useEffect(() => setOpen(false), [location.pathname]);
  useEffect(() => {
    if (!open) return;
    menu.current?.querySelector<HTMLElement>("a[href]")?.focus();
    function dismiss(event: PointerEvent) {
      if (
        event.target instanceof Node &&
        !panel.current?.contains(event.target)
      ) {
        setOpen(false);
        trigger.current?.focus({ preventScroll: true });
      }
    }
    function escape(event: KeyboardEvent) {
      if (event.key !== "Escape") return;
      event.preventDefault();
      event.stopPropagation();
      setOpen(false);
      trigger.current?.focus({ preventScroll: true });
    }
    document.addEventListener("pointerdown", dismiss);
    document.addEventListener("keydown", escape, true);
    return () => {
      document.removeEventListener("pointerdown", dismiss);
      document.removeEventListener("keydown", escape, true);
    };
  }, [open]);

  function navigate() {
    setOpen(false);
    onNavigate();
  }

  return (
    <div
      className="user-panel account-panel"
      ref={panel}
      onBlur={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget)) setOpen(false);
      }}
    >
      <button
        ref={trigger}
        className="user-profile account-trigger"
        aria-label={`账号菜单：${user.displayName}`}
        aria-expanded={open}
        aria-controls={menuId}
        onClick={() => setOpen(!open)}
      >
        <Avatar name={user.displayName} seed={user.id} />
        <span className="account-identity">
          <b>{user.displayName}</b>
          <small>个人账户</small>
        </span>
        <ChevronUp size={16} aria-hidden="true" />
      </button>
      {open && (
        <nav
          id={menuId}
          className="account-menu"
          aria-label="个人账户"
          ref={menu}
        >
          <Link to="/app/agents" onClick={navigate}>
            <Bot size={17} aria-hidden="true" />
            个人 Agent
          </Link>
          <Link to="/app/settings" onClick={navigate}>
            <Settings size={17} aria-hidden="true" />
            账号设置
          </Link>
          <button
            className="danger-text"
            disabled={busy}
            onClick={() => {
              setOpen(false);
              onSignOut();
            }}
          >
            <LogOut size={17} aria-hidden="true" />
            退出登录
          </button>
        </nav>
      )}
    </div>
  );
}
