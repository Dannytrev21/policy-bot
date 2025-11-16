#!/usr/bin/env bash

set -euo pipefail

COMMAND=${1:-help}
LOCALSTACK_CONTAINER_NAME=${LOCALSTACK_CONTAINER_NAME:-policy-bot-localstack}
LOCALSTACK_IMAGE=${LOCALSTACK_IMAGE:-localstack/localstack}
LOCALSTACK_PORT=${LOCALSTACK_PORT:-4566}
LOCALSTACK_URL=${LOCALSTACK_URL:-http://localhost:${LOCALSTACK_PORT}}
LOCALSTACK_WAIT_TIMEOUT=${LOCALSTACK_WAIT_TIMEOUT:-30}
QUEUE_NAMES=(
  "github-installation"
  "github-pull-request"
  "github-pull-request-review"
  "github-issue-comment"
  "github-status"
  "github-check-run"
)

usage() {
  cat <<USAGE
Usage: $0 <command>

Commands:
  start    Start LocalStack and create required queues
  stop     Stop LocalStack container if running
  status   Display LocalStack status and queue readiness
  queues   Create or update required queues only

Environment variables:
  LOCALSTACK_CONTAINER_NAME  (default: policy-bot-localstack)
  LOCALSTACK_IMAGE           (default: localstack/localstack)
  LOCALSTACK_PORT            (default: 4566)
  LOCALSTACK_URL             (default: http://localhost:4566)
  LOCALSTACK_WAIT_TIMEOUT    (default: 30 seconds)
USAGE
}

require_docker() {
  if ! command -v docker >/dev/null 2>&1; then
    echo "docker is required for this command" >&2
    exit 1
  fi
}

localstack_running() {
  docker ps --filter "name=${LOCALSTACK_CONTAINER_NAME}" --filter "status=running" --format '{{.ID}}' | grep -q .
}

wait_for_localstack() {
  local elapsed=0
  while ! curl -sf "${LOCALSTACK_URL}" >/dev/null 2>&1; do
    if (( elapsed >= LOCALSTACK_WAIT_TIMEOUT )); then
      echo "LocalStack did not become ready within ${LOCALSTACK_WAIT_TIMEOUT}s" >&2
      return 1
    fi
    sleep 1
    ((elapsed++))
  done
  echo "LocalStack is ready at ${LOCALSTACK_URL}"
}

create_queues() {
  if ! command -v aws >/dev/null 2>&1; then
    echo "aws CLI is required to manage queues" >&2
    exit 1
  fi

  export AWS_ACCESS_KEY_ID=${AWS_ACCESS_KEY_ID:-test}
  export AWS_SECRET_ACCESS_KEY=${AWS_SECRET_ACCESS_KEY:-test}
  export AWS_DEFAULT_REGION=${AWS_DEFAULT_REGION:-us-east-1}

  for queue in "${QUEUE_NAMES[@]}"; do
    echo "Ensuring queue: ${queue}"
    aws sqs create-queue \
      --queue-name "${queue}" \
      --endpoint-url "${LOCALSTACK_URL}" \
      >/dev/null
  done

  echo "Queues ready at ${LOCALSTACK_URL}"
}

case "${COMMAND}" in
  start)
    require_docker
    if localstack_running; then
      echo "LocalStack already running"
    else
      echo "Starting LocalStack container ${LOCALSTACK_CONTAINER_NAME}..."
      docker run -d --rm \
        -p "${LOCALSTACK_PORT}:4566" \
        --name "${LOCALSTACK_CONTAINER_NAME}" \
        "${LOCALSTACK_IMAGE}" >/dev/null
    fi

    wait_for_localstack
    create_queues
    ;;
  stop)
    require_docker
    if localstack_running; then
      echo "Stopping LocalStack..."
      docker stop "${LOCALSTACK_CONTAINER_NAME}" >/dev/null
    else
      echo "LocalStack not running"
    fi
    ;;
  status)
    if command -v docker >/dev/null 2>&1 && localstack_running; then
      echo "LocalStack container ${LOCALSTACK_CONTAINER_NAME} is running"
    else
      echo "LocalStack container ${LOCALSTACK_CONTAINER_NAME} is not running"
    fi
    if curl -sf "${LOCALSTACK_URL}" >/dev/null 2>&1; then
      echo "LocalStack endpoint reachable at ${LOCALSTACK_URL}"
    else
      echo "LocalStack endpoint not reachable at ${LOCALSTACK_URL}" >&2
      exit 1
    fi
    ;;
  queues)
    wait_for_localstack
    create_queues
    ;;
  help|--help|-h)
    usage
    ;;
  *)
    echo "Unknown command: ${COMMAND}" >&2
    usage
    exit 1
    ;;

esac
