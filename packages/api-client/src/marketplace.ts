import {
  IDL_ROUTES,
  type ConversationAppSessionResp,
  type CreatedDeveloperMarketplaceAppResp,
  type CreatedMarketplaceAppResp,
  type DeveloperMarketplaceAppDetailResp,
  type DeveloperMarketplaceAppListResp,
  type DeveloperMarketplaceAppResp,
  type MarketplaceAppListResp,
  type MarketplaceAppResp,
  type RotateDeveloperMarketplaceAppSecretResp,
  type RotateMarketplaceAppSecretResp,
  type VerifyMarketplaceAppResp,
} from "./generated";
import {
  publicDeveloperMarketplaceApp,
  publicMarketplaceApp,
  publicMarketplaceReview,
  publicMarketplaceVerification,
} from "./normalizers";
import { executeTransportRequest, routePath } from "./transport";
import type { HttpTransportState } from "./transport";
import type {
  ConversationAppSession,
  CreateDeveloperMarketplaceAppInput,
  CreateMarketplaceAppInput,
  UpdateMarketplaceAppInput,
} from "./types";

const mapDetail = (res: DeveloperMarketplaceAppDetailResp) => ({
  app: publicDeveloperMarketplaceApp(res.app),
  reviews: (res.reviews || []).map(publicMarketplaceReview),
});

const mapVerify = (res: VerifyMarketplaceAppResp) => ({
  app: publicDeveloperMarketplaceApp(res.app),
  verification: publicMarketplaceVerification(res.verification),
});

export function createMarketplaceClient(state: HttpTransportState) {
  const request = <T>(path: string, options?: RequestInit): Promise<T> =>
    executeTransportRequest<T>(state, path, options);
  return {
    developerMarketplaceApps: async () => ({
      apps: (
        await request<DeveloperMarketplaceAppListResp>(
          IDL_ROUTES.ListDeveloperMarketplaceApps.clientPath,
        )
      ).apps.map(publicDeveloperMarketplaceApp),
    }),
    createDeveloperMarketplaceApp: async (
      input: CreateDeveloperMarketplaceAppInput,
    ) => {
      const res = await request<CreatedDeveloperMarketplaceAppResp>(
        IDL_ROUTES.CreateDeveloperMarketplaceApp.clientPath,
        {
          method: IDL_ROUTES.CreateDeveloperMarketplaceApp.method,
          body: JSON.stringify(input),
        },
      );
      return {
        app: publicDeveloperMarketplaceApp(res.app),
        signingSecret: res.signingSecret,
      };
    },
    developerMarketplaceApp: async (id: string) =>
      mapDetail(
        await request<DeveloperMarketplaceAppDetailResp>(
          routePath(IDL_ROUTES.GetDeveloperMarketplaceApp, { id }),
        ),
      ),
    updateDeveloperMarketplaceApp: async (
      id: string,
      input: UpdateMarketplaceAppInput,
    ) => ({
      app: publicDeveloperMarketplaceApp(
        (
          await request<DeveloperMarketplaceAppResp>(
            routePath(IDL_ROUTES.UpdateDeveloperMarketplaceApp, { id }),
            {
              method: IDL_ROUTES.UpdateDeveloperMarketplaceApp.method,
              body: JSON.stringify(input),
            },
          )
        ).app,
      ),
    }),
    verifyDeveloperMarketplaceApp: async (id: string) =>
      mapVerify(
        await request<VerifyMarketplaceAppResp>(
          routePath(IDL_ROUTES.VerifyDeveloperMarketplaceApp, { id }),
          {
            method: IDL_ROUTES.VerifyDeveloperMarketplaceApp.method,
            body: JSON.stringify({}),
          },
        ),
      ),
    submitDeveloperMarketplaceApp: async (id: string, note?: string) =>
      mapDetail(
        await request<DeveloperMarketplaceAppDetailResp>(
          routePath(IDL_ROUTES.SubmitDeveloperMarketplaceApp, { id }),
          {
            method: IDL_ROUTES.SubmitDeveloperMarketplaceApp.method,
            body: JSON.stringify(note ? { note } : {}),
          },
        ),
      ),
    withdrawDeveloperMarketplaceApp: async (id: string) =>
      mapDetail(
        await request<DeveloperMarketplaceAppDetailResp>(
          routePath(IDL_ROUTES.WithdrawDeveloperMarketplaceApp, { id }),
          {
            method: IDL_ROUTES.WithdrawDeveloperMarketplaceApp.method,
            body: JSON.stringify({}),
          },
        ),
      ),
    rotateDeveloperMarketplaceAppSecret: async (id: string) => {
      const res = await request<RotateDeveloperMarketplaceAppSecretResp>(
        routePath(IDL_ROUTES.RotateDeveloperMarketplaceAppSecret, { id }),
        {
          method: IDL_ROUTES.RotateDeveloperMarketplaceAppSecret.method,
          body: JSON.stringify({}),
        },
      );
      return {
        app: publicDeveloperMarketplaceApp(res.app),
        signingSecret: res.signingSecret,
      };
    },
    deleteDeveloperMarketplaceApp: (id: string) =>
      request<void>(
        routePath(IDL_ROUTES.DeleteDeveloperMarketplaceApp, { id }),
        { method: IDL_ROUTES.DeleteDeveloperMarketplaceApp.method },
      ),

    adminMarketplaceApps: async () => ({
      apps: (
        await request<DeveloperMarketplaceAppListResp>(
          IDL_ROUTES.ListAdminMarketplaceApps.clientPath,
        )
      ).apps.map(publicDeveloperMarketplaceApp),
    }),
    adminMarketplaceApp: async (id: string) =>
      mapDetail(
        await request<DeveloperMarketplaceAppDetailResp>(
          routePath(IDL_ROUTES.GetAdminMarketplaceApp, { id }),
        ),
      ),
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
    verifyAdminMarketplaceApp: async (id: string) =>
      mapVerify(
        await request<VerifyMarketplaceAppResp>(
          routePath(IDL_ROUTES.VerifyAdminMarketplaceApp, { id }),
          {
            method: IDL_ROUTES.VerifyAdminMarketplaceApp.method,
            body: JSON.stringify({}),
          },
        ),
      ),
    approveAdminMarketplaceApp: async (
      id: string,
      expectedRevision: number,
      note?: string,
    ) =>
      mapDetail(
        await request<DeveloperMarketplaceAppDetailResp>(
          routePath(IDL_ROUTES.ApproveAdminMarketplaceApp, { id }),
          {
            method: IDL_ROUTES.ApproveAdminMarketplaceApp.method,
            body: JSON.stringify({ expectedRevision, ...(note ? { note } : {}) }),
          },
        ),
      ),
    rejectAdminMarketplaceApp: async (
      id: string,
      expectedRevision: number,
      note: string,
    ) =>
      mapDetail(
        await request<DeveloperMarketplaceAppDetailResp>(
          routePath(IDL_ROUTES.RejectAdminMarketplaceApp, { id }),
          {
            method: IDL_ROUTES.RejectAdminMarketplaceApp.method,
            body: JSON.stringify({ expectedRevision, note }),
          },
        ),
      ),
    suspendAdminMarketplaceApp: async (id: string, reason: string) =>
      mapDetail(
        await request<DeveloperMarketplaceAppDetailResp>(
          routePath(IDL_ROUTES.SuspendAdminMarketplaceApp, { id }),
          {
            method: IDL_ROUTES.SuspendAdminMarketplaceApp.method,
            body: JSON.stringify({ reason }),
          },
        ),
      ),
    unsuspendAdminMarketplaceApp: async (id: string) =>
      mapDetail(
        await request<DeveloperMarketplaceAppDetailResp>(
          routePath(IDL_ROUTES.UnsuspendAdminMarketplaceApp, { id }),
          {
            method: IDL_ROUTES.UnsuspendAdminMarketplaceApp.method,
            body: JSON.stringify({}),
          },
        ),
      ),
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
        { method: IDL_ROUTES.DeleteAdminMarketplaceApp.method },
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
        { method: IDL_ROUTES.UninstallWorkspaceApp.method },
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
