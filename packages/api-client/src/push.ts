import { IDL_ROUTES } from "./generated";
import { executeTransportRequest } from "./transport";
import type { HttpTransportState } from "./transport";
import type {
  PushConfig,
  PushSubscriptionList,
  PushSubscriptionResult,
  UpsertPushSubscriptionInput,
} from "./types";

export function createPushClient(state: HttpTransportState) {
  const request = <T>(path: string, options?: RequestInit): Promise<T> =>
    executeTransportRequest<T>(state, path, options, "json");

  return {
    pushConfig: (signal?: AbortSignal) =>
      request<PushConfig>(IDL_ROUTES.GetPushConfig.clientPath, { signal }),
    pushSubscriptions: (signal?: AbortSignal) =>
      request<PushSubscriptionList>(
        IDL_ROUTES.ListPushSubscriptions.clientPath,
        { signal },
      ),
    upsertPushSubscription: (input: UpsertPushSubscriptionInput) =>
      request<PushSubscriptionResult>(
        IDL_ROUTES.UpsertPushSubscription.clientPath,
        {
          method: IDL_ROUTES.UpsertPushSubscription.method,
          body: JSON.stringify(input),
        },
      ),
    deletePushSubscription: (endpoint: string) =>
      request<void>(IDL_ROUTES.DeletePushSubscription.clientPath, {
        method: IDL_ROUTES.DeletePushSubscription.method,
        body: JSON.stringify({ endpoint }),
      }),
  };
}
