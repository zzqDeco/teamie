import type { Message, Reaction } from "@teamie/api-client";

export type ReactionState = {
  reactions: Reaction[];
  reactionsVersion?: string;
  retracted?: true;
};
export type ReactionStates = Record<string, Record<string, ReactionState>>;

export function validReactionVersion(value?: string): value is string {
  return typeof value === "string" && /^\d+$/.test(value);
}

export function messageReactionState(message: Message): ReactionState | undefined {
  if (message.reactionsVersion !== undefined && !validReactionVersion(message.reactionsVersion))
    return undefined;
  if (message.retractedAt)
    return { reactions: [], reactionsVersion: message.reactionsVersion, retracted: true };
  if (message.reactions === undefined && message.reactionsVersion === undefined)
    return undefined;
  return { reactions: message.reactions || [], reactionsVersion: message.reactionsVersion };
}

/** A tombstone is terminal; otherwise only a newer server revision replaces state. */
export function reconcileReactionState(
  current: ReactionState | undefined,
  incoming: ReactionState | undefined,
): ReactionState | undefined {
  if (!incoming) return current;
  if (current?.retracted) {
    if (incoming.retracted && validReactionVersion(incoming.reactionsVersion) &&
      (!validReactionVersion(current.reactionsVersion) || BigInt(incoming.reactionsVersion) > BigInt(current.reactionsVersion)))
      return incoming;
    return current;
  }
  if (incoming.retracted) return incoming;
  if (!current) return incoming;
  // Unversioned pre-upgrade snapshots can seed an empty cache, but cannot
  // overwrite a state already observed through history, API or durable sync.
  if (!validReactionVersion(incoming.reactionsVersion)) return current;
  if (validReactionVersion(current.reactionsVersion) &&
    BigInt(incoming.reactionsVersion) <= BigInt(current.reactionsVersion)) return current;
  return incoming;
}

export function updateReactionStates(
  states: ReactionStates | undefined,
  conversationId: string,
  messageId: string,
  incoming: ReactionState | undefined,
): ReactionStates | undefined {
  const previous = states?.[conversationId]?.[messageId];
  const state = reconcileReactionState(previous, incoming);
  if (state === previous || !state) return states;
  return {
    ...states,
    [conversationId]: { ...states?.[conversationId], [messageId]: state },
  };
}

export function materializeReactionState<T extends Message>(message: T, state?: ReactionState): T {
  if (!state) return message;
  const reactions = message.retractedAt || state.retracted ? undefined : state.reactions;
  if (message.reactions === reactions && message.reactionsVersion === state.reactionsVersion)
    return message;
  return { ...message, reactions, reactionsVersion: state.reactionsVersion };
}

export function projectMessageReactions<T extends Message>(message: T, states?: ReactionStates): T {
  const state = reconcileReactionState(
    states?.[message.conversationId]?.[message.id],
    messageReactionState(message),
  );
  return materializeReactionState(message, state);
}

export function projectReactionList<T extends Message>(messages: T[], states?: ReactionStates): T[] {
  const projected = messages.map((message) => projectMessageReactions(message, states));
  return projected.every((message, index) => message === messages[index]) ? messages : projected;
}
