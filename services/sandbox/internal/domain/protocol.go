package domain

// OutputSegment represents a contiguous stdout/stderr byte slice within a paged output response.
type OutputSegment struct {
	Stream     string `json:"stream"`
	ByteOffset int64  `json:"byteOffset"`
	NextOffset int64  `json:"nextOffset"`
	Data       []byte `json:"dataBase64"`
}

// PingRequest represents a Sandbox readiness ping request.
type PingRequest struct {
	ServiceToken string
}

func (p *PingRequest) GetServiceToken() string {
	if p == nil {
		return ""
	}
	return p.ServiceToken
}

// PingResponse represents a Sandbox readiness ping response.
type PingResponse struct {
	Ready bool
}

// SandboxExecRequest represents a unary container execution request.
type SandboxExecRequest struct {
	ServiceToken   string
	AgentId        string
	RunId          string
	RequestId      string
	Command        string
	TimeoutSeconds int32
	ExecutionId    *string
}

func (p *SandboxExecRequest) GetExecutionId() string {
	if p == nil || p.ExecutionId == nil {
		return ""
	}
	return *p.ExecutionId
}

// SandboxExecResponse represents a unary container execution response.
type SandboxExecResponse struct {
	Ok           bool
	ExitCode     int32
	Stdout       string
	Stderr       string
	Truncated    bool
	ErrorCode    *string
	ErrorMessage *string
}

func (p *SandboxExecResponse) GetErrorCode() string {
	if p == nil || p.ErrorCode == nil {
		return ""
	}
	return *p.ErrorCode
}

func (p *SandboxExecResponse) GetErrorMessage() string {
	if p == nil || p.ErrorMessage == nil {
		return ""
	}
	return *p.ErrorMessage
}

// SandboxWorkspaceRequest represents a workspace operation request.
type SandboxWorkspaceRequest struct {
	ServiceToken string
	AgentId      string
	Operation    string
	ArgsJson     string
	RequestId    *string
}

func (p *SandboxWorkspaceRequest) GetRequestId() string {
	if p == nil || p.RequestId == nil {
		return ""
	}
	return *p.RequestId
}

// SandboxWorkspaceResponse represents a workspace operation response.
type SandboxWorkspaceResponse struct {
	Ok           bool
	DataJson     *string
	ErrorCode    *string
	ErrorMessage *string
}

func (p *SandboxWorkspaceResponse) GetDataJson() string {
	if p == nil || p.DataJson == nil {
		return ""
	}
	return *p.DataJson
}

func (p *SandboxWorkspaceResponse) GetErrorCode() string {
	if p == nil || p.ErrorCode == nil {
		return ""
	}
	return *p.ErrorCode
}

func (p *SandboxWorkspaceResponse) GetErrorMessage() string {
	if p == nil || p.ErrorMessage == nil {
		return ""
	}
	return *p.ErrorMessage
}

// SandboxImportRequest represents an atomic binary attachment import request.
type SandboxImportRequest struct {
	ServiceToken string
	AgentId      string
	RunId        string
	RequestId    string
	Path         string
	Content      []byte
	Sha256       string
}

// SandboxExportRequest represents a bounded binary file export request.
type SandboxExportRequest struct {
	ServiceToken string
	AgentId      string
	Path         string
}

// SandboxExportResponse represents a bounded binary file export response.
type SandboxExportResponse struct {
	Ok        bool
	Content   []byte
	Sha256    *string
	ErrorCode *string
}

func (p *SandboxExportResponse) GetSha256() string {
	if p == nil || p.Sha256 == nil {
		return ""
	}
	return *p.Sha256
}

func (p *SandboxExportResponse) GetErrorCode() string {
	if p == nil || p.ErrorCode == nil {
		return ""
	}
	return *p.ErrorCode
}

// SandboxStartRequest represents a managed task start request.
type SandboxStartRequest struct {
	ServiceToken   string
	AgentId        string
	RunId          string
	TaskId         string
	Command        string
	TimeoutSeconds *int32
	OutputVersion  *int32
}

func (p *SandboxStartRequest) GetTimeoutSeconds() int32 {
	if p == nil || p.TimeoutSeconds == nil {
		return 0
	}
	return *p.TimeoutSeconds
}

func (p *SandboxStartRequest) GetOutputVersion() int32 {
	if p == nil || p.OutputVersion == nil {
		return 0
	}
	return *p.OutputVersion
}

// SandboxTaskRequest represents a managed task status/output/cancel request.
type SandboxTaskRequest struct {
	ServiceToken string
	AgentId      string
	TaskId       string
	Offset       *int64
	Limit        *int32
}

func (p *SandboxTaskRequest) GetOffset() int64 {
	if p == nil || p.Offset == nil {
		return 0
	}
	return *p.Offset
}

func (p *SandboxTaskRequest) GetLimit() int32 {
	if p == nil || p.Limit == nil {
		return 0
	}
	return *p.Limit
}

// SandboxTaskResponse represents a managed task status/output/cancel response.
type SandboxTaskResponse struct {
	Ok                     bool
	TaskId                 string
	State                  string
	ExitCode               *int32
	Stdout                 *string
	Stderr                 *string
	Truncated              *bool
	NextOffset             *int64
	ErrorCode              *string
	ErrorMessage           *string
	OutputBytes            *int64
	OutputLimitBytes       *int64
	OutputStatus           *string
	HasMore                *bool
	OutputSha256           *string
	OutputExpiresAt        *string
	OutputRetentionSeconds *int64
	OutputAvailability     *string
}

func (p *SandboxTaskResponse) GetExitCode() int32 {
	if p == nil || p.ExitCode == nil {
		return 0
	}
	return *p.ExitCode
}

func (p *SandboxTaskResponse) GetStdout() string {
	if p == nil || p.Stdout == nil {
		return ""
	}
	return *p.Stdout
}

func (p *SandboxTaskResponse) GetStderr() string {
	if p == nil || p.Stderr == nil {
		return ""
	}
	return *p.Stderr
}

func (p *SandboxTaskResponse) GetTruncated() bool {
	if p == nil || p.Truncated == nil {
		return false
	}
	return *p.Truncated
}

func (p *SandboxTaskResponse) GetNextOffset() int64 {
	if p == nil || p.NextOffset == nil {
		return 0
	}
	return *p.NextOffset
}

func (p *SandboxTaskResponse) GetErrorCode() string {
	if p == nil || p.ErrorCode == nil {
		return ""
	}
	return *p.ErrorCode
}

func (p *SandboxTaskResponse) GetErrorMessage() string {
	if p == nil || p.ErrorMessage == nil {
		return ""
	}
	return *p.ErrorMessage
}

func (p *SandboxTaskResponse) GetOutputBytes() int64 {
	if p == nil || p.OutputBytes == nil {
		return 0
	}
	return *p.OutputBytes
}

func (p *SandboxTaskResponse) GetOutputLimitBytes() int64 {
	if p == nil || p.OutputLimitBytes == nil {
		return 0
	}
	return *p.OutputLimitBytes
}

func (p *SandboxTaskResponse) GetOutputStatus() string {
	if p == nil || p.OutputStatus == nil {
		return ""
	}
	return *p.OutputStatus
}

func (p *SandboxTaskResponse) GetHasMore() bool {
	if p == nil || p.HasMore == nil {
		return false
	}
	return *p.HasMore
}

func (p *SandboxTaskResponse) GetOutputSha256() string {
	if p == nil || p.OutputSha256 == nil {
		return ""
	}
	return *p.OutputSha256
}

func (p *SandboxTaskResponse) GetOutputExpiresAt() string {
	if p == nil || p.OutputExpiresAt == nil {
		return ""
	}
	return *p.OutputExpiresAt
}

func (p *SandboxTaskResponse) GetOutputRetentionSeconds() int64 {
	if p == nil || p.OutputRetentionSeconds == nil {
		return 0
	}
	return *p.OutputRetentionSeconds
}

func (p *SandboxTaskResponse) GetOutputAvailability() string {
	if p == nil || p.OutputAvailability == nil {
		return ""
	}
	return *p.OutputAvailability
}

// CancelRequest represents a foreground execution cancellation request.
type CancelRequest struct {
	ServiceToken string
	AgentId      string
	RunId        string
}

// Ack represents a simple boolean acknowledgement.
type Ack struct {
	Ok bool
}
