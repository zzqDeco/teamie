#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

# 1. Contract sync gate
make gen
git diff --exit-code -- contracts/ packages/api-client/src/generated.ts

# 2. IM independence gate: services/{identity,chat,realtime} and pkg must never depend on eino, contracts/gen/agent, or services/agent-*
for svc in pkg services/identity services/chat services/realtime; do
  deps="$(cd "${svc}" && go list -deps ./...)"
  if printf '%s\n' "${deps}" | grep -E '(github\.com/cloudwego/eino|teamie/contracts/gen/agent|teamie/services/agent-)' >/dev/null; then
    echo "IM independence violation in ${svc}" >&2
    exit 1
  fi
done

# 3. DDD dependency direction gate: services/*/internal/domain/... must never import hertz, kitex, pgx, eino, or contracts/gen
for svc in services/*; do
  if [[ -d "${svc}/internal/domain" ]]; then
    domain_imports="$(cd "${svc}" && go list -f '{{join .Imports "\n"}}' ./internal/domain/...)"
    if printf '%s\n' "${domain_imports}" | grep -E '(hertz|kitex|pgx|eino|contracts/gen|teamie/)' >/dev/null; then
      echo "DDD domain layer purity violation in ${svc}/internal/domain" >&2
      exit 1
    fi
  fi
done
