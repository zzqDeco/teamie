#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GOPATH_BIN="$(go env GOPATH)/bin"
export PATH="${GOPATH_BIN}:${PATH}"

for bin in kitex thriftgo node; do
  if ! command -v "${bin}" >/dev/null 2>&1; then
    echo "missing required codegen binary: ${bin}" >&2
    exit 1
  fi
done

(
  cd "${ROOT_DIR}/backend"
  kitex -module teamie/backend ../contracts/im.thrift
  for idl in im_identity_http.thrift im_chat_http.thrift im_realtime_ws.thrift; do
    thriftgo -g go:template=slim,no_default_serdes=true,no_processor=true -o hertz_gen "../contracts/${idl}"
  done
)

(
  cd "${ROOT_DIR}/agent-protocol"
  kitex -module teamie/agent-protocol ../contracts/agent.thrift
  for idl in agent_control_http.thrift agent_gateway_http.thrift; do
    thriftgo -g go:template=slim,no_default_serdes=true,no_processor=true -o hertz_gen "../contracts/${idl}"
  done
)

node "${ROOT_DIR}/tools/thrift-ts-codegen/index.mjs"
