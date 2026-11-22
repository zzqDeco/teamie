import { useEffect, useRef, useState } from "react";
import type { LocalMessage } from "../store";
import { plainPreview } from "./plain-preview";

const MAX_ANNOUNCEMENT_CHARS = 120;

export type LiveMessageAnnouncement = {
  text: string;
  slot: 0 | 1;
};

function formatLiveMessageSummary(message: LocalMessage): string {
  const raw =
    plainPreview(message.body) ||
    (message.attachments?.length
      ? `${message.attachments.length} 个文件`
      : message.forwarded
        ? "转发了一条消息"
        : "发来一条新消息");
  const chars = Array.from(raw);
  const summary =
    chars.length > MAX_ANNOUNCEMENT_CHARS
      ? `${chars.slice(0, MAX_ANNOUNCEMENT_CHARS).join("")}…`
      : raw;
  return `${message.senderName}：${summary}`;
}

/**
 * Produces polite screen-reader announcements exclusively for single real-time
 * incoming messages from other participants. Alternates between two persistent
 * live-region slots so consecutive messages with identical summaries (e.g.,
 * repeated short replies or attachment-only messages) still trigger a fresh
 * announcement in screen readers.
 */
export function useLiveMessageAnnouncement({
  conversationId,
  messages,
  userId,
  loading,
  locating,
  located,
}: {
  conversationId: string;
  messages: readonly LocalMessage[];
  userId: string;
  loading: boolean;
  locating: boolean;
  located: boolean;
}): LiveMessageAnnouncement {
  const [announcement, setAnnouncement] = useState<LiveMessageAnnouncement>({
    text: "",
    slot: 0,
  });
  const stateRef = useRef<{
    conversationId: string;
    ready: boolean;
    maxSeq: number;
    knownIds: Set<string>;
  }>({
    conversationId,
    ready: false,
    maxSeq: 0,
    knownIds: new Set(),
  });

  useEffect(() => {
    const prev = stateRef.current;
    if (prev.conversationId !== conversationId) {
      stateRef.current = {
        conversationId,
        ready: false,
        maxSeq: 0,
        knownIds: new Set(),
      };
      setAnnouncement({ text: "", slot: 0 });
    }
    const current = stateRef.current;

    if (loading || locating || located) {
      current.ready = false;
      return;
    }

    let highestSeq = current.maxSeq;
    for (const msg of messages) {
      if (!msg.status && msg.seq > highestSeq) highestSeq = msg.seq;
    }

    if (!current.ready) {
      current.ready = true;
      current.maxSeq = highestSeq;
      current.knownIds = new Set(messages.map((msg) => msg.id));
      return;
    }

    const added = messages.filter(
      (msg) =>
        !msg.status &&
        !msg.retractedAt &&
        msg.seq > current.maxSeq &&
        !current.knownIds.has(msg.id),
    );

    current.maxSeq = Math.max(current.maxSeq, highestSeq);
    for (const msg of messages) current.knownIds.add(msg.id);

    if (added.length === 1 && added[0]!.senderId !== userId) {
      const nextText = formatLiveMessageSummary(added[0]!);
      setAnnouncement((last) => ({
        text: nextText,
        slot: last.slot === 0 ? 1 : 0,
      }));
    }
  }, [conversationId, messages, userId, loading, locating, located]);

  return announcement;
}
