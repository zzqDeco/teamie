import { ChatProvider } from "./chat";
import { InAppLinkNavigator } from "./chat/in-app-links";
import { Shell } from "./shell";

export function AuthenticatedApp() {
  return (
    <ChatProvider>
      <InAppLinkNavigator />
      <Shell />
    </ChatProvider>
  );
}
