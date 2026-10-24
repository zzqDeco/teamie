import { useState } from "react";
import type { FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { Home, Menu, Plus } from "lucide-react";
import { Button, IconButton } from "./ui";
import { ICON } from "./icons";

export function parseInviteCode(raw: string): string {
  const trimmed = raw.trim();
  if (!trimmed) return "";
  const match = trimmed.match(/\/join\/([^/?#\s]+)/i);
  if (match?.[1]) {
    try {
      return decodeURIComponent(match[1]);
    } catch {
      return match[1];
    }
  }
  if (trimmed.includes("://") || trimmed.includes("/")) return "";
  return trimmed;
}

export function Welcome({
  userName,
  onCreate,
  onMenu,
}: {
  userName: string;
  onCreate: () => void;
  onMenu: () => void;
}) {
  const navigate = useNavigate();
  const [inviteInput, setInviteInput] = useState("");
  const inviteCode = parseInviteCode(inviteInput);
  function submitInvite(event: FormEvent) {
    event.preventDefault();
    if (!inviteCode) return;
    navigate(`/join/${encodeURIComponent(inviteCode)}`);
  }
  return (
    <div className="welcome-view">
      <header>
        <IconButton label="打开侧栏" className="mobile-menu" onClick={onMenu}>
          <Menu size={ICON.lg} />
        </IconButton>
        <span>
          <Home size={ICON.sm} />
          你的起点
        </span>
      </header>
      <div className="welcome-content">
        <div className="welcome-illustration">
          <span className="welcome-bubble">
            <MessagesSquareIcon />
          </span>
        </div>
        <h1>你好，{userName}</h1>
        <p>
          创建一个团队，开始协作。
          <br />
          已有团队邀请？输入邀请链接或邀请码即可加入。
        </p>
        <Button onClick={onCreate}>
          <Plus size={ICON.md} />
          创建工作空间
        </Button>
        <form className="welcome-join-form" onSubmit={submitInvite}>
          <input
            value={inviteInput}
            onChange={(event) => setInviteInput(event.target.value)}
            placeholder="粘贴邀请链接或输入邀请码"
            aria-label="邀请链接或邀请码"
          />
          <Button
            type="submit"
            className="secondary"
            disabled={!inviteCode}
          >
            加入团队
          </Button>
        </form>
      </div>
    </div>
  );
}

function MessagesSquareIcon() {
  return (
    <svg
      width="70"
      height="64"
      viewBox="0 0 70 64"
      fill="none"
      aria-hidden="true"
    >
      <path
        d="M12 12h39a9 9 0 0 1 9 9v20a9 9 0 0 1-9 9H33L21 60V50h-9a9 9 0 0 1-9-9V21a9 9 0 0 1 9-9z"
        fill="#fff"
      />
      <path
        d="M26 3h33a8 8 0 0 1 8 8v19a8 8 0 0 1-8 8h-2v8l-10-8H26a8 8 0 0 1-8-8V11a8 8 0 0 1 8-8z"
        fill="#d8efae"
        stroke="#7164d9"
        strokeWidth="3"
      />
      <circle cx="31" cy="21" r="2.7" fill="#7164d9" />
      <circle cx="42" cy="21" r="2.7" fill="#7164d9" />
      <circle cx="53" cy="21" r="2.7" fill="#7164d9" />
    </svg>
  );
}
