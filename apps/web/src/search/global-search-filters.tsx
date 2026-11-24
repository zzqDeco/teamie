import { Paperclip, RotateCcw } from "lucide-react";
import type { Conversation, Workspace } from "@teamie/api-client";
import { ICON } from "../icons";
import { conversationName } from "../messages";
import type { Participant } from "../participants";

export type DatePreset = "any" | "7d" | "30d" | "custom";

export function GlobalSearchFilters({
  workspaces,
  workspaceMap,
  scopedConversations,
  membersByWorkspace,
  filteredSenders,
  userId,
  effectiveWorkspaceId,
  effectiveConversationId,
  senderId,
  senderFilterText,
  hasFiles,
  datePreset,
  customAfter,
  customBefore,
  hasAnyFilter,
  onWorkspaceChange,
  onConversationChange,
  onSenderFilterTextChange,
  onSenderChange,
  onDatePresetChange,
  onCustomAfterChange,
  onCustomBeforeChange,
  onToggleHasFiles,
  onReset,
}: {
  workspaces: Workspace[];
  workspaceMap: Map<string, Workspace>;
  scopedConversations: Conversation[];
  membersByWorkspace: Map<string, Participant[]>;
  filteredSenders: Participant[];
  userId: string;
  effectiveWorkspaceId: string;
  effectiveConversationId: string;
  senderId: string;
  senderFilterText: string;
  hasFiles: boolean;
  datePreset: DatePreset;
  customAfter: string;
  customBefore: string;
  hasAnyFilter: boolean;
  onWorkspaceChange: (workspaceId: string) => void;
  onConversationChange: (conversationId: string) => void;
  onSenderFilterTextChange: (text: string) => void;
  onSenderChange: (senderId: string) => void;
  onDatePresetChange: (preset: DatePreset) => void;
  onCustomAfterChange: (date: string) => void;
  onCustomBeforeChange: (date: string) => void;
  onToggleHasFiles: () => void;
  onReset: () => void;
}) {
  return (
    <div className="global-search-filters" role="group" aria-label="搜索筛选条件">
      <label className="search-filter-field">
        <span>工作空间</span>
        <select
          aria-label="按工作空间筛选"
          value={effectiveWorkspaceId}
          onChange={(event) => onWorkspaceChange(event.target.value)}
        >
          <option value="">全部工作空间</option>
          {workspaces.map((space) => (
            <option key={space.id} value={space.id}>
              {space.name}
            </option>
          ))}
        </select>
      </label>

      <label className="search-filter-field">
        <span>对话</span>
        <select
          aria-label="按对话筛选"
          value={effectiveConversationId}
          onChange={(event) => onConversationChange(event.target.value)}
        >
          <option value="">全部对话</option>
          {scopedConversations.map((conv) => {
            const wsName = workspaceMap.get(conv.workspaceId)?.name;
            const label = conversationName(
              conv,
              userId,
              membersByWorkspace.get(conv.workspaceId) || [],
            );
            const prefix = conv.type === "channel" ? "# " : "";
            return (
              <option key={conv.id} value={conv.id}>
                {effectiveWorkspaceId || !wsName
                  ? `${prefix}${label}`
                  : `${wsName} / ${prefix}${label}`}
              </option>
            );
          })}
        </select>
      </label>

      <div className="search-filter-field">
        <label htmlFor="global-search-sender-select">发送人</label>
        <div className="search-sender-inputs">
          <input
            type="search"
            value={senderFilterText}
            onChange={(event) => onSenderFilterTextChange(event.target.value)}
            placeholder="拼音或姓名过滤…"
            aria-label="按拼音或姓名过滤发送人列表"
            className="search-sender-filter-input"
          />
          <select
            id="global-search-sender-select"
            aria-label="按发送人筛选"
            value={senderId}
            onChange={(event) => onSenderChange(event.target.value)}
          >
            <option value="">全部成员</option>
            {filteredSenders.map((member) => (
              <option key={member.userId} value={member.userId}>
                {member.displayName}
                {member.kind === "agent" ? " (Agent)" : ""}
              </option>
            ))}
          </select>
        </div>
      </div>

      <label className="search-filter-field">
        <span>时间范围</span>
        <select
          aria-label="按时间范围筛选"
          value={datePreset}
          onChange={(event) =>
            onDatePresetChange(event.target.value as DatePreset)
          }
        >
          <option value="any">不限时间</option>
          <option value="7d">近 7 天</option>
          <option value="30d">近 30 天</option>
          <option value="custom">自定义日期</option>
        </select>
      </label>

      {datePreset === "custom" && (
        <div className="search-filter-dates">
          <label className="search-filter-field">
            <span>起始日期</span>
            <input
              type="date"
              aria-label="起始日期"
              value={customAfter}
              onChange={(event) => onCustomAfterChange(event.target.value)}
            />
          </label>
          <label className="search-filter-field">
            <span>结束日期</span>
            <input
              type="date"
              aria-label="结束日期"
              value={customBefore}
              onChange={(event) => onCustomBeforeChange(event.target.value)}
            />
          </label>
        </div>
      )}

      <button
        type="button"
        className={`search-filter-chip ${hasFiles ? "active" : ""}`}
        aria-pressed={hasFiles}
        onClick={onToggleHasFiles}
      >
        <Paperclip size={ICON.xs} aria-hidden="true" />
        仅含附件
      </button>

      {hasAnyFilter && (
        <button type="button" className="search-filter-reset" onClick={onReset}>
          <RotateCcw size={ICON.xs} aria-hidden="true" />
          重置筛选
        </button>
      )}
    </div>
  );
}
