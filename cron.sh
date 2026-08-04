#!/bin/sh
set -eu

CRON_CONFIG_FILE="${CRON_CONFIG_FILE:-/config/cron_scan.conf}"
CRON_FILE="${CRON_FILE:-/etc/cron.d/clamav-scheduled-scan}"

LOG_DIR="${SCAN_LOG_DIR:-/log}"
CRON_LOG_FILE="${CRON_LOG_FILE:-${LOG_DIR}/cron.log}"
CONFIG_LOG_FILE="$CRON_LOG_FILE"

STATUS_DIR="${STATUS_DIR:-/state}"
JOBS_DIR="${JOBS_DIR:-${STATUS_DIR}/jobs}"
STATUS_FILE="${STATUS_FILE:-${STATUS_DIR}/status.json}"
SCAN_LOCK_DIR="${SCAN_LOCK_DIR:-${STATUS_DIR}/scan.lock}"
SLEEP_LOCK_DIR="${SLEEP_LOCK_DIR:-${STATUS_DIR}/sleep.lock}"
SCAN_WAIT_INTERVAL="${SCAN_WAIT_INTERVAL:-${CRON_WAIT_INTERVAL:-30}}"
SCAN_WAIT_MAX_SECONDS="${SCAN_WAIT_MAX_SECONDS:-${CRON_WAIT_MAX_SECONDS:-0}}"
SCAN_ONCE_SCRIPT="${SCAN_ONCE_SCRIPT:-/scan_once.sh}"
STARTUP_SCRIPT="${STARTUP_SCRIPT:-/startup.sh}"
QUARANTINE_DIR="${QUARANTINE_DIR:-/quarantine}"
CLAMD_CONF="${CLAMD_CONF:-/etc/clamav/clamd.conf}"

. "${LOG_SCRIPT:-/log.sh}"
. "${CONFIG_SCRIPT:-/config.sh}"

usage() {
    echo "Usage: $0 <validate [config_file]|reload|start>" >&2
}

write_cron_header() {
    : > "$CRON_FILE"

    # Debian /etc/cron.d files do not inherit the container environment, so
    # pass the timezone, scan paths, and state paths explicitly to jobs.
    {
        echo "SHELL=/bin/sh"
        echo "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
        if [ -n "${TZ:-}" ]; then
            echo "TZ=$TZ"
        fi
        echo "SCAN_LOG_DIR=$LOG_DIR"
        echo "SCAN_LOG_MAX_BYTES=${SCAN_LOG_MAX_BYTES:-5242880}"
        echo "QUARANTINE_DIR=$QUARANTINE_DIR"
        echo "STATUS_DIR=$STATUS_DIR"
        echo "JOBS_DIR=$JOBS_DIR"
        echo "STATUS_FILE=$STATUS_FILE"
        echo "SCAN_LOCK_DIR=$SCAN_LOCK_DIR"
        echo "SLEEP_LOCK_DIR=$SLEEP_LOCK_DIR"
        echo "SCAN_WAIT_INTERVAL=$SCAN_WAIT_INTERVAL"
        echo "SCAN_WAIT_MAX_SECONDS=$SCAN_WAIT_MAX_SECONDS"
        echo "SCAN_ONCE_SCRIPT=$SCAN_ONCE_SCRIPT"
        echo "STARTUP_SCRIPT=$STARTUP_SCRIPT"
        echo "CLAMD_CONF=$CLAMD_CONF"
        echo ""
    } >> "$CRON_FILE"
}

reload_cron() {
    mkdir -p "$(dirname "$CRON_FILE")" "$LOG_DIR" "$STATUS_DIR" "$JOBS_DIR"

    # Reload validates the live config even if the caller already validated a
    # temporary file. This keeps manual edits and future WebUI saves safe.
    validate_cron_config "$CRON_CONFIG_FILE"
    rules_count="$VALID_CRON_RULES_COUNT"

    write_cron_header

    line_no=0
    while IFS= read -r line || [ -n "$line" ]; do
        line_no=$((line_no + 1))

        case "$line" in
            ""|\#*) continue ;;
        esac

        split_cron_config_line "$line" "$line_no"

        if [ ! -e "$CRON_TARGET" ]; then
            log_line "$CRON_LOG_FILE" "[WARN] Config line $line_no target does not exist now: $CRON_TARGET"
        fi

        # cron invokes /bin/sh; single-quote escaping keeps paths with spaces
        # intact when scan_once.sh receives its target and action arguments.
        escaped_scan_script="$(printf "%s" "$SCAN_ONCE_SCRIPT" | sed "s/'/'\\\\''/g")"
        escaped_target="$(printf "%s" "$CRON_TARGET" | sed "s/'/'\\\\''/g")"
        escaped_action="$(printf "%s" "$CRON_ACTION" | sed "s/'/'\\\\''/g")"

        printf "%s %s %s %s %s root '%s' --type cron --target '%s' --action '%s' --wait\n" \
            "$CRON_MINUTE" "$CRON_HOUR" "$CRON_DAY" "$CRON_MONTH" "$CRON_WEEKDAY" \
            "$escaped_scan_script" "$escaped_target" "$escaped_action" \
            >> "$CRON_FILE"

        log_line "$CRON_LOG_FILE" "[INFO] Registered cron scan: $CRON_MINUTE $CRON_HOUR $CRON_DAY $CRON_MONTH $CRON_WEEKDAY -> $CRON_TARGET [$CRON_ACTION]"
    done < "$CRON_CONFIG_FILE"

    chmod 0644 "$CRON_FILE"
    log_line "$CRON_LOG_FILE" "[INFO] Cron rules reloaded: $rules_count rule(s)."
    echo "Cron rules reloaded: $rules_count rule(s)."
}

start_cron() {
    mkdir -p "$LOG_DIR"
    log_line "$CRON_LOG_FILE" "[INFO] Starting Debian cron..."
    # Keep cron in the foreground so startup.sh can monitor this process.
    exec cron -f
}

command="${1:-}"
case "$command" in
    validate)
        config_file="${2:-$CRON_CONFIG_FILE}"
        validate_cron_config "$config_file"
        echo "Cron config is valid: $VALID_CRON_RULES_COUNT rule(s)."
        ;;
    reload)
        reload_cron
        ;;
    start)
        start_cron
        ;;
    *)
        usage
        exit 2
        ;;
esac
