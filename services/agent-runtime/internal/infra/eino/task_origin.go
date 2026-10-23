package eino

// These tools own their cached observation boundary and must recheck current
// source ACLs even when replaying. Generic native receipts cannot bypass that.
// Web, clock, and schedule tools likewise own their receipts; wrapping them
// again reserves the same call ID under a different name before Tools.execute
// can record its intent.
func privateReadTool(name string) bool {
	return len(name) >= 3 && name[:3] == "im_" ||
		len(name) >= 4 && name[:4] == "web_" ||
		name == "runtime_clock" ||
		len(name) >= 9 && name[:9] == "schedule_" ||
		name == "context_read" || name == "context_search"
}
