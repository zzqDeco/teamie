#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GOPATH_BIN="$(go env GOPATH)/bin"
export PATH="${PATH}:${GOPATH_BIN}"

if ! command -v node >/dev/null 2>&1; then
  echo "missing required codegen binary: node" >&2
  exit 1
fi

if [[ "${1:-}" == "--check" ]]; then
  node "${ROOT_DIR}/tools/thrift-go-http-codegen/index.mjs" --check
  node "${ROOT_DIR}/tools/thrift-ts-codegen/index.mjs" --check
  exit 0
fi

for binary in kitex thriftgo; do
  if ! command -v "${binary}" >/dev/null 2>&1; then
    echo "missing required codegen binary: ${binary}; see CONTRIBUTING.md for installation" >&2
    exit 1
  fi
done

assemble_rpc_idl() {
  local ns="$1"
  local out="$2"
  shift 2
  {
    printf 'namespace go %s\n\n' "${ns}"
    for part in "$@"; do
      sed -E '/^[[:space:]]*namespace[[:space:]]+go[[:space:]]+/d' "${part}"
      printf '\n'
    done
  } > "${out}"
}

trap 'rm -f "${ROOT_DIR}/contracts/im/rpc.thrift" "${ROOT_DIR}/contracts/agent/rpc.thrift"' EXIT

assemble_rpc_idl im "${ROOT_DIR}/contracts/im/rpc.thrift" \
  "${ROOT_DIR}/contracts/im/model.thrift" \
  "${ROOT_DIR}/contracts/im/rpc_identity.thrift" \
  "${ROOT_DIR}/contracts/im/rpc_chat.thrift"

assemble_rpc_idl agent "${ROOT_DIR}/contracts/agent/rpc.thrift" \
  "${ROOT_DIR}/contracts/agent/model.thrift" \
  "${ROOT_DIR}/contracts/agent/rpc_control.thrift" \
  "${ROOT_DIR}/contracts/agent/rpc_runtime.thrift" \
  "${ROOT_DIR}/contracts/agent/rpc_gateway.thrift" \
  "${ROOT_DIR}/contracts/agent/rpc_sandbox.thrift"

(
  cd "${ROOT_DIR}/contracts/gen/im"
  rm -rf kitex_gen hertz_gen
  kitex -module teamie/contracts/gen/im ../../im/rpc.thrift
  for idl in http_identity.thrift http_chat.thrift ws_realtime.thrift; do
    thriftgo -g go:template=slim,no_default_serdes=true,no_processor=true -o hertz_gen "../../im/${idl}"
  done
)

(
  cd "${ROOT_DIR}/contracts/gen/agent"
  rm -rf kitex_gen hertz_gen
  kitex -module teamie/contracts/gen/agent ../../agent/rpc.thrift
  for idl in http_control.thrift http_gateway.thrift; do
    thriftgo -g go:template=slim,no_default_serdes=true,no_processor=true -o hertz_gen "../../agent/${idl}"
  done
)

node "${ROOT_DIR}/tools/thrift-go-http-codegen/index.mjs"
node "${ROOT_DIR}/tools/thrift-ts-codegen/index.mjs"
