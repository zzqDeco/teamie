package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"

	"github.com/cloudwego/kitex/client/callopt"
	wire "teamie/agent-protocol/kitex_gen/agent"
	"teamie/agent-protocol/kitex_gen/agent/modelgatewayservice"
	"teamie/agent-protocol/modeltransport"
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

func (c *gatewayHTTPClient) Chat(ctx context.Context, in *wire.GatewayChatRequest, _ ...callopt.Option) (*wire.GatewayChatResponse, error) {
	if in == nil {
		return nil, errors.New("invalid gateway request")
	}
	body, err := json.Marshal(modeltransport.CallRequest{OwnerID: in.OwnerId, AgentID: in.AgentId, SessionID: in.SessionId, RunID: in.RunId, RequestJSON: in.RequestJson})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+in.ServiceToken)
	req.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("gateway HTTP request failed")
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
	return &output, nil
}
