import { IDL_ROUTES, type TestAdminModelConfigResp } from "./generated";
import { publicModelCatalog, publicModelGateway } from "./normalizers";
import { executeTransportRequest, routePath } from "./transport";
import type { HttpTransportState } from "./transport";
import type {
  CreateModelProfileInput, CreateModelProviderInput, ModelCatalog, ModelGateway,
  ModelRoutingInput, UpdateModelProfileInput, UpdateModelProviderInput, WebSearchInput,
} from "./types";

export function createModelGatewayClient(state: HttpTransportState) {
  const request = <T>(path: string, options?: RequestInit): Promise<T> =>
    executeTransportRequest<T>(state, path, options);
  return {
    modelCatalog: async () =>
      publicModelCatalog(
        await request<ModelCatalog>(IDL_ROUTES.GetModelCatalog.clientPath),
      ),
    modelGateway: async () =>
      publicModelGateway(
        await request<ModelGateway>(IDL_ROUTES.GetAdminModelConfig.clientPath),
      ),
    saveWebSearch: async (input: WebSearchInput) =>
      publicModelGateway(
        await request<ModelGateway>(IDL_ROUTES.UpdateAdminWebSearchConfig.clientPath, {
          method: IDL_ROUTES.UpdateAdminWebSearchConfig.method,
          body: JSON.stringify(input),
        }),
      ),
    saveModelRouting: async (input: ModelRoutingInput) =>
      publicModelGateway(
        await request<ModelGateway>(IDL_ROUTES.UpdateAdminModelRouting.clientPath, {
          method: IDL_ROUTES.UpdateAdminModelRouting.method,
          body: JSON.stringify(input),
        }),
      ),
    testModelGateway: () =>
      request<TestAdminModelConfigResp>(IDL_ROUTES.TestAdminModelConfig.clientPath, {
        method: IDL_ROUTES.TestAdminModelConfig.method,
        body: JSON.stringify({}),
      }),
    createModelProvider: async (input: CreateModelProviderInput) =>
      publicModelGateway(
        await request<ModelGateway>(IDL_ROUTES.CreateAdminModelProvider.clientPath, {
          method: IDL_ROUTES.CreateAdminModelProvider.method,
          body: JSON.stringify(input),
        }),
      ),
    updateModelProvider: async (id: string, input: UpdateModelProviderInput) =>
      publicModelGateway(
        await request<ModelGateway>(
          routePath(IDL_ROUTES.UpdateAdminModelProvider, { id }),
          { method: IDL_ROUTES.UpdateAdminModelProvider.method, body: JSON.stringify(input) },
        ),
      ),
    deleteModelProvider: async (id: string) =>
      publicModelGateway(
        await request<ModelGateway>(
          routePath(IDL_ROUTES.DeleteAdminModelProvider, { id }),
          { method: IDL_ROUTES.DeleteAdminModelProvider.method },
        ),
      ),
    createModelProfile: async (input: CreateModelProfileInput) =>
      publicModelGateway(
        await request<ModelGateway>(IDL_ROUTES.CreateAdminModelProfile.clientPath, {
          method: IDL_ROUTES.CreateAdminModelProfile.method,
          body: JSON.stringify(input),
        }),
      ),
    updateModelProfile: async (id: string, input: UpdateModelProfileInput) =>
      publicModelGateway(
        await request<ModelGateway>(
          routePath(IDL_ROUTES.UpdateAdminModelProfile, { id }),
          { method: IDL_ROUTES.UpdateAdminModelProfile.method, body: JSON.stringify(input) },
        ),
      ),
    deleteModelProfile: async (id: string) =>
      publicModelGateway(
        await request<ModelGateway>(
          routePath(IDL_ROUTES.DeleteAdminModelProfile, { id }),
          { method: IDL_ROUTES.DeleteAdminModelProfile.method },
        ),
      ),
    testModelProfile: (id: string) =>
      request<TestAdminModelConfigResp>(
        routePath(IDL_ROUTES.TestAdminModelProfile, { id }),
        { method: IDL_ROUTES.TestAdminModelProfile.method, body: JSON.stringify({}) },
      ),
  };
}
