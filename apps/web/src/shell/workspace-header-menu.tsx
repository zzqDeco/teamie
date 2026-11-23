import { useEffect, useRef, useState } from "react";
import type { KeyboardEvent } from "react";
import { useNavigate } from "react-router-dom";
import { Blocks, ChevronDown, Hash, UserPlus, Users } from "lucide-react";
import type { Workspace } from "@teamie/api-client";
import type { DialogKind } from "../dialogs";
import { ICON } from "../icons";

export function WorkspaceHeaderMenu({
  workspace,
  owner,
  canCreateChannel,
  active,
  onOpenDialog,
  onNavigate,
}: {
  workspace?: Workspace;
  owner: boolean;
  canCreateChannel: boolean;
  active: boolean;
  onOpenDialog: (kind: DialogKind) => void;
  onNavigate: () => void;
}) {
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const wrapRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!active) setOpen(false);
  }, [active]);

  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: PointerEvent) => {
      if (
        event.target instanceof Node &&
        !wrapRef.current?.contains(event.target)
      ) {
        setOpen(false);
      }
    };
    const onKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        setOpen(false);
        triggerRef.current?.focus({ preventScroll: true });
      }
    };
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown, true);
    const raf = requestAnimationFrame(() => {
      const menu = menuRef.current;
      if (menu && !menu.contains(document.activeElement)) {
        menu
          .querySelector<HTMLElement>('[role="menuitem"]')
          ?.focus({ preventScroll: true });
      }
    });
    return () => {
      cancelAnimationFrame(raf);
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown, true);
    };
  }, [open]);

  if (!workspace) {
    return (
      <div className="workspace-title">
        <b>我的 Teamie</b>
      </div>
    );
  }

  function handleMenuKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    const items = Array.from(
      menuRef.current?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? [],
    );
    if (!items.length) return;
    const index = items.indexOf(document.activeElement as HTMLElement);
    let next = -1;
    if (event.key === "ArrowDown") next = (index + 1) % items.length;
    else if (event.key === "ArrowUp")
      next = (index - 1 + items.length) % items.length;
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = items.length - 1;
    else if (event.key === "Tab") {
      setOpen(false);
      return;
    }
    if (next >= 0) {
      event.preventDefault();
      items[next]?.focus({ preventScroll: true });
    }
  }

  return (
    <div className="workspace-title" ref={wrapRef} title={workspace.name}>
      <button
        ref={triggerRef}
        type="button"
        className="workspace-title-trigger"
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
      >
        <b>{workspace.name}</b>
        <ChevronDown
          size={ICON.xs}
          className={open ? "open" : undefined}
          aria-hidden="true"
        />
      </button>
      {open && (
        // eslint-disable-next-line jsx-a11y/interactive-supports-focus -- focus moves into menuitem on open
        <div
          ref={menuRef}
          className="menu workspace-title-menu"
          role="menu"
          aria-label="工作空间菜单"
          onKeyDown={handleMenuKeyDown}
        >
          <button
            type="button"
            role="menuitem"
            className="menu-item"
            onClick={() => {
              setOpen(false);
              onNavigate();
              navigate(`/app/w/${workspace.id}/members`);
            }}
          >
            <Users size={ICON.sm} aria-hidden="true" />
            成员目录
          </button>
          {owner && (
            <button
              type="button"
              role="menuitem"
              className="menu-item"
              onClick={() => {
                setOpen(false);
                onOpenDialog("invite");
              }}
            >
              <UserPlus size={ICON.sm} aria-hidden="true" />
              邀请成员
            </button>
          )}
          <button
            type="button"
            role="menuitem"
            className="menu-item"
            onClick={() => {
              setOpen(false);
              onNavigate();
              navigate(`/app/w/${workspace.id}/apps`);
            }}
          >
            <Blocks size={ICON.sm} aria-hidden="true" />
            工作空间应用
          </button>
          {owner && canCreateChannel && (
            <>
              <div className="menu-separator" role="separator" />
              <button
                type="button"
                role="menuitem"
                className="menu-item"
                onClick={() => {
                  setOpen(false);
                  onOpenDialog("channel");
                }}
              >
                <Hash size={ICON.sm} aria-hidden="true" />
                创建频道
              </button>
            </>
          )}
        </div>
      )}
    </div>
  );
}
