package eino

import (
	"teamie/pkg/observability"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/cloudwego/kitex/client/callopt"
	wire "teamie/contracts/gen/agent/kitex_gen/agent"
	"teamie/contracts/gen/agent/kitex_gen/agent/modelgatewayservice"
	"teamie/contracts/gen/agent/hertz_gen/agent/modeltransport"
)

// Only Ping/Cancel use the embedded RPC client. Generation uses the same
// private HTTP service as streaming so cancel/connection loss reaches Hertz.
type gatewayHTTPClient struct {
	modelgatewayservice.Client
	url  string
	http *http.Client
}

func newGatewayHTTPClient(rpc modelgatewayservice.Client, streamURL string) (*gatewayHTTPClient, error) {
	u, err := url.Parse(streamURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid private gateway URL")
	}
	u.Path, u.RawPath = modeltransport.GeneratePath, ""
	return &gatewayHTTPClient{Client: rpc, url: u.String(), http: gatewayStreamHTTP}, nil
}

func (c *gatewayHTTPClient) Ping(ctx context.Context, in *wire.PingRequest, opts ...callopt.Option) (*wire.PingResponse, error) {
	if c == nil || c.Client == nil {
		return nil, errors.New("gateway RPC client not configured")
	}
	return c.Client.Ping(ctx, in, opts...)
}

type expectedModelConfigIDKey struct{}

func withExpectedModelConfigID(ctx context.Context, configID string) context.Context {
	return context.WithValue(ctx, expectedModelConfigIDKey{}, strings.TrimSpace(configID))
}

func expectedModelConfigIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(expectedModelConfigIDKey{}).(string)
	return id
}

func (c *gatewayHTTPClient) Chat(ctx context.Context, in *wire.GatewayChatRequest, _ ...callopt.Option) (*wire.GatewayChatResponse, error) {
	if in == nil {
		return nil, errors.New("invalid gateway request")
	}
	call := modeltransport.CallRequest{OwnerID: in.OwnerId, AgentID: in.AgentId, SessionID: in.SessionId, RunID: in.RunId, RequestJSON: in.RequestJson, ModelID: strings.TrimSpace(in.GetModelId())}
	if run, err := CurrentRun(ctx); err == nil {
		call.MessageLimit = modelInputMessageLimit(run)
	}
	body, err := json.Marshal(call)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+in.ServiceToken)
	req.Header.Set("Content-Type", "application/json")
	if expectedConfigID := expectedModelConfigIDFromContext(ctx); expectedConfigID != "" {
		req.Header.Set(modeltransport.ModelConfigIDHeader, expectedConfigID)
	}
	response, err := observability.Client(c.http, "teamie.llm-gateway").Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, readGatewayHTTPFailure(response)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, modeltransport.MaxHTTPResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > modeltransport.MaxHTTPResponseBytes {
		return nil, errors.New("gateway HTTP response exceeded limit")
	}
	var output wire.GatewayChatResponse
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&output) != nil || dec.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid gateway HTTP response")
	}
	if !output.IsSetModelConfigId() {
		if hdr := strings.TrimSpace(response.Header.Get(modeltransport.ModelConfigIDHeader)); hdr != "" {
			output.ModelConfigId = &hdr
		}
	}
	return &output, nil
}

// Only consume the gateway's bounded, structured error envelope. Provider
// messages and bodies are neither retained in failure decisions nor exposed.
func readGatewayHTTPFailure(response *http.Response) *ModelError {
	configID := strings.TrimSpace(response.Header.Get(modeltransport.ModelConfigIDHeader))
	var body struct {
		OK           bool   `json:"ok"`
		ErrorCode    string `json:"errorCode"`
		ErrorMessage string `json:"errorMessage,omitempty"`
		StatusCode   int    `json:"statusCode,omitempty"`
		Error        *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err == nil && len(raw) <= 4096 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if dec.Decode(&body) == nil && dec.Decode(new(any)) == io.EOF && !body.OK {
			if body.Error != nil && body.ErrorCode == "" {
				body.ErrorCode = body.Error.Code
			}
			if body.ErrorCode == "" {
				return &ModelError{Code: "gateway_unavailable", Retry: true, ConfigID: configID}
			}
			code := safeGatewayCode(body.ErrorCode)
			return &ModelError{Code: code, Retry: code != "invalid_request" && !isContextOverflow(code), ConfigID: configID}
		}
	}
	return &ModelError{Code: "gateway_unavailable", Retry: true, ConfigID: configID}
}
