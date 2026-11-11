#!/usr/bin/env bash
# Shared host paths. Callers set umask 077 before sourcing.
TEAMIE_ROOT=${TEAMIE_ROOT:-/srv/teamie}
[[ $TEAMIE_ROOT =~ ^(/[a-zA-Z0-9._-]+)+$ && ! "$TEAMIE_ROOT/" =~ /\.\.?/ ]] || { echo 'Invalid TEAMIE_ROOT' >&2; exit 1; }
export TEAMIE_ROOT
HOST_ENV="$TEAMIE_ROOT/config/production.env"
export COMPOSE_PARALLEL_LIMIT=1

ONLINE_AB_SERVICES=(identity chat marketplace realtime web agent-control llm-gateway)

compose_for() {
  local recipe=$1 image_env=$2
  COMPOSE_RECIPE=$recipe
  COMPOSE=(docker compose --project-name teamie --file "$recipe/deploy/compose.production.yaml"
    --env-file "$HOST_ENV" --env-file "$image_env")
}

cleanup_canary_containers() {
  command -v docker >/dev/null 2>&1 || return 0
  local ids
  ids=$(docker ps -aq --filter 'label=io.teamie.canary=true' 2>/dev/null || true)
  if [[ -n $ids ]]; then
    # shellcheck disable=SC2086
    docker rm -f $ids >/dev/null 2>&1 || true
  fi
}

wait_canary_healthy() {
  local container=$1 timeout_secs=${2:-120}
  local poll_secs=${TEAMIE_CANARY_POLL_SECONDS:-0.2}
  local deadline=$((SECONDS + timeout_secs)) status
  while (( SECONDS < deadline )); do
    status=$(docker inspect --format '{{if .State.Running}}{{if .State.Health}}{{.State.Health.Status}}{{else}}healthy{{end}}{{else}}stopped{{end}}' "$container" 2>/dev/null || echo missing)
    case "$status" in
      healthy) return 0 ;;
      unhealthy|stopped|missing) return 1 ;;
    esac
    sleep "$poll_secs"
  done
  return 1
}

cutover_service_ab() {
  local svc=$1 compose_json=$2 manifest_tool=$3
  local settle_secs=${TEAMIE_CANARY_SETTLE_SECONDS:-1.2}
  if python3 "$manifest_tool" service-up-to-date "$compose_json" "$svc"; then
    return 0
  fi
  local existing_ids
  existing_ids=$(docker ps -q --filter 'label=com.docker.compose.project=teamie' --filter "label=com.docker.compose.service=$svc") || return
  if [[ -z $existing_ids ]]; then
    "${COMPOSE[@]}" up -d --no-deps --wait --wait-timeout 180 "$svc" || return
    return 0
  fi

  cleanup_canary_containers
  local canary="teamie-${svc}-canary"
  if ! python3 "$manifest_tool" canary-launch "$compose_json" "$svc"; then
    cleanup_canary_containers
    return 1
  fi
  if ! wait_canary_healthy "$canary" "${TEAMIE_CANARY_HEALTH_TIMEOUT:-120}"; then
    cleanup_canary_containers
    return 1
  fi
  sleep "$settle_secs"

  if ! "${COMPOSE[@]}" stop --timeout 25 "$svc"; then
    cleanup_canary_containers
    return 1
  fi
  if ! "${COMPOSE[@]}" up -d --no-deps --wait --wait-timeout 180 "$svc"; then
    cleanup_canary_containers
    return 1
  fi
  sleep "$settle_secs"

  docker stop --time 25 "$canary" >/dev/null 2>&1 || true
  docker rm -f "$canary" >/dev/null 2>&1 || true
}

rollout_fenced() {
  local compose_json=${1:-} manifest_tool=${2:-}
  # Every service migration is forward-only and runs before its /readyz is healthy.
  "${COMPOSE[@]}" up -d --no-deps --wait --wait-timeout 180 postgres redis || return
  local services
  services=$("${COMPOSE[@]}" config --services) || return
  if [[ $services == *llm-gateway* || $services == *marketplace* ]]; then
    "${COMPOSE[@]}" exec -T postgres env TEAMIE_VERIFY_DB_CREDENTIALS=1 bash -s < "${COMPOSE_RECIPE}/deploy/postgres/01-databases.sh" || return
  fi
  if [[ $services == *llm-gateway* ]]; then
    # Do not let an old Session writer call newly migrated/replaced dependencies.
    # Async children remain owned by Docker and their Sandbox receipts survive.
    "${COMPOSE[@]}" stop --timeout 30 agent-runtime || return
  fi
  "${COMPOSE[@]}" up -d --no-deps --wait --wait-timeout 180 identity || return
  if [[ $services == *marketplace* ]]; then
    local has_legacy_marketplace
    # shellcheck disable=SC2016
    has_legacy_marketplace=$("${COMPOSE[@]}" exec -T postgres sh -ec \
      'PGPASSWORD="$POSTGRES_PASSWORD" exec psql --username postgres --dbname teamie_chat --tuples-only --no-align --set ON_ERROR_STOP=1 --command "SELECT to_regclass('\''public.marketplace_apps'\'') IS NOT NULL"') || return
    if [[ $has_legacy_marketplace == t ]]; then
      # Quiesce both legacy Chat and standalone Marketplace writes before copying
      # and reconciling embedded Marketplace state into teamie_marketplace.
      "${COMPOSE[@]}" stop --timeout 30 chat || return
      "${COMPOSE[@]}" stop --timeout 30 marketplace || return
      "${COMPOSE[@]}" exec -T postgres bash -s < "${COMPOSE_RECIPE}/deploy/postgres/02-migrate-chat-marketplace.sh" || return
    fi
  fi
  "${COMPOSE[@]}" up -d --no-deps --wait --wait-timeout 180 chat || return
  if [[ $services == *marketplace* ]]; then
    "${COMPOSE[@]}" up -d --no-deps --wait --wait-timeout 180 marketplace || return
  fi
  "${COMPOSE[@]}" up -d --no-deps --wait --wait-timeout 180 realtime web || return
  if [[ $services == *llm-gateway* ]]; then
    "${COMPOSE[@]}" up -d --no-deps --wait --wait-timeout 180 agent-control llm-gateway sandbox || return
    "${COMPOSE[@]}" up -d --no-deps --wait --wait-timeout 180 agent-runtime || return
  fi
  if [[ -n $compose_json && -f $compose_json && -n $manifest_tool ]]; then
    if ! python3 "$manifest_tool" service-up-to-date "$compose_json" caddy; then
      "${COMPOSE[@]}" up -d --no-deps --force-recreate --wait --wait-timeout 180 caddy || return
    else
      "${COMPOSE[@]}" up -d --no-deps --wait --wait-timeout 180 caddy || return
    fi
    python3 "$manifest_tool" record-caddy-tls "$compose_json" || return
  else
    "${COMPOSE[@]}" up -d --no-deps --wait --wait-timeout 180 caddy || return
  fi
}

rollout_online() {
  local compose_json=$1 manifest_tool=$2 services=$3
  cleanup_canary_containers
  if ! python3 "$manifest_tool" service-up-to-date "$compose_json" caddy; then
    "${COMPOSE[@]}" up -d --no-deps --force-recreate --wait --wait-timeout 180 caddy || { cleanup_canary_containers; return 1; }
    python3 "$manifest_tool" record-caddy-tls "$compose_json" || { cleanup_canary_containers; return 1; }
  fi
  local service_list
  # shellcheck disable=SC2086
  service_list=" $(printf '%s ' $services) "
  local svc
  for svc in "${ONLINE_AB_SERVICES[@]}"; do
    if [[ $service_list == *" $svc "* ]]; then
      cutover_service_ab "$svc" "$compose_json" "$manifest_tool" || { cleanup_canary_containers; return 1; }
    fi
  done
  local single_svc
  for single_svc in sandbox agent-runtime; do
    if [[ $service_list == *" $single_svc "* ]] && ! python3 "$manifest_tool" service-up-to-date "$compose_json" "$single_svc"; then
      "${COMPOSE[@]}" stop --timeout 30 "$single_svc" || { cleanup_canary_containers; return 1; }
      "${COMPOSE[@]}" up -d --no-deps --wait --wait-timeout 180 "$single_svc" || { cleanup_canary_containers; return 1; }
    fi
  done
  cleanup_canary_containers
}

rollout() {
  local target_manifest=${TEAMIE_TARGET_MANIFEST:-}
  local current_manifest=${TEAMIE_CURRENT_MANIFEST:-}
  local compose_json=${TEAMIE_COMPOSE_JSON:-}
  local common_dir
  common_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
  local manifest_tool="$common_dir/manifest.py"

  if [[ -n $target_manifest && -f $target_manifest && -n $current_manifest && -f $current_manifest && -n $compose_json && -f $compose_json ]] &&
     python3 "$manifest_tool" online-compatible "$target_manifest" "$current_manifest" &&
     python3 "$manifest_tool" service-up-to-date "$compose_json" postgres &&
     python3 "$manifest_tool" service-up-to-date "$compose_json" redis &&
     python3 "$manifest_tool" online-memory-ready "$compose_json"; then
    local services
    services=$("${COMPOSE[@]}" config --services) || return
    if [[ $services == *llm-gateway* || $services == *marketplace* ]]; then
      "${COMPOSE[@]}" exec -T postgres env TEAMIE_VERIFY_DB_CREDENTIALS=1 bash -s < "${COMPOSE_RECIPE}/deploy/postgres/01-databases.sh" || return
    fi
    local has_legacy_marketplace=f
    if [[ $services == *marketplace* ]]; then
      # shellcheck disable=SC2016
      has_legacy_marketplace=$("${COMPOSE[@]}" exec -T postgres sh -ec \
        'PGPASSWORD="$POSTGRES_PASSWORD" exec psql --username postgres --dbname teamie_chat --tuples-only --no-align --set ON_ERROR_STOP=1 --command "SELECT to_regclass('\''public.marketplace_apps'\'') IS NOT NULL"') || return
    fi
    if [[ $has_legacy_marketplace != t ]]; then
      rollout_online "$compose_json" "$manifest_tool" "$services"
      return
    fi
  fi

  cleanup_canary_containers
  rollout_fenced "$compose_json" "$manifest_tool"
}
