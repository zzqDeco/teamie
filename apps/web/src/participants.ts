import type { Agent, Member } from "@teamie/api-client";
export type Participant = Member & {
  selectable?: boolean;
  /**
   * True when this entry came from `api.members()` (actual workspace member);
   * false when appended purely from workspace Agent discovery.
   */
  workspaceMember?: boolean;
  /**
   * Public `enabled` flag of an Agent member's discovery profile; undefined for
   * humans and for Agents absent from discovery (which show no availability).
   */
  agentEnabled?: boolean;
};
export function enrichParticipants(
  members: Member[],
  agents: Agent[],
): Participant[] {
  const byId = new Map(agents.map((agent) => [agent.principalId, agent]));
  const result: Participant[] = members.map((member) => {
    const agent = byId.get(member.userId);
    return member.kind === "agent"
      ? {
          ...member,
          ownerId: agent?.ownerId || member.ownerId,
          ownerName: agent?.ownerName || member.ownerName,
          selectable: !!agent && agent.enabled,
          workspaceMember: true,
          ...(agent ? { agentEnabled: agent.enabled } : {}),
        }
      : { ...member, selectable: true, workspaceMember: true };
  });
  const existing = new Set(result.map((member) => member.userId));
  for (const agent of agents)
    if (!existing.has(agent.principalId))
      result.push({
        userId: agent.principalId,
        displayName: agent.name,
        kind: "agent",
        role: "member",
        ownerId: agent.ownerId,
        ownerName: agent.ownerName,
        selectable: agent.enabled,
        workspaceMember: false,
        agentEnabled: agent.enabled,
      });
  return result;
}
