import { useId, useState } from "react";
import type { FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Check,
  CheckCheck,
  ChevronRight,
  Code2,
  Copy,
  Hash,
  Link as LinkIcon,
  ShieldCheck,
  Users,
} from "lucide-react";
import { api } from "@teamie/api-client";
import type { Conversation, Workspace } from "@teamie/api-client";
import { useAuth } from "./auth";
import { useChat } from "./chat";
import { errorText } from "./errors";
import { chatStore } from "./store";
import { Avatar, Button, Modal, ModalCloseButton, Notice, SwitchField } from "./ui";
import { enrichParticipants } from "./participants";
import { ParticipantIdentity } from "./group-members";
import { canConfigureModelGateway } from "./agent-management";
import { PasswordSettings } from "./password-settings";
import { ICON } from "./icons";
import { useThemePreference } from "./theme";
import type { ThemePreference } from "./theme";
import { useNotificationPreferences } from "./notifications";
export type DialogKind =
  | "workspace"
  | "channel"
  | "direct"
  | "group"
  | "invite";
export function WorkspaceDialog({
  kind,
  workspace,
  initialMemberIds = [],
  onClose,
}: {
  kind: DialogKind;
  workspace?: Workspace;
  initialMemberIds?: string[];
  onClose: () => void;
}) {
  const { user } = useAuth();
  const { refresh, data } = useChat();
  const navigate = useNavigate();
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [selected, setSelected] = useState<string[]>(initialMemberIds);
  const [invite, setInvite] = useState<{
      code: string;
      expiresAt: string;
    } | null>(null),
    [copied, setCopied] = useState(false);
  const [dirty, setDirty] = useState(false);
  const [fieldErrors, setFieldErrors] = useState<{ name?: string; members?: string }>({});
  const nameErrorId = useId();
  const membersErrorId = useId();
  const memberQuery = useQuery({
    queryKey: ["members", user!.id, workspace?.id],
    queryFn: () => api.members(workspace!.id),
    enabled: !!workspace && ["direct", "group"].includes(kind),
  });
  const agentsQuery = useQuery({
    queryKey: ["workspaceAgents", user!.id, workspace?.id],
    queryFn: () => api.workspaceAgents(workspace!.id),
    enabled:
      !!workspace &&
      ["direct", "group"].includes(kind) &&
      !!data?.capabilities["agents.discover"],
  });
  const participants = enrichParticipants(
    memberQuery.data?.members || [],
    (data?.capabilities["agents.discover"]
      ? agentsQuery.data?.agents
      : []) || [],
  ).filter(
    (member) => member.userId !== user!.id && member.selectable !== false,
  );
  const eligibleSelected = selected.filter((id) =>
    participants.some((member) => member.userId === id),
  );
  const titles: Record<DialogKind, [string, string]> = {
    workspace: ["创建工作空间", ""],
    channel: ["创建频道", "空间内所有成员都可访问。"],
    direct: ["发起私信", ""],
    group: ["创建小组", ""],
    invite: ["邀请成员", "仅已注册用户可通过此链接加入。"],
  };
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setError("");
    const form = event.currentTarget;
    const name = String(new FormData(form).get("name") || "").trim();
    const generation = chatStore.generation;
    try {
      // The form is noValidate so required-field feedback is inline Chinese
      // copy rather than the browser's native bubble.
      const nextErrors: { name?: string; members?: string } = {};
      if (["workspace", "channel", "group"].includes(kind) && !name)
        nextErrors.name = `请填写${{ workspace: "工作空间", channel: "频道", group: "小组" }[kind as "workspace" | "channel" | "group"]}名称。`;
      if (kind === "group" && (eligibleSelected.length < 1 || eligibleSelected.length > 49))
        nextErrors.members = "请选择 1 至 49 位成员。";
      setFieldErrors(nextErrors);
      if (nextErrors.name || nextErrors.members) {
        if (nextErrors.name) form.querySelector<HTMLInputElement>('input[name="name"]')?.focus();
        return;
      }
      if (kind === "workspace") {
        const result = await api.createWorkspace(name);
        if (generation !== chatStore.generation) return;
        chatStore.addConversation(result.conversation);
        await refresh();
        navigate(`/app/w/${result.workspace.id}/c/${result.conversation.id}`);
        onClose();
      } else if (kind === "invite")
        setInvite(await api.createInvite(workspace!.id));
      else {
        const result = await api.createConversation(workspace!.id, {
          type: kind as Conversation["type"],
          ...(name ? { name } : {}),
          ...(eligibleSelected.length ? { memberIds: eligibleSelected } : {}),
        });
        if (generation !== chatStore.generation) return;
        chatStore.addConversation(result.conversation);
        await refresh();
        navigate(`/app/w/${workspace!.id}/c/${result.conversation.id}`);
        onClose();
      }
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  const link = invite ? `${location.origin}/join/${invite.code}` : "";
  async function copy() {
    try {
      await navigator.clipboard.writeText(link);
      setCopied(true);
    } catch {
      setError("复制未成功，请选中链接手动复制。");
    }
  }
  return (
    <Modal
      title={titles[kind][0]}
      subtitle={titles[kind][1]}
      onClose={onClose}
      busy={busy}
      dirty={dirty || (!!invite && !copied)}
      discardMessage={
        invite && !copied
          ? "邀请链接还没有复制，确定关闭吗？"
          : "放弃未保存的更改？"
      }
    >
      <form
        noValidate
        onSubmit={submit}
        onInput={() => {
          setDirty(true);
          if (fieldErrors.name) setFieldErrors((old) => ({ ...old, name: undefined }));
        }}
      >
        <Notice>{error}</Notice>
        {kind === "workspace" && (
          <>
            <label>
              工作空间名称
              <input
                name="name"
                placeholder="例如：灵感工作室"
                maxLength={80}
                required
                autoComplete="off"
                aria-invalid={fieldErrors.name ? true : undefined}
                aria-describedby={fieldErrors.name ? nameErrorId : undefined}
              />
            </label>
            <FieldError id={nameErrorId} message={fieldErrors.name} />
            <div className="form-note">
              <Hash size={ICON.md} />
              <span>自动创建 general 频道。</span>
            </div>
          </>
        )}
        {kind === "channel" && (
          <>
            <label>
              频道名称
              <div className="input-icon">
                <Hash size={ICON.sm} />
                <input
                  name="name"
                  placeholder="例如：产品讨论"
                  maxLength={80}
                  required
                  autoComplete="off"
                  aria-invalid={fieldErrors.name ? true : undefined}
                  aria-describedby={fieldErrors.name ? nameErrorId : undefined}
                />
              </div>
            </label>
            <FieldError id={nameErrorId} message={fieldErrors.name} />
            <div className="form-note">
              <Users size={ICON.md} />
              <span>工作空间内的所有成员都可以访问这个频道。</span>
            </div>
          </>
        )}
        {kind === "group" && (
          <>
            <label>
              小组名称
              <input
                name="name"
                placeholder="例如：周五灵感碰头"
                maxLength={80}
                required
                aria-invalid={fieldErrors.name ? true : undefined}
                aria-describedby={fieldErrors.name ? nameErrorId : undefined}
              />
            </label>
            <FieldError id={nameErrorId} message={fieldErrors.name} />
          </>
        )}
        {["direct", "group"].includes(kind) && (
          <div
            className="member-select"
            aria-describedby={fieldErrors.members ? membersErrorId : undefined}
          >
            <span className="label-text">
              {kind === "direct"
                ? "选择一位伙伴"
                : `选择小组成员 · 已选 ${eligibleSelected.length} 位`}
            </span>
            {memberQuery.isLoading && <p className="muted">正在加载成员…</p>}
            {memberQuery.error && (
              <div role="alert">
                <Notice>成员加载失败。</Notice>
                <Button type="button" variant="secondary" size="sm" onClick={() => void memberQuery.refetch()}>
                  重新加载
                </Button>
              </div>
            )}
            {participants.map((member) => (
              <button
                key={member.userId}
                type="button"
                disabled={kind === "group" && !selected.includes(member.userId) && eligibleSelected.length >= 49}
                className={`member-option ${selected.includes(member.userId) ? "selected" : ""}`}
                onClick={() => {
                  setDirty(true);
                  setFieldErrors((old) => ({ ...old, members: undefined }));
                  setSelected((old) =>
                    kind === "direct"
                      ? [member.userId]
                      : old.includes(member.userId)
                        ? old.filter((id) => id !== member.userId)
                        : [...old, member.userId],
                  );
                }}
              >
                <ParticipantIdentity member={member} />
                <span className="selection-check">
                  {selected.includes(member.userId) && <Check size={ICON.xs} />}
                </span>
              </button>
            ))}
            {!memberQuery.isLoading &&
              !agentsQuery.isLoading &&
              !memberQuery.error &&
              participants.length === 0 && (
                <div className="mini-empty">
                  暂无可选成员。请先邀请成员加入工作空间。
                </div>
              )}
            <FieldError id={membersErrorId} message={fieldErrors.members} />
          </div>
        )}
        {kind === "invite" && (
          <div className="invite-generator">
            <div className="invite-generator-icon">
              <LinkIcon size={ICON.xl} />
            </div>
            <h3>
              {invite ? "链接准备好了！" : `邀请伙伴加入「${workspace?.name}」`}
            </h3>
            <p>拥有链接的已注册用户，可以加入这个工作空间。</p>
            {invite && (
              <>
                <label>
                  工作空间邀请链接
                  <div className="copy-input">
                    <input
                      value={link}
                      readOnly
                      onFocus={(e) => e.target.select()}
                      aria-label="工作空间邀请链接"
                    />
                    <button
                      type="button"
                      onClick={copy}
                      aria-label="复制邀请链接"
                    >
                      {copied ? <CheckCheck size={ICON.md} /> : <Copy size={ICON.md} />}
                    </button>
                  </div>
                </label>
                <small>
                  有效期至 {new Date(invite.expiresAt).toLocaleString("zh-CN")}
                </small>
              </>
            )}
          </div>
        )}
        <div className="modal-actions">
          <ModalCloseButton>{invite ? "完成" : "取消"}</ModalCloseButton>
          {!invite && (
            <Button
              type="submit"
              busy={busy}
              disabled={
                ["direct", "group"].includes(kind) &&
                (eligibleSelected.length < 1 || (kind === "group" && eligibleSelected.length > 49) ||
                  memberQuery.isLoading ||
                  agentsQuery.isLoading)
              }
            >
              {kind === "invite"
                ? "创建邀请链接"
                : kind === "workspace"
                  ? "创建工作空间"
                  : kind === "direct"
                    ? "开始私信"
                    : kind === "group"
                      ? "创建小组"
                      : "创建频道"}
            </Button>
          )}
        </div>
      </form>
    </Modal>
  );
}
function FieldError({ id, message }: { id: string; message?: string }) {
  return message ? (
    <small id={id} className="field-error" role="alert">
      {message}
    </small>
  ) : null;
}
export function ProfileSettings() {
  const { user, updateUser, logout } = useAuth();
  const client = useQueryClient();
  const navigate = useNavigate();
  const [themePref, setThemePref] = useThemePreference();
  const {
    preferences: notificationPrefs,
    permission: notificationPermission,
    setPreferences: setNotificationPrefs,
    enableNotifications,
    disableNotifications,
  } = useNotificationPreferences(user?.id);
  const effectiveNotificationsEnabled =
    notificationPrefs.enabled && notificationPermission === "granted";
  const [name, setName] = useState(user!.displayName),
    [saving, setSaving] = useState(false),
    [signingOut, setSigningOut] = useState(false),
    [error, setError] = useState(""),
    [saved, setSaved] = useState(false);
  const themeOptions: { value: ThemePreference; label: string }[] = [
    { value: "system", label: "跟随系统" },
    { value: "light", label: "浅色" },
    { value: "dark", label: "深色" },
  ];
  async function save(event: FormEvent) {
    event.preventDefault();
    const trimmed = name.trim();
    if (!trimmed) {
      setError("请填写显示名称。");
      return;
    }
    const sessionToken = api.csrfToken;
    const subjectId = user!.id;
    setSaving(true);
    setSaved(false);
    setError("");
    try {
      const result = await api.profile(trimmed);
      if (sessionToken !== api.csrfToken || result.user.id !== subjectId) return;
      updateUser(result.user);
      await client.invalidateQueries({ queryKey: ["bootstrap"] });
      if (sessionToken !== api.csrfToken) return;
      setSaved(true);
    } catch (e) {
      if (sessionToken === api.csrfToken) setError(errorText(e));
    } finally {
      setSaving(false);
    }
  }
  async function signOut() {
    const sessionToken = api.csrfToken;
    setError("");
    setSigningOut(true);
    try {
      await logout();
      if (!api.csrfToken) navigate("/login");
    } catch (e) {
      if (sessionToken === api.csrfToken) setError(errorText(e));
    } finally {
      setSigningOut(false);
    }
  }
  return (
    <div className="settings-view">
      <div className="profile-card">
        <div className="profile-banner"></div>
        <div className="profile-content">
          <Avatar name={user!.displayName} seed={user!.id} size="large" />
          <h2>{user!.displayName}</h2>
          <p>{user!.email}</p>
          <span
            className={`profile-tag ${user!.platformRole === "admin" ? "admin-profile-tag" : ""}`}
          >
            {user!.platformRole === "admin" ? "平台管理员" : "团队伙伴"}
          </span>
        </div>
      </div>
      <form className="settings-form" noValidate onSubmit={save}>
        <h3>基本信息</h3>
        <Notice>{error}</Notice>
        <Notice kind="success">{saved ? "资料已保存。" : ""}</Notice>
        <label>
          显示名称
          <input
            value={name}
            onChange={(e) => {
              setName(e.target.value);
              setSaved(false);
            }}
            maxLength={80}
            required
          />
        </label>
        <label>
          邮箱地址
          <input value={user!.email} readOnly className="readonly" />
          <small className="field-help">此邮箱用于登录你的 Teamie 账户。</small>
        </label>
        <Button
          busy={saving}
          type="submit"
          disabled={signingOut || !name.trim() || name.trim() === user!.displayName}
        >
          保存更改
        </Button>
      </form>
      <section className="settings-form theme-settings" aria-label="外观">
        <h3>外观</h3>
        <p className="field-help">选择界面外观主题，仅保存在当前设备。</p>
        <div className="theme-options" role="group" aria-label="外观主题">
          {themeOptions.map((opt) => (
            <button
              key={opt.value}
              type="button"
              className={`theme-option ${themePref === opt.value ? "active" : ""}`}
              aria-pressed={themePref === opt.value}
              onClick={() => setThemePref(opt.value)}
            >
              {opt.label}
            </button>
          ))}
        </div>
      </section>
      <section
        className="settings-form notification-settings"
        aria-label="通知"
      >
        <h3>通知</h3>
        <p className="field-help">
          当页面在后台或正在浏览其他会话时，对提及、回复和私信发送浏览器系统通知（仅当前页面打开时生效）。
        </p>
        <SwitchField
          className="notification-enable-switch"
          checked={effectiveNotificationsEnabled}
          disabled={
            notificationPermission === "unsupported" ||
            notificationPermission === "denied"
          }
          title="开启浏览器通知"
          description={
            notificationPermission === "unsupported"
              ? "当前浏览器不支持系统通知。"
              : notificationPermission === "denied"
                ? "浏览器通知权限已被阻止，请在浏览器地址栏设置中允许通知后再试。"
                : "收到 @提及、回复或私信时在系统通知栏提醒。"
          }
          onChange={(checked) => {
            if (checked) {
              void enableNotifications();
            } else {
              disableNotifications();
            }
          }}
        />
        <SwitchField
          className="notification-preview-switch"
          checked={notificationPrefs.showPreview}
          disabled={!effectiveNotificationsEnabled}
          title="显示消息内容"
          description="默认关闭以保护隐私；开启后在通知中显示消息正文预览。"
          onChange={(checked) =>
            setNotificationPrefs({ showPreview: checked })
          }
        />
      </section>
      <PasswordSettings />
      <Link to="/app/developers" className="platform-management-link">
        <Code2 size={ICON.lg} aria-hidden="true" />
        <span>
          <b>开发者平台</b>
          <small>自部署 Web + MCP 应用、线上协议拨测与提审手册</small>
        </span>
        <ChevronRight size={ICON.md} aria-hidden="true" />
      </Link>
      {canConfigureModelGateway(user) && (
        <Link to="/app/admin" className="platform-management-link">
          <ShieldCheck size={ICON.lg} aria-hidden="true" />
          <span>
            <b>平台管理</b>
            <small>注册邀请、用户、模型网关、应用市场与平台公告</small>
          </span>
          <ChevronRight size={ICON.md} aria-hidden="true" />
        </Link>
      )}
      <div className="session-setting">
        <div>
          <h3>当前登录</h3>
          <p>退出后，下次访问需要重新登录。</p>
        </div>
        <Button
          className="secondary danger-text"
          busy={signingOut}
          disabled={saving}
          onClick={signOut}
        >
          退出登录
        </Button>
      </div>
    </div>
  );
}
