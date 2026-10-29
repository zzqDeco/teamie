import {
  IDL_ROUTES, type MarketplaceAppListResp, type CreatedMarketplaceAppResp,
  type MarketplaceAppResp, type RotateMarketplaceAppSecretResp,
  type ConversationAppSessionResp,
} from "./generated";
import { publicMarketplaceApp } from "./normalizers";
import { executeTransportRequest, routePath } from "./transport";
import type { HttpTransportState } from "./transport";
import type { CreateMarketplaceAppInput, UpdateMarketplaceAppInput, ConversationAppSession } from "./types";

export function createMarketplaceClient(state: HttpTransportState) {
  const request = <T>(path: string, options?: RequestInit): Promise<T> =>
    executeTransportRequest<T>(state, path, options);
  return {
    adminMarketplaceApps: async () => ({
      apps: (
        await request<MarketplaceAppListResp>(
          IDL_ROUTES.ListAdminMarketplaceApps.clientPath,
        )
      ).apps.map(publicMarketplaceApp),
    }),
    createAdminMarketplaceApp: async (input: CreateMarketplaceAppInput) => {
      const res = await request<CreatedMarketplaceAppResp>(
        IDL_ROUTES.CreateAdminMarketplaceApp.clientPath,
        {
          method: IDL_ROUTES.CreateAdminMarketplaceApp.method,
          body: JSON.stringify(input),
        },
      );
      return {
        app: publicMarketplaceApp(res.app),
        signingSecret: res.signingSecret,
      };
    },
    updateAdminMarketplaceApp: async (
      id: string,
      input: UpdateMarketplaceAppInput,
    ) => ({
      app: publicMarketplaceApp(
        (
          await request<MarketplaceAppResp>(
            routePath(IDL_ROUTES.UpdateAdminMarketplaceApp, { id }),
            {
              method: IDL_ROUTES.UpdateAdminMarketplaceApp.method,
              body: JSON.stringify(input),
            },
          )
        ).app,
      ),
    }),
    rotateAdminMarketplaceAppSecret: async (id: string) => {
      const res = await request<RotateMarketplaceAppSecretResp>(
        routePath(IDL_ROUTES.RotateAdminMarketplaceAppSecret, { id }),
        {
          method: IDL_ROUTES.RotateAdminMarketplaceAppSecret.method,
          body: JSON.stringify({}),
        },
      );
      return {
        app: publicMarketplaceApp(res.app),
        signingSecret: res.signingSecret,
      };
    },
    deleteAdminMarketplaceApp: (id: string) =>
      request<void>(
        routePath(IDL_ROUTES.DeleteAdminMarketplaceApp, { id }),
        {
          method: IDL_ROUTES.DeleteAdminMarketplaceApp.method,
        },
      ),
    marketplaceCatalog: async () => ({
      apps: (
        await request<MarketplaceAppListResp>(
          IDL_ROUTES.ListMarketplaceCatalog.clientPath,
        )
      ).apps.map(publicMarketplaceApp),
    }),
    workspaceApps: async (workspaceId: string) => ({
      apps: (
        await request<MarketplaceAppListResp>(
          routePath(IDL_ROUTES.ListWorkspaceApps, { id: workspaceId }),
        )
      ).apps.map(publicMarketplaceApp),
    }),
    installWorkspaceApp: async (workspaceId: string, appId: string) => ({
      app: publicMarketplaceApp(
        (
          await request<MarketplaceAppResp>(
            routePath(IDL_ROUTES.InstallWorkspaceApp, { id: workspaceId }),
            {
              method: IDL_ROUTES.InstallWorkspaceApp.method,
              body: JSON.stringify({ appId }),
            },
          )
        ).app,
      ),
    }),
    uninstallWorkspaceApp: (workspaceId: string, appId: string) =>
      request<void>(
        routePath(IDL_ROUTES.UninstallWorkspaceApp, {
          id: workspaceId,
          appId,
        }),
        {
          method: IDL_ROUTES.UninstallWorkspaceApp.method,
        },
      ),
    conversationApps: async (conversationId: string) => ({
      apps: (
        await request<MarketplaceAppListResp>(
          routePath(IDL_ROUTES.ListConversationApps, { id: conversationId }),
        )
      ).apps.map(publicMarketplaceApp),
    }),
    createConversationAppSession: async (
      conversationId: string,
      appId: string,
    ): Promise<ConversationAppSession> => {
      const res = await request<ConversationAppSessionResp>(
        routePath(IDL_ROUTES.CreateConversationAppSession, {
          id: conversationId,
          appId,
        }),
        {
          method: IDL_ROUTES.CreateConversationAppSession.method,
          body: JSON.stringify({}),
        },
      );
      return {
        app: publicMarketplaceApp(res.app),
        token: res.token,
        expiresAt: res.expiresAt,
      };
    },
  };
}
