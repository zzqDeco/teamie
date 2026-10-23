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
  cd "${ROOT_DIR}/contracts/gen/im"
  kitex -module teamie/contracts/gen/im ../../im/rpc.thrift
  for idl in http_identity.thrift http_chat.thrift ws_realtime.thrift; do
    thriftgo -g go:template=slim,no_default_serdes=true,no_processor=true -o hertz_gen "../../im/${idl}"
  done
)

(
  cd "${ROOT_DIR}/contracts/gen/agent"
  kitex -module teamie/contracts/gen/agent ../../agent/rpc.thrift
  for idl in http_control.thrift http_gateway.thrift; do
    thriftgo -g go:template=slim,no_default_serdes=true,no_processor=true -o hertz_gen "../../agent/${idl}"
  done
)

node "${ROOT_DIR}/tools/thrift-go-http-codegen/index.mjs"
node "${ROOT_DIR}/tools/thrift-ts-codegen/index.mjs"
