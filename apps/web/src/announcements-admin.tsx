import { useMemo, useState } from "react";
import type { FormEvent } from "react";
import { Link } from "react-router-dom";
import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { LoaderCircle, Megaphone, ShieldCheck, Trash2 } from "lucide-react";
import { api, ApiError } from "@teamie/api-client";
import type { Announcement, User } from "@teamie/api-client";
import { useAuth } from "./auth";
import { errorText } from "./errors";
import { MessageBody } from "./markdown";
import { BadgePill, Button, Modal, Notice } from "./ui";
import { ICON } from "./icons";
import {
  announcementsHistoryQueryKey,
  charCount,
  formatAnnouncementTime,
} from "./announcements";

const TITLE_MAX_CHARS = 120;
const BODY_MAX_CHARS = 8000;

function AdminRequired() {
  return (
    <div className="unavailable-view">
      <span className="large-symbol">
        <ShieldCheck size={ICON.xl} />
      </span>
      <h2>需要平台管理员权限</h2>
      <p>平台公告仅限平台管理员发布与管理。</p>
      <Link to="/app" className="button">
        回到我的工作空间
      </Link>
    </div>
  );
}

export function PlatformAnnouncementsAdmin({
  publishEnabled = true,
}: {
  publishEnabled?: boolean;
}) {
  const { user } = useAuth();
  if (user?.platformRole !== "admin") return <AdminRequired />;
  return (
    <AdminAnnouncementsManager
      key={`${user.id}:${user.platformRole}`}
      user={user}
      publishEnabled={publishEnabled}
    />
  );
}

function AdminAnnouncementsManager({
  user,
  publishEnabled,
}: {
  user: User;
  publishEnabled: boolean;
}) {
  const client = useQueryClient();
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [mode, setMode] = useState<"write" | "preview">("write");
  const [publishing, setPublishing] = useState(false);
  const [formError, setFormError] = useState("");
  const [successMsg, setSuccessMsg] = useState("");
  const [deletingTarget, setDeletingTarget] = useState<Announcement | null>(
    null,
  );
  const [deleteBusy, setDeleteBusy] = useState(false);
  const [deleteError, setDeleteError] = useState("");

  const listQuery = useInfiniteQuery({
    queryKey: announcementsHistoryQueryKey(user.id),
    initialPageParam: 0,
    queryFn: ({ pageParam }) =>
      api.announcements({
        limit: 20,
        ...(pageParam > 0 ? { beforeSeq: pageParam } : {}),
      }),
    getNextPageParam: (lastPage) => {
      if (!lastPage.hasMore || lastPage.items.length === 0) return undefined;
      return lastPage.items[lastPage.items.length - 1].seq;
    },
    staleTime: 0,
  });

  const items = useMemo(() => {
    const seen = new Set<string>();
    const out: Announcement[] = [];
    for (const page of listQuery.data?.pages ?? []) {
      for (const item of page.items) {
        if (!seen.has(item.id)) {
          seen.add(item.id);
          out.push(item);
        }
      }
    }
    return out;
  }, [listQuery.data?.pages]);

  const titleChars = charCount(title.trim());
  const bodyChars = charCount(body.trim());

  async function handlePublish(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!publishEnabled || publishing) return;
    const cleanTitle = title.trim();
    const cleanBody = body.trim();
    if (!cleanTitle || titleChars > TITLE_MAX_CHARS) {
      setFormError(`请输入 1–${TITLE_MAX_CHARS} 字的公告标题。`);
      return;
    }
    if (!cleanBody || bodyChars > BODY_MAX_CHARS) {
      setFormError(`请输入 1–${BODY_MAX_CHARS} 字的公告正文。`);
      return;
    }
    setPublishing(true);
    setFormError("");
    setSuccessMsg("");
    try {
      await api.createAnnouncement({ title: cleanTitle, body: cleanBody });
      setTitle("");
      setBody("");
      setMode("write");
      setSuccessMsg(
        "公告已发布，在线用户将收到弹窗提醒，已启用的 Agent 也将自动同步。",
      );
      await client.invalidateQueries({ queryKey: ["announcements"] });
    } catch (err) {
      setFormError(
        err instanceof ApiError && err.code === "feature_disabled"
          ? "平台公告发布功能当前已关闭。"
          : errorText(err),
      );
    } finally {
      setPublishing(false);
    }
  }

  async function handleConfirmDelete() {
    if (!deletingTarget || deleteBusy) return;
    setDeleteBusy(true);
    setDeleteError("");
    try {
      await api.deleteAnnouncement(deletingTarget.id);
      setDeletingTarget(null);
      await client.invalidateQueries({ queryKey: ["announcements"] });
    } catch (err) {
      setDeleteError(
        err instanceof ApiError && err.code === "feature_disabled"
          ? "平台公告管理写操作当前已关闭。"
          : errorText(err),
      );
    } finally {
      setDeleteBusy(false);
    }
  }

  return (
    <div className="admin-view platform-announcements-view">
      <div className="admin-heading">
        <div>
          <h1>
            平台公告
            <span className="platform-role-badge">
              <ShieldCheck size={ICON.xs} />
              平台管理员
            </span>
          </h1>
          <p>
            面向全体人类用户（弹窗 + 历史回看）与已启用 Agent 发布版本更新或平台行为通知。
          </p>
        </div>
        <span className="admin-heading-icon">
          <Megaphone size={ICON.xl} />
        </span>
      </div>

      {!publishEnabled && (
        <Notice kind="info">
          平台公告发布功能当前已关闭，已发布的公告仍可正常查阅。
        </Notice>
      )}
      <Notice>{formError}</Notice>
      <Notice kind="success">{successMsg}</Notice>

      <form
        className="admin-announcement-composer"
        onSubmit={(e) => void handlePublish(e)}
      >
        <div className="admin-announcement-field">
          <div className="admin-announcement-label-row">
            <label htmlFor="announcement-title-input">公告标题</label>
            <span className="admin-announcement-counter">
              {charCount(title)} / {TITLE_MAX_CHARS}
            </span>
          </div>
          <input
            id="announcement-title-input"
            type="text"
            value={title}
            maxLength={TITLE_MAX_CHARS}
            disabled={!publishEnabled || publishing}
            placeholder="例如：Teamie v0.4.6 更新说明"
            onChange={(e) => {
              setTitle(e.target.value);
              if (successMsg) setSuccessMsg("");
            }}
          />
        </div>

        <div className="admin-announcement-field">
          <div className="admin-announcement-label-row">
            <label htmlFor="announcement-body-input">
              公告正文（支持 Markdown）
            </label>
            <div className="admin-announcement-label-actions">
              <span className="admin-announcement-counter">
                {charCount(body)} / {BODY_MAX_CHARS}
              </span>
              <div
                className="admin-announcement-tabs"
                role="tablist"
                aria-label="编辑与预览切换"
              >
                <button
                  type="button"
                  role="tab"
                  aria-selected={mode === "write"}
                  onClick={() => setMode("write")}
                >
                  编辑
                </button>
                <button
                  type="button"
                  role="tab"
                  aria-selected={mode === "preview"}
                  onClick={() => setMode("preview")}
                >
                  预览
                </button>
              </div>
            </div>
          </div>
          {mode === "write" ? (
            <textarea
              id="announcement-body-input"
              value={body}
              maxLength={BODY_MAX_CHARS}
              disabled={!publishEnabled || publishing}
              placeholder="编写版本更新亮点、功能说明或平台协作指引…"
              onChange={(e) => {
                setBody(e.target.value);
                if (successMsg) setSuccessMsg("");
              }}
            />
          ) : (
            <div className="admin-announcement-preview announcement-markdown">
              {body.trim() ? (
                <MessageBody body={body} userId={user.id} />
              ) : (
                <p className="admin-announcement-preview-empty">暂无可预览内容</p>
              )}
            </div>
          )}
        </div>

        <div className="admin-announcement-submit-row">
          <p className="admin-announcement-hint">
            新注册用户首次进入系统时，也会自动弹出当前最新一条公告。
          </p>
          <Button
            type="submit"
            busy={publishing}
            disabled={!publishEnabled || !title.trim() || !body.trim()}
          >
            发布公告
          </Button>
        </div>
      </form>

      <section aria-label="已发布公告列表" className="admin-announcement-list">
        {listQuery.isPending ? (
          <div className="admin-empty" role="status">
            <LoaderCircle className="spin" size={ICON.lg} />
            <p>正在加载已发布公告…</p>
          </div>
        ) : listQuery.isError ? (
          <div className="admin-empty">
            <Notice>{errorText(listQuery.error)}</Notice>
            <Button
              type="button"
              variant="secondary"
              onClick={() => void listQuery.refetch()}
            >
              重新加载
            </Button>
          </div>
        ) : items.length === 0 ? (
          <div className="admin-empty">
            <Megaphone size={ICON.xl} />
            <h3>暂无历史公告</h3>
            <p>发布第一条平台公告后，所有成员与已启用 Agent 都会收到通知。</p>
          </div>
        ) : (
          <>
            {items.map((item, idx) => (
              <article key={item.id} className="announcement-card">
                <div className="announcement-card-header">
                  <h3 className="announcement-card-title">
                    <span>{item.title}</span>
                    {idx === 0 && <BadgePill>当前最新</BadgePill>}
                  </h3>
                  <Button
                    type="button"
                    variant="danger"
                    size="sm"
                    disabled={!publishEnabled}
                    aria-label={`删除公告：${item.title}`}
                    onClick={() => {
                      setDeleteError("");
                      setDeletingTarget(item);
                    }}
                  >
                    <Trash2 size={ICON.xs} aria-hidden="true" />
                    删除
                  </Button>
                </div>
                <div className="announcement-card-meta">
                  <span>#{item.seq}</span>
                  <span aria-hidden="true">·</span>
                  <span>{item.authorName}</span>
                  <span aria-hidden="true">·</span>
                  <time dateTime={item.createdAt}>
                    {formatAnnouncementTime(item.createdAt)}
                  </time>
                </div>
                <div className="announcement-markdown">
                  <MessageBody body={item.body} userId={user.id} />
                </div>
              </article>
            ))}
            {listQuery.hasNextPage && (
              <div className="announcement-load-more">
                <Button
                  type="button"
                  variant="secondary"
                  size="sm"
                  busy={listQuery.isFetchingNextPage}
                  onClick={() => void listQuery.fetchNextPage()}
                >
                  加载更早公告
                </Button>
              </div>
            )}
          </>
        )}
      </section>

      {deletingTarget && (
        <Modal
          title="删除平台公告"
          subtitle="删除后不可恢复，尚未阅读该公告的用户与尚未消费的 Agent 将不再收到此条通知。"
          onClose={() => {
            if (!deleteBusy) setDeletingTarget(null);
          }}
          busy={deleteBusy}
        >
          <Notice>{deleteError}</Notice>
          <p>
            确定要删除公告 <b>「{deletingTarget.title}」</b> 吗？
          </p>
          <div className="announcement-modal-actions">
            <span />
            <div className="announcement-modal-actions-right">
              <Button
                type="button"
                variant="secondary"
                disabled={deleteBusy}
                onClick={() => setDeletingTarget(null)}
              >
                取消
              </Button>
              <Button
                type="button"
                variant="danger"
                busy={deleteBusy}
                onClick={() => void handleConfirmDelete()}
              >
                确认删除
              </Button>
            </div>
          </div>
        </Modal>
      )}
    </div>
  );
}
