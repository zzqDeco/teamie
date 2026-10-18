package gateway

import "teamie/agent-protocol/modeltransport"

func configuredOutputLimit(c Config) int {
	window := c.ContextWindowTokens
	if window < 4096 {
		window = 16384
	}
	return min(modeltransport.MaxOutputTokens, int(window/4))
}
