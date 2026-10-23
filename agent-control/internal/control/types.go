// Package control wires the Agent Control Plane 4-layer Clean DDD components
// (domain, app, adapter/http, adapter/rpc, infra/pg, infra/rpcclient) and
// exposes the service composition root.
package control

import (
	"net/http"
	"os"

	httpadapter "teamie/agent-control/internal/control/adapter/http"
	"teamie/agent-control/internal/control/domain"
)

var Version = "unreleased"
var SHA = "unknown"

type Agent = domain.Agent
type Changes = domain.Changes
type Error = domain.Error

func errAPI(status int, code, message string) error {
	return domain.Err(status, code, message)
}

func fail(w http.ResponseWriter, e error) {
	httpadapter.Fail(w, e)
}

func write(w http.ResponseWriter, status int, v any) {
	httpadapter.Write(w, status, v)
}

func env(key, otherwise string) string {
	if s := os.Getenv(key); s != "" {
		return s
	}
	return otherwise
}

func validKey(expected, provided string) bool {
	return domain.ValidKey(expected, provided)
}

func id() string {
	return domain.NewID()
}

func normalized(ids []string) []string {
	return domain.Normalized(ids)
}

func bounded(v string, min, max int) bool {
	return domain.Bounded(v, min, max)
}

func same(a, b []string) bool {
	return domain.Same(a, b)
}
