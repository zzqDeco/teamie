import { useEffect } from "react";
import { ChatProvider } from "./chat";
import { InAppLinkNavigator } from "./chat/in-app-links";
import { Shell } from "./shell";

function hasFilesTransfer(event: DragEvent) {
  return (
    Array.from(event.dataTransfer?.types || []).includes("Files") ||
    (event.dataTransfer?.files?.length ?? 0) > 0
  );
}

/**
 * Prevents the browser from navigating away to a dropped local file when a
 * file is dropped outside an enabled conversation upload target.
 */
function GlobalFileDropGuard() {
  useEffect(() => {
    const onDragOver = (event: DragEvent) => {
      if (!hasFilesTransfer(event) || event.defaultPrevented) return;
      event.preventDefault();
      if (event.dataTransfer) event.dataTransfer.dropEffect = "none";
    };
    const onDrop = (event: DragEvent) => {
      if (!hasFilesTransfer(event) || event.defaultPrevented) return;
      event.preventDefault();
    };
    window.addEventListener("dragover", onDragOver);
    window.addEventListener("drop", onDrop);
    return () => {
      window.removeEventListener("dragover", onDragOver);
      window.removeEventListener("drop", onDrop);
    };
  }, []);
  return null;
}

export function AuthenticatedApp() {
  return (
    <ChatProvider>
      <GlobalFileDropGuard />
      <InAppLinkNavigator />
      <Shell />
    </ChatProvider>
  );
}
