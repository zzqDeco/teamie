import { MessageSquarePlus, Plus, Users } from "lucide-react";
import { usePopover } from "./popover";
import { ICON } from "./icons";

export function ConversationActions({
  active,
  canDirect,
  canGroup,
  onSelect,
}: {
  active: boolean;
  canDirect: boolean;
  canGroup: boolean;
  onSelect: (kind: "direct" | "group") => void;
}) {
  const { expanded, setOpen, panel, trigger, popover, id, onBlur } =
    usePopover<HTMLDivElement>(active);

  function select(kind: "direct" | "group") {
    trigger.current?.focus({ preventScroll: true });
    setOpen(false);
    onSelect(kind);
  }

  return (
    <div className="conversation-actions" ref={panel} onBlur={onBlur}>
      <button
        ref={trigger}
        className="icon-button conversation-actions-trigger"
        title="新对话"
        aria-label="新对话"
        aria-expanded={expanded}
        aria-controls={id}
        onClick={() => setOpen(!expanded)}
      >
        <Plus size={ICON.lg} aria-hidden="true" />
      </button>
      {expanded && (
        <div
          id={id}
          className="conversation-actions-menu"
          role="group"
          aria-label="新对话"
          ref={popover}
        >
          {canDirect && (
            <button onClick={() => select("direct")}>
              <MessageSquarePlus size={ICON.md} aria-hidden="true" />
              发起私聊
            </button>
          )}
          {canGroup && (
            <button onClick={() => select("group")}>
              <Users size={ICON.md} aria-hidden="true" />
              创建小组
            </button>
          )}
        </div>
      )}
    </div>
  );
}
