import { useState } from "react";
import type { Workspace } from "@teamie/api-client";
import { Modal, ModalCloseButton } from "../ui";
import { WorkspaceAppsPanel } from "./workspace-apps";

/**
 * In-context workspace app management opened from a group's apps pane. It
 * shares {@link WorkspaceAppsPanel} with the routable `/app/w/:id/apps` page.
 */
export function WorkspaceMarketplaceModal({
  workspace,
  isOwner,
  marketplaceEnabled = true,
  onClose,
}: {
  workspace: Workspace;
  isOwner: boolean;
  marketplaceEnabled?: boolean;
  onClose: () => void;
}) {
  const [busy, setBusy] = useState(false);
  return (
    <Modal
      title={`「${workspace.name}」工作空间应用`}
      subtitle="安装后的应用将在当前工作空间的所有小组中对人类成员与 Agent 开放。"
      onClose={onClose}
      busy={busy}
    >
      <WorkspaceAppsPanel
        workspace={workspace}
        isOwner={isOwner}
        marketplaceEnabled={marketplaceEnabled}
        onBusyChange={setBusy}
      />
      <div className="modal-actions">
        <ModalCloseButton>关闭</ModalCloseButton>
      </div>
    </Modal>
  );
}
