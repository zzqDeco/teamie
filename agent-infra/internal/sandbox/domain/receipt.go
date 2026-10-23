package domain

import "time"

// Receipt is the durable execution receipt for unary SandboxExec calls.
type Receipt struct {
	Fingerprint string    `json:"fingerprint"`
	State       string    `json:"state"`
	Result      Result    `json:"result"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// TaskReceipt is the durable execution receipt for managed background/foreground tasks.
type TaskReceipt struct {
	Version            int          `json:"version"`
	AgentID            string       `json:"agentId"`
	RunID              string       `json:"runId"`
	TaskID             string       `json:"taskId"`
	Command            string       `json:"command"`
	Fingerprint        string       `json:"fingerprint"`
	ContainerName      string       `json:"containerName"`
	ContainerID        string       `json:"containerId,omitempty"`
	CleanupDone        bool         `json:"cleanupDone,omitempty"`
	State              string       `json:"state"`
	TimeoutSeconds     int32        `json:"timeoutSeconds"`
	Deadline           time.Time    `json:"deadline,omitempty"`
	CancelRequested    bool         `json:"cancelRequested"`
	CancelCode         string       `json:"cancelCode,omitempty"`
	Result             Result       `json:"result"`
	Output             []OutputPart `json:"output,omitempty"`
	OutputBytes        int64        `json:"outputBytes,omitempty"`
	OutputFileBytes    int64        `json:"outputFileBytes,omitempty"`
	OutputFrames       int          `json:"outputFrames,omitempty"`
	OutputStatus       string       `json:"outputStatus,omitempty"`
	OutputSHA256       string       `json:"outputSha256,omitempty"`
	OutputAnchor       string       `json:"outputAnchor,omitempty"`
	OutputAnchorTime   string       `json:"outputAnchorTime,omitempty"`
	OutputGateReleased bool         `json:"outputGateReleased,omitempty"`
	OutputExpiresAt    *time.Time   `json:"outputExpiresAt,omitempty"`
	OutputExpiredAt    *time.Time   `json:"outputExpiredAt,omitempty"`
	UpdatedAt          time.Time    `json:"updatedAt"`
}

// OutputRetentionEligible reports whether t is eligible for output retention/expiration.
func OutputRetentionEligible(t *TaskReceipt) bool {
	return t.Version == 2 && TerminalTask(t.State) && t.State != "unknown" && t.CleanupDone
}

// FileReceipt is the durable intent/completion receipt for workspace mutations.
type FileReceipt struct {
	Version      int    `json:"version"`
	Fingerprint  string `json:"fingerprint"`
	State        string `json:"state"`
	Operation    string `json:"operation"`
	Path         string `json:"path"`
	ExpectedHash string `json:"expectedHash"`
	DataJSON     string `json:"dataJson"`
}
