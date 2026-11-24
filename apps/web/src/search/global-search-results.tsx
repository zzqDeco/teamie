import { Link } from "react-router-dom";
import { Forward, Hash, MessageCircle, Paperclip, Users } from "lucide-react";
import type { GlobalSearchItem } from "@teamie/api-client";
import { AgentAvatar, AgentBadge } from "../agent-ui";
import { ICON } from "../icons";
import { Avatar, Button, date, Notice, time } from "../ui";
import { allMessageAttachments, MultiTermHighlight } from "./search-highlight";

export type VisibleGlobalSearchHit = {
  item: GlobalSearchItem;
  conversationLabel: string;
  path: string;
};

export function GlobalSearchResults({
  hasCriteria,
  pending,
  error,
  hasMore,
  loadingMore,
  items,
  terms,
  onLoadMore,
  onRetry,
}: {
  hasCriteria: boolean;
  pending: boolean;
  error: string;
  hasMore: boolean;
  loadingMore: boolean;
  items: VisibleGlobalSearchHit[];
  terms: string[];
  onLoadMore: () => void;
  onRetry: () => void;
}) {
  return (
    <div className="global-search-scroll" aria-busy={pending}>
      {!hasCriteria ? (
        <p className="global-search-empty">
          输入关键词，或选择发送人、附件、时间范围开始全局检索。
        </p>
      ) : pending ? (
        <p className="global-search-empty" role="status">
          正在跨工作空间搜索…
        </p>
      ) : error && !items.length ? (
        <div className="global-search-error">
          <Notice kind="error">{error}</Notice>
          <Button type="button" variant="secondary" onClick={onRetry}>
            重试
          </Button>
        </div>
      ) : items.length === 0 ? (
        <>
          <p className="global-search-empty" role="status">
            {hasMore
              ? "当前页匹配消息已被撤回或变更，可继续加载更早结果"
              : "没有找到匹配的消息"}
          </p>
          {error && <Notice kind="error">{error}</Notice>}
          {hasMore && (
            <Button
              type="button"
              variant="secondary"
              className="global-search-more"
              busy={loadingMore}
              onClick={onLoadMore}
            >
              加载更多
            </Button>
          )}
        </>
      ) : (
        <>
          <ol className="global-search-list" aria-label="全局搜索结果">
            {items.map(({ item, conversationLabel, path }) => {
              const { message, conversation, workspaceName } = item;
              const attachments = allMessageAttachments(message);
              const forwarded = message.forwarded;
              return (
                <li key={message.id}>
                  <Link to={path} className="global-search-card">
                    <div className="global-search-card-source">
                      <span className="search-source-workspace">
                        {workspaceName}
                      </span>
                      <span className="search-source-sep" aria-hidden="true">
                        /
                      </span>
                      <span className="search-source-conv">
                        {conversation.type === "channel" ? (
                          <Hash size={ICON.xs} aria-hidden="true" />
                        ) : conversation.type === "group" ? (
                          <Users size={ICON.xs} aria-hidden="true" />
                        ) : (
                          <MessageCircle size={ICON.xs} aria-hidden="true" />
                        )}
                        {conversationLabel}
                      </span>
                      <time dateTime={message.createdAt}>
                        {date(message.createdAt)} {time(message.createdAt)}
                      </time>
                    </div>

                    <div className="global-search-card-row">
                      {message.senderKind === "agent" ? (
                        <AgentAvatar size="small" seed={message.senderId} />
                      ) : (
                        <Avatar
                          size="small"
                          name={message.senderName}
                          seed={message.senderId}
                        />
                      )}
                      <div className="global-search-card-body">
                        <div className="global-search-card-sender">
                          <b>{message.senderName}</b>
                          {message.senderKind === "agent" && <AgentBadge />}
                        </div>
                        {message.body && (
                          <p className="global-search-snippet">
                            <MultiTermHighlight
                              text={message.body}
                              terms={terms}
                              maxLength={200}
                              plain
                            />
                          </p>
                        )}
                        {forwarded && (
                          <p className="global-search-forwarded">
                            <Forward size={ICON.xs} aria-hidden="true" />
                            <span>转发自 {forwarded.senderName}：</span>
                            <MultiTermHighlight
                              text={
                                forwarded.body ||
                                (forwarded.attachments?.length
                                  ? `${forwarded.attachments.length} 个附件`
                                  : "")
                              }
                              terms={terms}
                              maxLength={160}
                              plain
                            />
                          </p>
                        )}
                        {attachments.length > 0 && (
                          <div className="global-search-attachments">
                            <Paperclip size={ICON.xs} aria-hidden="true" />
                            <span>{attachments.length} 个附件</span>
                            {attachments.slice(0, 3).map((att) => (
                              <span
                                key={att.id}
                                className="global-search-file-pill"
                              >
                                <MultiTermHighlight
                                  text={att.filename}
                                  terms={terms}
                                />
                              </span>
                            ))}
                            {attachments.length > 3 && (
                              <span className="global-search-file-more">
                                +{attachments.length - 3}
                              </span>
                            )}
                          </div>
                        )}
                      </div>
                    </div>
                  </Link>
                </li>
              );
            })}
          </ol>
          {error && <Notice kind="error">{error}</Notice>}
          {hasMore && (
            <Button
              type="button"
              variant="secondary"
              className="global-search-more"
              busy={loadingMore}
              onClick={onLoadMore}
            >
              加载更多
            </Button>
          )}
        </>
      )}
    </div>
  );
}
