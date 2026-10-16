#!/usr/bin/env bash
# Shared host paths. Callers set umask 077 before sourcing.
TEAMIE_ROOT=${TEAMIE_ROOT:-/srv/teamie}
[[ $TEAMIE_ROOT =~ ^/[a-zA-Z0-9/_-]+$ ]] || { echo 'Invalid TEAMIE_ROOT' >&2; exit 1; }
HOST_ENV="$TEAMIE_ROOT/config/production.env"
export COMPOSE_PARALLEL_LIMIT=1

compose_for() {
  local recipe=$1 image_env=$2
  COMPOSE_RECIPE=$recipe
  COMPOSE=(docker compose --project-name teamie --file "$recipe/deploy/compose.production.yaml"
    --env-file "$HOST_ENV" --env-file "$image_env")
}

rollout() {
  # Every service migration is forward-only and runs before its /readyz is healthy.
  "${COMPOSE[@]}" up -d --no-deps --wait --wait-timeout 180 postgres redis || return
  if [[ $("${COMPOSE[@]}" config --services) == *llm-gateway* ]]; then
    "${COMPOSE[@]}" exec -T postgres env TEAMIE_VERIFY_DB_CREDENTIALS=1 bash -s < "${COMPOSE_RECIPE}/deploy/postgres/01-databases.sh" || return
    # Do not let an old Session writer call newly migrated/replaced dependencies.
    # Async children remain owned by Docker and their Sandbox receipts survive.
    "${COMPOSE[@]}" stop --timeout 30 agent-runtime || return
  fi
  "${COMPOSE[@]}" up -d --no-deps --wait --wait-timeout 180 identity || return
  "${COMPOSE[@]}" up -d --no-deps --wait --wait-timeout 180 chat || return
  "${COMPOSE[@]}" up -d --no-deps --wait --wait-timeout 180 realtime web || return
  if [[ $("${COMPOSE[@]}" config --services) == *llm-gateway* ]]; then
    "${COMPOSE[@]}" up -d --no-deps --wait --wait-timeout 180 agent-control llm-gateway sandbox || return
    "${COMPOSE[@]}" up -d --no-deps --wait --wait-timeout 180 agent-runtime || return
  fi
  "${COMPOSE[@]}" up -d --no-deps --wait --wait-timeout 180 caddy || return
}
