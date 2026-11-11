import type { Conversation, Workspace } from "@teamie/api-client";
import type { ConversationUnreadSummary } from "../notification-model";
import type { PinyinConverter } from "./pinyin-match";

export type QuickItemKind = "channel" | "direct" | "group" | "workspace" | "page";

export type QuickItem = {
  id: string;
  label: string;
  subtitle: string;
  path: string;
  kind: QuickItemKind;
  /** Page icon key for `kind === "page"`. */
  page?: string;
  conversation?: Conversation;
  summary?: ConversationUnreadSummary;
};

export type QuickGroup = { id: string; label: string; items: QuickItem[] };

/** Special `onSelect` path that opens the keyboard shortcuts dialog. */
export const SHORTCUTS_ENTRY = "#shortcuts";

const ADMIN_PAGES: Array<[string, string, string]> = [
  ["admin-invites", "注册邀请", "/app/admin"],
  ["admin-users", "用户管理", "/app/admin/users"],
  ["admin-gateway", "模型网关", "/app/admin/gateway"],
  ["admin-marketplace", "应用市场", "/app/admin/marketplace"],
  ["admin-announcements", "平台公告", "/app/admin/announcements"],
];

/**
 * Page entries. Workspace pages target the current workspace; administrator
 * pages are listed only for human platform administrators (the server stays
 * authoritative).
 */
export function buildPageItems({
  workspace,
  isPlatformAdmin,
}: {
  workspace?: Workspace;
  isPlatformAdmin: boolean;
}): QuickItem[] {
  const page = (page: string, label: string, subtitle: string, path: string): QuickItem => ({
    id: `page:${page}`,
    label,
    subtitle,
    path,
    kind: "page",
    page,
  });
  return [
    ...(workspace
      ? [
          page("members", "成员目录", workspace.name, `/app/w/${workspace.id}/members`),
          page("apps", "应用", workspace.name, `/app/w/${workspace.id}/apps`),
        ]
      : []),
    page("attention", "提及与回复", "收件箱", "/app/attention"),
    page("saved", "收藏", "收藏的消息", "/app/attention?tab=saved"),
    page("agents", "个人 Agent", "管理你的 Agent", "/app/agents"),
    page("settings", "账号设置", "个人资料与密码", "/app/settings"),
    page("developers", "开发者平台", "应用开发与提审", "/app/developers"),
    page("shortcuts", "键盘快捷键", "查看全部快捷键", SHORTCUTS_ENTRY),
    ...(isPlatformAdmin
      ? ADMIN_PAGES.map(([id, label, path]) => page(id, label, "平台管理", path))
      : []),
  ];
}

const GROUP_LIMIT = 5;

/**
 * Empty-query view: 最近 / 频道 / 私信与小组 / 工作空间 / 页面, at most five
 * entries each, without repeating a recent conversation in its type group.
 */
export function groupEmptyState({
  conversations,
  recentConversationIds,
  workspaces,
  pages,
}: {
  conversations: QuickItem[];
  recentConversationIds: string[];
  workspaces: QuickItem[];
  pages: QuickItem[];
}): QuickGroup[] {
  const byId = new Map(conversations.map((item) => [item.id, item]));
  const recent = recentConversationIds
    .map((id) => byId.get(`conv:${id}`))
    .filter((item): item is QuickItem => !!item)
    .slice(0, GROUP_LIMIT);
  const shown = new Set(recent.map((item) => item.id));
  const rest = conversations.filter((item) => !shown.has(item.id));
  return [
    { id: "recent", label: "最近", items: recent },
    { id: "channels", label: "频道", items: rest.filter((i) => i.kind === "channel").slice(0, GROUP_LIMIT) },
    { id: "direct", label: "私信与小组", items: rest.filter((i) => i.kind !== "channel").slice(0, GROUP_LIMIT) },
    { id: "workspaces", label: "工作空间", items: workspaces.slice(0, GROUP_LIMIT) },
    { id: "pages", label: "页面", items: pages.slice(0, GROUP_LIMIT) },
  ].filter((group) => group.items.length > 0);
}

/**
 * Match rank of `item` for `query` (lower is better), or -1 for no match:
 * label prefix, word prefix, pinyin full/initials prefix, label substring,
 * pinyin substring, then subtitle substring.
 */
export function matchRank(item: QuickItem, query: string, toPinyin?: PinyinConverter | null) {
  const q = query.trim().toLocaleLowerCase();
  if (!q) return 0;
  const label = item.label.toLocaleLowerCase();
  if (label.startsWith(q)) return 0;
  if (label.split(/[\s\-_/#·]+/u).some((word) => word && word.startsWith(q))) return 1;
  const forms = toPinyin?.(item.label);
  const compact = q.replace(/\s+/gu, "");
  if (forms?.full && (forms.full.startsWith(compact) || forms.initials.startsWith(compact))) return 2;
  if (label.includes(q)) return 3;
  if (forms?.full && (forms.full.includes(compact) || forms.initials.includes(compact))) return 4;
  if (item.subtitle.toLocaleLowerCase().includes(q)) return 5;
  return -1;
}

/** Items matching `query`, prefix matches first; ties keep the input order. */
export function rankItems(items: QuickItem[], query: string, toPinyin?: PinyinConverter | null) {
  return items
    .map((item, index) => ({ item, index, rank: matchRank(item, query, toPinyin) }))
    .filter((entry) => entry.rank >= 0)
    .sort((a, b) => a.rank - b.rank || a.index - b.index)
    .map((entry) => entry.item);
}
