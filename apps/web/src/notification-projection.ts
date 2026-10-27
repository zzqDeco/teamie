import { api } from "@teamie/api-client";
import { chatStore } from "./store";

export async function fetchAttentionCount(signal: AbortSignal) {
  const observed = { userId: chatStore.getSnapshot().userId, generation: chatStore.generation, epoch: chatStore.notificationEpoch };
  const page = await api.attention({ limit: 1 }, signal);
  if (signal.aborted || observed.userId !== chatStore.getSnapshot().userId || observed.generation !== chatStore.generation || observed.epoch !== chatStore.notificationEpoch) {
    throw new DOMException("Obsolete attention request", "AbortError");
  }
  if (page.attentionUnreadByConversation !== undefined && !chatStore.setAttentionProjection(page.attentionUnreadByConversation, observed)) {
    throw new Error("Incomplete attention projection");
  }
  return page;
}
