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
  "${COMPOSE[@]}" up -d --no-deps --wait --wait-timeout 180 caddy || return
}
