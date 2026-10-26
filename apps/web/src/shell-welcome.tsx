import { useId, useState } from "react";
import type { FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { Home, Menu, Plus } from "lucide-react";
import { Button, IconButton } from "./ui";
import { FieldError } from "./password-field";
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
  const inputId = useId();
  const hintId = useId();
  const unparsable = !!inviteInput.trim() && !inviteCode;
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
          已有工作空间邀请？粘贴工作空间邀请链接即可加入。
        </p>
        <Button onClick={onCreate}>
          <Plus size={ICON.md} />
          创建工作空间
        </Button>
        <form className="welcome-join-form" onSubmit={submitInvite}>
          <label htmlFor={inputId}>加入工作空间：粘贴工作空间邀请链接</label>
          <input
            id={inputId}
            value={inviteInput}
            onChange={(event) => setInviteInput(event.target.value)}
            placeholder="https://…/join/…"
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            aria-invalid={unparsable || undefined}
            aria-describedby={unparsable ? hintId : undefined}
          />
          <Button type="submit" variant="secondary" disabled={!inviteCode}>
            加入工作空间
          </Button>
          <FieldError
            id={hintId}
            message={
              unparsable
                ? "请粘贴 /join/ 开头的工作空间邀请链接，注册邀请码请在注册页使用"
                : undefined
            }
          />
        </form>
      </div>
    </div>
  );
}

function MessagesSquareIcon() {
  return (
    <svg
      width="70"
      height="70"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <path d="M14 9a2 2 0 0 1-2 2H6l-4 4V4c0-1.1.9-2 2-2h8a2 2 0 0 1 2 2z" />
      <path d="M18 9h2a2 2 0 0 1 2 2v11l-4-4h-6a2 2 0 0 1-2-2v-1" />
    </svg>
  );
}
