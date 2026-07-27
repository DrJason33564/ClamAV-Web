#!/bin/sh
set -eu

CLAMD_WAIT_RETRIES=240
CLAMD_WAIT_INTERVAL=5

CRON_CONFIG_FILE="${CRON_CONFIG_FILE:-/config/cron_scan.conf}"
EXCLUDE_CONFIG_FILE="${EXCLUDE_CONFIG_FILE:-/config/exclude.conf}"
EXAMPLE_CONFIG="${CRON_SCAN_EXAMPLE:-/cron_scan_example.conf}"

LOG_DIR="${SCAN_LOG_DIR:-/log}"
STARTUP_LOG="${STARTUP_LOG_FILE:-${LOG_DIR}/startup.log}"
STATUS_DIR="${STATUS_DIR:-/state}"
JOBS_DIR="${JOBS_DIR:-${STATUS_DIR}/jobs}"
STATUS_FILE="${STATUS_FILE:-${STATUS_DIR}/status.json}"
QUARANTINE_DIR="${QUARANTINE_DIR:-/quarantine}"

LOG_SCRIPT="${LOG_SCRIPT:-/log.sh}"
CONFIG_SCRIPT="${CONFIG_SCRIPT:-/config.sh}"
EXCLUDE_SCRIPT="${EXCLUDE_SCRIPT:-/exclude.sh}"
CRON_SCRIPT="${CRON_SCRIPT:-/cron.sh}"
SCAN_ONCE_SCRIPT="${SCAN_ONCE_SCRIPT:-/scan_once.sh}"
INIT_CMD="${CLAMAV_INIT:-/init}"
SCANNER_CMD="${SCANNER_CMD:-/server}"
STOP_GRACE_SECONDS="${STOP_GRACE_SECONDS:-5}"

CONFIG_LOG_FILE="$STARTUP_LOG"
export CRON_CONFIG_FILE EXCLUDE_CONFIG_FILE LOG_SCRIPT CONFIG_SCRIPT

. "$LOG_SCRIPT"
. "$CONFIG_SCRIPT"

json_escape() {
    printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'
}

now_iso() {
    date '+%Y-%m-%dT%H:%M:%S%z'
}

startup_log() {
    log_line "$STARTUP_LOG" "$@"
}

fail() {
    startup_log "[ERROR] $*"
    exit 1
}

write_ready_status() {
    message="$1"
    tmp_status="$(mktemp "${STATUS_DIR}/.status.XXXXXX")"

    # Keep the base status file available before the WebUI exists. Scan jobs
    # will update the scan section while they are running and after completion.
    cat > "$tmp_status" <<EOF_STATUS
{
  "version": 1,
  "updated_at": "$(now_iso)",
  "clamd": {
    "status": "ready",
    "last_checked_at": "$(now_iso)",
    "message": "$(json_escape "$message")"
  },
  "scan": {
    "active_job_id": null,
    "last_job_id": null
  }
}
EOF_STATUS

    mv "$tmp_status" "$STATUS_FILE"
}

wait_for_clamd() {
    startup_log "[INFO] Waiting for clamd to become ready..."

    i=0
    while [ "$i" -lt "${CLAMD_WAIT_RETRIES:-180}" ]; do
        if clamdscan --config-file=/etc/clamav/clamd.conf --ping=1 >/dev/null 2>&1; then
            startup_log "[INFO] clamd is ready."
            write_ready_status "clamd is ready."
            return 0
        fi

        i=$((i + 1))
        sleep "${CLAMD_WAIT_INTERVAL:-2}"
    done

    fail "clamd did not become ready in time."
}

send_term_to_child_process() {
    process_name="$1"
    process_pid="$2"

    [ -n "$process_pid" ] || return 0

    if ! kill -0 "$process_pid" 2>/dev/null; then
        return 0
    fi

    startup_log "[INFO] Stopping ${process_name}: ${process_pid}"
    kill "$process_pid" 2>/dev/null || true
}

force_kill_child_process() {
    process_name="$1"
    process_pid="$2"

    [ -n "$process_pid" ] || return 0

    if ! kill -0 "$process_pid" 2>/dev/null; then
        return 0
    fi

    startup_log "[WARN] ${process_name} did not stop within ${STOP_GRACE_SECONDS}s, killing: ${process_pid}"
    kill -KILL "$process_pid" 2>/dev/null || true
}

wait_child_process() {
    process_pid="$1"

    [ -n "$process_pid" ] || return 0
    wait "$process_pid" 2>/dev/null || true
}

cleanup() {
    startup_log "[INFO] Stopping services..."

    # Stop the WebUI first so it cannot accept new scan or config requests
    # while the container is already shutting down.
    send_term_to_child_process "scanner web service" "${SCANNER_PID:-}"

    # Stop cron before ClamAV to avoid a scheduled scan being launched during
    # shutdown.
    send_term_to_child_process "cron" "${CRON_PID:-}"

    # The official ClamAV init process owns clamd/freshclam startup. Stopping
    # it gives ClamAV a chance to exit cleanly before PID 1 returns.
    send_term_to_child_process "official ClamAV init" "${INIT_PID:-}"

    # Keep shutdown bounded. Docker will send SIGKILL after its own stop
    # timeout, which makes the container report 137 even if SIGTERM was trapped.
    sleep "$STOP_GRACE_SECONDS"

    force_kill_child_process "scanner web service" "${SCANNER_PID:-}"
    force_kill_child_process "cron" "${CRON_PID:-}"
    force_kill_child_process "official ClamAV init" "${INIT_PID:-}"

    wait_child_process "${SCANNER_PID:-}"
    wait_child_process "${CRON_PID:-}"
    wait_child_process "${INIT_PID:-}"
}

shutdown() {
    trap - INT TERM
    cleanup
    startup_log "[INFO] Services stopped. Exiting with code 0."
    exit 0
}

prepare_config_files() {
    mkdir -p \
        "$(dirname "$CRON_CONFIG_FILE")" \
        "$(dirname "$EXCLUDE_CONFIG_FILE")" \
        "$LOG_DIR" \
        "$STATUS_DIR" \
        "$JOBS_DIR" \
        "$QUARANTINE_DIR"

    touch "$STARTUP_LOG"

    if [ ! -f "$EXCLUDE_CONFIG_FILE" ]; then
        : > "$EXCLUDE_CONFIG_FILE"
        startup_log "[INFO] Created empty exclude config: $EXCLUDE_CONFIG_FILE"
    fi

    if [ ! -f "$CRON_CONFIG_FILE" ]; then
        startup_log "[WARN] Config file not found: $CRON_CONFIG_FILE"

        if [ ! -f "$EXAMPLE_CONFIG" ]; then
            fail "Example config file not found: $EXAMPLE_CONFIG"
        fi

        cp "$EXAMPLE_CONFIG" "$CRON_CONFIG_FILE"

        startup_log "[INFO] Released example config to: $CRON_CONFIG_FILE"
        startup_log "[INFO] Continuing startup with the released default config."
    fi
}

validate_startup_inputs() {
    validate_cron_config "$CRON_CONFIG_FILE" || fail "Config validation failed: $CRON_CONFIG_FILE"

    [ -r "$EXCLUDE_CONFIG_FILE" ] || fail "Exclude config file is not readable: $EXCLUDE_CONFIG_FILE"
    [ -x "$EXCLUDE_SCRIPT" ] || fail "Exclude script is not executable: $EXCLUDE_SCRIPT"
    [ -x "$CRON_SCRIPT" ] || fail "Cron script is not executable: $CRON_SCRIPT"
    [ -x "$SCAN_ONCE_SCRIPT" ] || fail "Scan script is not executable: $SCAN_ONCE_SCRIPT"
    [ -x "$SCANNER_CMD" ] || fail "Scanner web service is not executable: $SCANNER_CMD"
}

trap shutdown INT TERM

startup_log "[INFO] startup.sh started."

prepare_config_files
validate_startup_inputs

startup_log "[INFO] Config validation passed. Executing exclude script: $EXCLUDE_SCRIPT"
"$EXCLUDE_SCRIPT"

startup_log "[INFO] Starting official ClamAV init: $INIT_CMD"
"$INIT_CMD" &
INIT_PID="$!"

wait_for_clamd

# cron.sh owns both cron file generation and cron daemon startup so WebUI can
# reuse the same reload path later without duplicating shell logic in Go.
startup_log "[INFO] Loading cron rules through: $CRON_SCRIPT reload"
"$CRON_SCRIPT" reload

startup_log "[INFO] Starting cron through: $CRON_SCRIPT start"
"$CRON_SCRIPT" start &
CRON_PID="$!"

startup_log "[INFO] Starting web service: $SCANNER_CMD"
"$SCANNER_CMD" &
SCANNER_PID="$!"

while :; do
    if ! kill -0 "$INIT_PID" 2>/dev/null; then
        startup_log "[ERROR] Official ClamAV init process exited. Container will stop."
        cleanup
        exit 1
    fi

    if ! kill -0 "$CRON_PID" 2>/dev/null; then
        startup_log "[ERROR] cron process exited. Container will stop."
        cleanup
        exit 1
    fi

    if ! kill -0 "$SCANNER_PID" 2>/dev/null; then
        startup_log "[ERROR] web service exited. Container will stop."
        cleanup
        exit 1
    fi

    sleep 5
done
