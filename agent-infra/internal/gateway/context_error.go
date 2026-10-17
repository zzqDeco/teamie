package gateway

import (
	"encoding/json"
	"regexp"
	"strings"

	"teamie/agent-infra/internal/api"
)

// Provider text is inspected only inside this boundary and is never forwarded
// or persisted. A general invalid-parameter/auth/rate-limit error must not cause
// context rewriting. DashScope documents this exact input-range failure:
// https://www.alibabacloud.com/help/en/model-studio/error-code
var inputRangeError = regexp.MustCompile(`^(?:InternalError\.Algo\.InvalidParameter: ?)?Range of input length should be\s*\[1,\s*[1-9][0-9]*\]\.?(?:$|\s)`)

func providerContextError(raw []byte) error {
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if len(raw) > 16384 || json.Unmarshal(raw, &envelope) != nil {
		return nil
	}
	e := envelope.Error
	code := strings.ToLower(e.Code)
	overflow := code == "context_length_exceeded" || code == "context_window_exceeded" || code == "max_context_length_exceeded"
	if !overflow && (code == "invalidparameter" || code == "internalerror.algo.invalidparameter" || code == "invalid_parameter_error") {
		overflow = inputRangeError.MatchString(e.Message)
	}
	if !overflow {
		return nil
	}
	return api.Err(413, "context_overflow", "Model input exceeds the provider context window")
}
