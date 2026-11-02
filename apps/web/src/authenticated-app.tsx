import { ChatProvider } from "./chat";
import { Shell } from "./shell";

export function AuthenticatedApp() {
  return (
    <ChatProvider>
      <Shell />
    </ChatProvider>
  );
}
