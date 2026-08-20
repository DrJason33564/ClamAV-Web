#!/bin/sh
set -eu

LOG_DIR="${SCAN_LOG_DIR:-/log}"
STATUS_DIR="${STATUS_DIR:-/state}"
JOBS_DIR="${JOBS_DIR:-${STATUS_DIR}/jobs}"
STATUS_FILE="${STATUS_FILE:-${STATUS_DIR}/status.json}"
LOCK_DIR="${SCAN_LOCK_DIR:-${STATUS_DIR}/scan.lock}"
SLEEP_LOCK_DIR="${SLEEP_LOCK_DIR:-${STATUS_DIR}/sleep.lock}"
QUARANTINE_DIR="${QUARANTINE_DIR:-/quarantine}"
CLAMD_CONF="${CLAMD_CONF:-/etc/clamav/clamd.conf}"
STARTUP_SCRIPT="${STARTUP_SCRIPT:-/startup.sh}"
LOG_SCRIPT="${LOG_SCRIPT:-/log.sh}"

. "$LOG_SCRIPT"

usage() {
    echo "Usage: $0 [--id <job_id>] --type <manual|cron> -u <user_id> --target <path> --action <warn|move|remove> [--wait] [--wake]" >&2
}

json_escape() {
    printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'
}

now_unix() {
    date '+%s'
}

now_iso() {
    date '+%Y-%m-%dT%H:%M:%S%z'
}

write_status() {
    clamd_status="$1"
    message="$2"
    tmp_status="$(mktemp "${STATUS_DIR}/.status.XXXXXX")"

    cat > "$tmp_status" <<EOF_STATUS
{
  "version": 1,
  "updated_at": "$(now_iso)",
  "clamd": {
    "status": "$clamd_status",
    "last_checked_at": "$(now_iso)",
    "message": "$(json_escape "$message")"
  },
  "scan": {
    "active_job_id": ${ACTIVE_JOB_JSON:-null},
    "last_job_id": ${LAST_JOB_JSON:-null}
  }
}
EOF_STATUS

    mv "$tmp_status" "$STATUS_FILE"
}

write_job_state() {
    status="$1"
    finished_at="$2"
    exit_code_json="$3"
    result="$4"
    detection_log_json="$5"
    message="$6"

    tmp_job="$(mktemp "${JOBS_DIR}/.${JOB_ID}.XXXXXX")"

    cat > "$tmp_job" <<EOF_JOB
{
  "version": 3,
  "job_id": "$(json_escape "$JOB_ID")",
  "type": "$(json_escape "$JOB_TYPE")",
  "user_id": "$(json_escape "$JOB_USER_ID")",
  "status": "$status",
  "target": "$(json_escape "$TARGET")",
  "action": "$ACTION",
  "pid": ${CLAMDSCAN_PID_JSON:-null},
  "started_at": $STARTED_AT,
  "finished_at": $finished_at,
  "exit_code": $exit_code_json,
  "result": "$result",
  "log_file": "$(json_escape "$JOB_LOG")",
  "detection_log": $detection_log_json,
  "message": "$(json_escape "$message")"
}
EOF_JOB

    mv "$tmp_job" "$JOB_STATE"
}

log() {
    log_line "$JOB_LOG" "$@"
}

append_file_with_timestamp() {
    input_file="$1"

    while IFS= read -r line || [ -n "$line" ]; do
        log "$line"
    done < "$input_file"
}

action_args() {
    case "$ACTION" in
        warn|move)
            printf '%s' ''
            ;;
        remove)
            printf '%s' "--remove=yes"
            ;;
    esac
}

log_scan_action() {
    case "$ACTION" in
        warn)
            log "[INFO] Detection action: warn only"
            ;;
        move)
            log "[INFO] Detection action: move infected files to $QUARANTINE_DIR"
            ;;
        remove)
            log "[INFO] Detection action: remove infected files directly"
            ;;
    esac
}

log_scan_header() {
    [ "${SCAN_HEADER_LOGGED:-0}" -eq 0 ] || return 0

    log "[INFO] Scan started: $TARGET"
    log "[INFO] Job id: $JOB_ID"
    log "[INFO] Job type: $JOB_TYPE"
    log "[INFO] Job owner ID: $JOB_USER_ID"
    log_scan_action
    SCAN_HEADER_LOGGED=1
}

fail_scan_job() {
    log_message="$1"
    job_message="$2"
    exit_code="$3"

    log "$log_message"
    FINISHED_AT="$(now_unix)"
    ACTIVE_JOB_JSON="null"
    write_job_state "failed" "$FINISHED_AT" "$exit_code" "error" "null" "$job_message"
    exit "$exit_code"
}

log_applied_action() {
    case "$ACTION" in
        warn)
            log "[ALERT] Action applied: warn only"
            ;;
        move)
            log "[ALERT] Action applied: move to $QUARANTINE_DIR"
            ;;
        remove)
            log "[ALERT] Action applied: remove infected files"
            ;;
    esac
}

write_detection_line() {
    log_rotate_if_needed "$DETECTION_LOG"
    printf '%s %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$1" >> "$DETECTION_LOG"
}

path_exists() {
    [ -e "$1" ] || [ -L "$1" ]
}

byte_length() {
    LC_ALL=C printf '%s' "$1" | wc -c | tr -d ' '
}

command_error_text() {
    tr '\n' ' ' < "$MOVE_ERROR_FILE" | sed 's/[[:space:]]*$//'
}

cleanup_failed_quarantine_target() {
    failed_target="$1"

    if ! path_exists "$failed_target"; then
        return 0
    fi
    if rm -f "$failed_target" 2> "$MOVE_ERROR_FILE"; then
        return 0
    fi

    log "[ERROR] Failed to clean incomplete quarantine target: $failed_target; $(command_error_text)"
    return 1
}

verify_completed_move() {
    verify_source="$1"
    verify_target="$2"

    ! path_exists "$verify_source" && path_exists "$verify_target"
}

prepare_move_fallback() {
    fallback_source="$1"
    fallback_target="$2"

    # If the source disappeared, the target may be the only remaining copy.
    # Preserve it for manual recovery instead of deleting potentially unique data.
    if ! path_exists "$fallback_source"; then
        log "[ERROR] Source disappeared during quarantine fallback; preserving target if present: $fallback_target"
        return 1
    fi
    cleanup_failed_quarantine_target "$fallback_target"
}

move_detected_file() {
    move_source="$1"
    move_target="$2"
    MOVE_METHOD=""

    if ! path_exists "$move_source"; then
        log "[ERROR] Quarantine failed: source file no longer exists: $move_source"
        return 1
    fi

    if cp -l "$move_source" "$move_target" 2> "$MOVE_ERROR_FILE"; then
        if rm "$move_source" 2> "$MOVE_ERROR_FILE"; then
            if verify_completed_move "$move_source" "$move_target"; then
                MOVE_METHOD="hard-link"
                return 0
            fi
            log "[ERROR] Hard-link move returned success but verification failed: source=$move_source target=$move_target"
            return 1
        fi
        log "[WARN] Hard-link created but source deletion failed; falling back to copy: source=$move_source target=$move_target; $(command_error_text)"
    else
        log "[WARN] Hard-link move failed; falling back to copy: source=$move_source target=$move_target; $(command_error_text)"
    fi
    if ! prepare_move_fallback "$move_source" "$move_target"; then
        return 1
    fi

    if cp -p "$move_source" "$move_target" 2> "$MOVE_ERROR_FILE"; then
        if rm "$move_source" 2> "$MOVE_ERROR_FILE"; then
            if verify_completed_move "$move_source" "$move_target"; then
                MOVE_METHOD="copy"
                return 0
            fi
            log "[ERROR] Copy move returned success but verification failed: source=$move_source target=$move_target"
            return 1
        fi
        log "[WARN] Copy completed but source deletion failed; falling back to mv: source=$move_source target=$move_target; $(command_error_text)"
    else
        log "[WARN] Copy move failed; falling back to mv: source=$move_source target=$move_target; $(command_error_text)"
    fi
    if ! prepare_move_fallback "$move_source" "$move_target"; then
        return 1
    fi

    if mv "$move_source" "$move_target" 2> "$MOVE_ERROR_FILE"; then
        if verify_completed_move "$move_source" "$move_target"; then
            MOVE_METHOD="mv"
            return 0
        fi
        log "[ERROR] mv returned success but verification failed: source=$move_source target=$move_target"
        return 1
    fi

    log "[ERROR] mv fallback failed: source=$move_source target=$move_target; $(command_error_text)"
    if path_exists "$move_source"; then
        cleanup_failed_quarantine_target "$move_target" || return 1
    else
        log "[ERROR] Source disappeared after mv failure; preserving target if present: $move_target"
    fi
    return 1
}

reserve_quarantine_name() {
    reserve_source="$1"
    reserve_base="$(basename "$reserve_source" 2>/dev/null || printf '%s' "$reserve_source")"
    reserve_index=0

    case "$reserve_base" in
        *.rec|clamav-quarantine-lock)
            reserve_index=1
            ;;
    esac

    while :; do
        if [ "$reserve_index" -eq 0 ]; then
            reserve_name="$reserve_base"
        else
            reserve_suffix="$(printf '%03d' "$reserve_index")"
            reserve_name="${reserve_base}.${reserve_suffix}"
        fi
        reserve_record_name="${reserve_name}.rec"

        if [ "$(byte_length "$reserve_name")" -gt "$QUARANTINE_NAME_MAX" ] || \
           [ "$(byte_length "$reserve_record_name")" -gt "$QUARANTINE_NAME_MAX" ]; then
            log "[ERROR] Quarantine failed: target filename exceeds NAME_MAX=${QUARANTINE_NAME_MAX}: $reserve_name"
            return 1
        fi

        reserve_target="${QUARANTINE_DIR}/${reserve_name}"
        reserve_record="${QUARANTINE_DIR}/${reserve_record_name}"
        if path_exists "$reserve_target" || path_exists "$reserve_record"; then
            reserve_index=$((reserve_index + 1))
            continue
        fi

        # An empty record is an invalid, invisible reservation. The valid record
        # is atomically published only after the file has moved successfully.
        if (set -C; : > "$reserve_record") 2>/dev/null; then
            QUARANTINE_TARGET="$reserve_target"
            QUARANTINE_RECORD="$reserve_record"
            return 0
        fi
        if path_exists "$reserve_target" || path_exists "$reserve_record"; then
            reserve_index=$((reserve_index + 1))
            continue
        fi

        log "[ERROR] Quarantine failed: cannot reserve target record: $reserve_record"
        return 1
    done
}

quarantine_detected_file() {
    quarantine_source="$1"
    QUARANTINE_TARGET=""
    QUARANTINE_RECORD=""

    if ! reserve_quarantine_name "$quarantine_source"; then
        return 1
    fi

    if ! quarantine_tmp_record="$(mktemp "${QUARANTINE_DIR}/.quarantine-rec.XXXXXX")"; then
        log "[ERROR] Quarantine failed: cannot create temporary metadata for: $quarantine_source"
        rm -f "$QUARANTINE_RECORD" 2>/dev/null || true
        return 1
    fi
    if ! printf '"%s" %s\n' "$quarantine_source" "$JOB_USER_ID" > "$quarantine_tmp_record"; then
        log "[ERROR] Quarantine failed: cannot write temporary metadata for: $quarantine_source"
        rm -f "$quarantine_tmp_record" "$QUARANTINE_RECORD" 2>/dev/null || true
        return 1
    fi

    if ! move_detected_file "$quarantine_source" "$QUARANTINE_TARGET"; then
        rm -f "$quarantine_tmp_record" "$QUARANTINE_RECORD" 2>/dev/null || true
        log "[ERROR] Quarantine failed: source=$quarantine_source target=$QUARANTINE_TARGET"
        return 1
    fi

    if mv "$quarantine_tmp_record" "$QUARANTINE_RECORD" 2> "$MOVE_ERROR_FILE"; then
        log "[INFO] Quarantine succeeded: source=$quarantine_source target=$QUARANTINE_TARGET method=$MOVE_METHOD"
        log "[INFO] Quarantine source record saved to: $QUARANTINE_RECORD"
        return 0
    fi

    log "[ERROR] Failed to publish quarantine metadata: source=$quarantine_source target=$QUARANTINE_TARGET record=$QUARANTINE_RECORD; $(command_error_text)"
    rm -f "$quarantine_tmp_record" "$QUARANTINE_RECORD" 2>/dev/null || true
    if mv "$QUARANTINE_TARGET" "$quarantine_source" 2> "$MOVE_ERROR_FILE"; then
        log "[WARN] Quarantine move rolled back after metadata failure: target=$QUARANTINE_TARGET source=$quarantine_source"
    else
        log "[ERROR] Failed to roll back quarantined file after metadata failure: target=$QUARANTINE_TARGET source=$quarantine_source; $(command_error_text)"
    fi
    return 1
}

log_detections_from_output() {
    input_file="$1"
    found=0

    while IFS= read -r line || [ -n "$line" ]; do
        case "$line" in
            *" FOUND")
                found=1
                DETECTION_COUNT=$((DETECTION_COUNT + 1))
                source_file="$(printf '%s\n' "$line" | sed 's/: [^:]* FOUND$//')"
                detection_reason="$(printf '%s\n' "$line" | sed 's/^.*: //; s/ FOUND$//')"
                source_dir="$(dirname "$source_file" 2>/dev/null || printf '%s' "unknown")"

                write_detection_line "------------ ClamAV Detection ------------"
                write_detection_line "[DETECTION] Source file      : $source_file"
                write_detection_line "[DETECTION] Source directory : $source_dir"
                write_detection_line "[DETECTION] Detection reason : $detection_reason"
                write_detection_line "[DETECTION] Quarantine dir   : $QUARANTINE_DIR"
                write_detection_line "------------------------------------------"
                if [ "$ACTION" = "move" ]; then
                    if quarantine_detected_file "$source_file"; then
                        QUARANTINE_SUCCESS_COUNT=$((QUARANTINE_SUCCESS_COUNT + 1))
                    else
                        QUARANTINE_FAILURE_COUNT=$((QUARANTINE_FAILURE_COUNT + 1))
                    fi
                fi
                ;;
            *)
                log "$line"
                ;;
        esac
    done < "$input_file"

    if [ "$found" -eq 1 ]; then
        log "[ALERT] Detection details saved to: $DETECTION_LOG"
    fi
}

cleanup_lock() {
    if [ "${LOCK_HELD:-0}" -eq 1 ]; then
        rm -f "$LOCK_DIR/pid" "$LOCK_DIR/job_id" 2>/dev/null || true
        rmdir "$LOCK_DIR" 2>/dev/null || true
    fi
}

make_job_id() {
    base="${JOB_TYPE}-$(now_unix)"
    candidate="$base"
    i=1

    while [ -e "${JOBS_DIR}/${candidate}.json" ]; do
        suffix="$(printf '%03d' "$i")"
        candidate="${base}-${suffix}"
        i=$((i + 1))
    done

    printf '%s' "$candidate"
}

cleanup_stale_lock_if_needed() {
    [ -d "$LOCK_DIR" ] || return 0
    if [ ! -f "$LOCK_DIR/pid" ]; then
        rm -f "$LOCK_DIR/job_id" 2>/dev/null || true
        rmdir "$LOCK_DIR" 2>/dev/null || true
        return 0
    fi

    stale_pid="$(cat "$LOCK_DIR/pid" 2>/dev/null || true)"
    if [ -z "$stale_pid" ]; then
        rm -f "$LOCK_DIR/pid" "$LOCK_DIR/job_id" 2>/dev/null || true
        rmdir "$LOCK_DIR" 2>/dev/null || true
        return 0
    fi

    if kill -0 "$stale_pid" 2>/dev/null; then
        return 1
    fi

    stale_job_id="$(cat "$LOCK_DIR/job_id" 2>/dev/null || true)"
    if [ -n "$stale_job_id" ]; then
        stale_job_file="${JOBS_DIR}/${stale_job_id}.json"
        if [ -f "$stale_job_file" ] && grep -q '"status": "running"' "$stale_job_file"; then
            rm -f "$stale_job_file"
        fi
    fi

    rm -f "$LOCK_DIR/pid" "$LOCK_DIR/job_id" 2>/dev/null || true
    rmdir "$LOCK_DIR" 2>/dev/null || true
    return 0
}

acquire_scan_lock() {
    waited=0
    wait_interval="${SCAN_WAIT_INTERVAL:-${CRON_WAIT_INTERVAL:-30}}"
    wait_max_seconds="${SCAN_WAIT_MAX_SECONDS:-${CRON_WAIT_MAX_SECONDS:-0}}"

    while ! mkdir "$LOCK_DIR" 2>/dev/null; do
        if cleanup_stale_lock_if_needed; then
            continue
        fi

        if [ "$WAIT_FOR_LOCK" -ne 1 ]; then
            log "[WARN] Another scan is running. Job rejected: $JOB_ID"
            write_job_state "failed" "$(now_unix)" "75" "error" "null" "Another scan is running."
            exit 75
        fi

        # Cron jobs wait here so scheduled scans run sequentially instead of
        # failing just because a previous manual or cron scan is still active.
        if [ "$wait_max_seconds" -gt 0 ] && [ "$waited" -ge "$wait_max_seconds" ]; then
            log "[WARN] Another scan is still running after ${waited}s. Job rejected: $JOB_ID"
            write_job_state "failed" "$(now_unix)" "75" "error" "null" "Another scan is running."
            exit 75
        fi

        log "[INFO] Another scan is running. Waiting ${wait_interval}s before retry: $JOB_ID"
        sleep "$wait_interval"
        waited=$((waited + wait_interval))
    done

    LOCK_HELD=1
    # Publish the owning shell immediately. Without a live PID, another
    # scan_once.sh can mistake the newly created directory for a stale lock
    # during the wake and pre-scan phases.
    printf '%s\n' "$$" > "$LOCK_DIR/pid"
    printf '%s\n' "$JOB_ID" > "$LOCK_DIR/job_id"
}

JOB_ID=""
JOB_TYPE=""
JOB_USER_ID=""
TARGET=""
ACTION=""
WAIT_FOR_LOCK=0
WAKE_CLAMAV=0

while [ "$#" -gt 0 ]; do
    case "$1" in
        --id)
            [ "$#" -ge 2 ] || { usage; exit 2; }
            JOB_ID="$2"
            shift 2
            ;;
        --type)
            [ "$#" -ge 2 ] || { usage; exit 2; }
            JOB_TYPE="$2"
            shift 2
            ;;
        -u|--user)
            [ "$#" -ge 2 ] || { usage; exit 2; }
            JOB_USER_ID="$2"
            shift 2
            ;;
        --target)
            [ "$#" -ge 2 ] || { usage; exit 2; }
            TARGET="$2"
            shift 2
            ;;
        --action)
            [ "$#" -ge 2 ] || { usage; exit 2; }
            ACTION="$2"
            shift 2
            ;;
        --wait)
            WAIT_FOR_LOCK=1
            shift
            ;;
        --wake)
            WAKE_CLAMAV=1
            shift
            ;;
        *)
            usage
            exit 2
            ;;
    esac
done

[ -n "$JOB_TYPE" ] || { usage; exit 2; }
[ -n "$JOB_USER_ID" ] || { usage; exit 2; }
[ -n "$TARGET" ] || { usage; exit 2; }

case "$JOB_USER_ID" in
    *[!a-z0-9]*)
        echo "Unsupported job owner ID: $JOB_USER_ID" >&2
        exit 2
        ;;
esac
if [ "${#JOB_USER_ID}" -ne 8 ]; then
    echo "Unsupported job owner ID: $JOB_USER_ID" >&2
    exit 2
fi
case "$JOB_USER_ID" in
    *[a-z]*) ;;
    *) echo "Unsupported job owner ID: $JOB_USER_ID" >&2; exit 2 ;;
esac
case "$JOB_USER_ID" in
    *[0-9]*) ;;
    *) echo "Unsupported job owner ID: $JOB_USER_ID" >&2; exit 2 ;;
esac

case "$JOB_TYPE" in
    manual|cron)
        ;;
    *)
        echo "Unsupported job type: $JOB_TYPE" >&2
        exit 2
        ;;
esac

case "$ACTION" in
    warn|move|remove)
        ;;
    *)
        echo "Unsupported scan action: $ACTION" >&2
        exit 2
        ;;
esac

if [ "$WAKE_CLAMAV" -eq 1 ] && [ "$JOB_TYPE" != "cron" ]; then
    echo "--wake is only supported for cron jobs" >&2
    exit 2
fi

mkdir -p "$LOG_DIR" "$JOBS_DIR" "$QUARANTINE_DIR"

QUARANTINE_NAME_MAX="$(getconf NAME_MAX "$QUARANTINE_DIR" 2>/dev/null || printf '%s' '255')"
case "$QUARANTINE_NAME_MAX" in
    ''|*[!0-9]*|0)
        QUARANTINE_NAME_MAX=255
        ;;
esac

if [ -z "$JOB_ID" ]; then
    JOB_ID="$(make_job_id)"
fi

case "$JOB_ID" in
    *[!A-Za-z0-9_.-]*)
        echo "Unsupported job id: $JOB_ID" >&2
        exit 2
        ;;
esac

JOB_LOG="${LOG_DIR}/${JOB_ID}.log"
DETECTION_LOG="${LOG_DIR}/clamav_detection_${JOB_ID}.log"
JOB_STATE="${JOBS_DIR}/${JOB_ID}.json"
STARTED_AT="$(now_unix)"
ACTIVE_JOB_JSON="\"$(json_escape "$JOB_ID")\""
LAST_JOB_JSON="\"$(json_escape "$JOB_ID")\""
CLAMDSCAN_PID_JSON="null"
SCAN_HEADER_LOGGED=0
DETECTION_COUNT=0
QUARANTINE_SUCCESS_COUNT=0
QUARANTINE_FAILURE_COUNT=0

touch "$JOB_LOG"
trap cleanup_lock EXIT INT TERM

# The WebUI starts this script without --id and reads this first stdout line to
# discover the script-owned job id before the scan finishes.
printf 'JOB_ID=%s\n' "$JOB_ID"

# Cron failures caused by sleep state must still have the normal identifying
# INFO header and a durable, complete job JSON document.
if [ "$JOB_TYPE" = "cron" ]; then
    log_scan_header
    if [ -e "$SLEEP_LOCK_DIR" ] && [ "$WAKE_CLAMAV" -ne 1 ]; then
        fail_scan_job "[ERROR] ClamAV is sleeping" "ClamAV is sleeping" "1"
    fi
fi

# Publish a durable waiting state before lock acquisition. Besides making cron
# waits observable, this prevents account deletion from racing an already
# launched scheduled scan whose owner would otherwise no longer exist.
write_job_state "waiting" "null" "null" "unknown" "null" "Waiting for the global scan lock."
acquire_scan_lock

# Recheck after taking the scan lock so a sleep transition racing with this
# process cannot slip between the initial check and the actual scan.
if [ "$JOB_TYPE" = "cron" ] && [ -e "$SLEEP_LOCK_DIR" ]; then
    if [ "$WAKE_CLAMAV" -ne 1 ]; then
        fail_scan_job "[ERROR] ClamAV is sleeping" "ClamAV is sleeping" "1"
    fi
    if ! "$STARTUP_SCRIPT" --wake; then
        fail_scan_job "[ERROR] Failed to wake ClamAV" "Failed to wake ClamAV" "1"
    fi
fi

if [ ! -e "$TARGET" ]; then
    log "[WARN] Scan target does not exist: $TARGET"
    FINISHED_AT="$(now_unix)"
    write_job_state "finished" "$FINISHED_AT" "0" "clean" "null" "Scan target does not exist, skipped."
    ACTIVE_JOB_JSON="null"
    write_status "ready" "Scan target does not exist, skipped."
    exit 0
fi

if [ "$ACTION" = "move" ] && [ ! -d "$QUARANTINE_DIR" ]; then
    log "[ERROR] Quarantine path is not a directory: $QUARANTINE_DIR"
    FINISHED_AT="$(now_unix)"
    write_job_state "failed" "$FINISHED_AT" "2" "error" "null" "Quarantine path is not a directory."
    ACTIVE_JOB_JSON="null"
    write_status "error" "Quarantine path is not a directory."
    exit 2
fi

tmp_output="$(mktemp)"
MOVE_ERROR_FILE="$(mktemp)"
trap 'rm -f "$tmp_output" "$MOVE_ERROR_FILE"; cleanup_lock' EXIT INT TERM

log_scan_header

extra_args="$(action_args)"

set +e
# shellcheck disable=SC2086
clamdscan \
    --config-file="$CLAMD_CONF" \
    --fdpass \
    --multiscan \
    $extra_args \
    "$TARGET" > "$tmp_output" 2>&1 &
CLAMDSCAN_PID="$!"
CLAMDSCAN_PID_JSON="$CLAMDSCAN_PID"
printf '%s\n' "$CLAMDSCAN_PID" > "$LOCK_DIR/pid"
printf '%s\n' "$JOB_ID" > "$LOCK_DIR/job_id"
write_status "running" "Scan is running."
write_job_state "running" "null" "null" "unknown" "\"$(json_escape "$DETECTION_LOG")\"" "Scan is running."
wait "$CLAMDSCAN_PID"
rc="$?"
set -e

if [ "$rc" -eq 1 ]; then
    log_detections_from_output "$tmp_output"
else
    append_file_with_timestamp "$tmp_output"
fi
rm -f "$tmp_output"

FINISHED_AT="$(now_unix)"
ACTIVE_JOB_JSON="null"

case "$rc" in
    0)
        log "[INFO] Scan finished cleanly: $TARGET"
        write_job_state "finished" "$FINISHED_AT" "$rc" "clean" "null" "Scan finished cleanly."
        write_status "ready" "Scan finished cleanly."
        ;;
    1)
        log "[ALERT] Threat found while scanning: $TARGET"
        if [ "$ACTION" = "move" ] && [ "$DETECTION_COUNT" -eq 0 ]; then
            QUARANTINE_FAILURE_COUNT=$((QUARANTINE_FAILURE_COUNT + 1))
            log "[ERROR] ClamAV reported a threat, but no detected file path could be parsed for quarantine."
        fi
        if [ "$ACTION" = "move" ] && [ "$QUARANTINE_FAILURE_COUNT" -gt 0 ]; then
            log "[ERROR] Quarantine summary: detected=$DETECTION_COUNT succeeded=$QUARANTINE_SUCCESS_COUNT failed=$QUARANTINE_FAILURE_COUNT"
            write_job_state "failed" "$FINISHED_AT" "74" "error" "\"$(json_escape "$DETECTION_LOG")\"" "Threats were detected, but one or more files failed to move to quarantine."
            write_status "error" "Threats were detected, but one or more files could not be moved to quarantine."
            exit 74
        fi
        log_applied_action
        if [ "$ACTION" = "move" ]; then
            log "[INFO] Quarantine summary: detected=$DETECTION_COUNT succeeded=$QUARANTINE_SUCCESS_COUNT failed=0"
        fi
        write_job_state "finished" "$FINISHED_AT" "$rc" "found" "\"$(json_escape "$DETECTION_LOG")\"" "Threat found while scanning."
        write_status "ready" "Threat found while scanning."
        ;;
    *)
        log "[ERROR] Scan failed for $TARGET, exit code: $rc"
        write_job_state "failed" "$FINISHED_AT" "$rc" "error" "null" "Scan failed with exit code $rc."
        write_status "error" "Scan failed with exit code $rc."
        ;;
esac

exit 0
