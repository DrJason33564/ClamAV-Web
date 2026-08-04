#!/bin/sh
set -eu

CRON_CONFIG_FILE="${CRON_CONFIG_FILE:-/config/cron_scan.conf}"

config_log() {
    if command -v log_line >/dev/null 2>&1 && [ -n "${CONFIG_LOG_FILE:-}" ]; then
        # Validation errors should still reach callers even if the configured
        # log volume is temporarily unavailable.
        if log_line "$CONFIG_LOG_FILE" "$@" 2>/dev/null; then
            return 0
        fi
    fi

    printf '%s %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*" >&2
}

config_invalid() {
    field="$1"
    line_no="$2"
    value="$3"

    message="Invalid crontab rule at: $field, line $line_no, value $value"
    if command -v log_line >/dev/null 2>&1 && [ -n "${CONFIG_LOG_FILE:-}" ]; then
        log_line "$CONFIG_LOG_FILE" "[ERROR] $message" 2>/dev/null || true
    fi
    printf '%s\n' "$message" >&2
}

trim_config_line() {
    printf '%s\n' "$1" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//'
}

split_cron_config_line() {
    line="$1"
    line_no="$2"

    # Disable pathname expansion so cron wildcards like "*" are not expanded
    # against files in the current working directory.
    set -f
    # shellcheck disable=SC2086
    set -- $line
    set +f

    if [ "$#" -lt 8 ]; then
        config_invalid "format" "$line_no" "$line"
        config_log "[ERROR] Invalid line $line_no: expected 5 cron fields, scan target and action, got: $line"
        config_log "[ERROR] Expected format: minute hour day month weekday scan_target action owner"
        config_log "[ERROR] scan_target may be quoted with double quotes if it contains spaces."
        config_log "[ERROR] Supported actions: warn, move, remove"
        return 1
    fi

    CRON_MINUTE="$1"
    CRON_HOUR="$2"
    CRON_DAY="$3"
    CRON_MONTH="$4"
    CRON_WEEKDAY="$5"

    # Remove the five cron fields from the original line instead of rebuilding
    # from "$@" so quoted paths containing spaces can be preserved.
    tail="$line"
    i=0
    while [ "$i" -lt 5 ]; do
        tail="$(printf '%s\n' "$tail" | sed 's/^[[:space:]]*//')"
        tail="$(printf '%s\n' "$tail" | sed 's/^[^[:space:]]*[[:space:]]*//')"
        i=$((i + 1))
    done
    tail="$(trim_config_line "$tail")"

    if [ -z "$tail" ]; then
        config_invalid "format" "$line_no" "$line"
        return 1
    fi

    case "$tail" in
        \"*)
            rest="${tail#\"}"
            case "$rest" in
                *\"*)
                    CRON_TARGET="${rest%%\"*}"
                    after_quote="${rest#*\"}"
                    after_quote="$(trim_config_line "$after_quote")"
                    ;;
                *)
                    config_invalid "target" "$line_no" "$line"
                    return 1
                    ;;
            esac

            set -f
            # shellcheck disable=SC2086
            set -- $after_quote
            set +f

            if [ "$#" -ne 2 ]; then
                config_invalid "format" "$line_no" "$line"
                return 1
            fi
            CRON_ACTION="$1"
            CRON_USER="$2"
            ;;
        *)
            set -f
            # shellcheck disable=SC2086
            set -- $tail
            set +f

            if [ "$#" -ne 3 ]; then
                config_invalid "target" "$line_no" "$line"
                config_log "[ERROR] Wrap paths containing spaces in double quotes, for example: \"/scan/My Folder\" move"
                return 1
            fi
            CRON_TARGET="$1"
            CRON_ACTION="$2"
            CRON_USER="$3"
            ;;
    esac

    case "$CRON_ACTION" in
        warn|move|remove)
            ;;
        *)
            config_invalid "action" "$line_no" "$CRON_ACTION"
            config_log "[ERROR] Supported actions: warn, move, remove"
            return 1
            ;;
    esac

    if [ -z "$CRON_TARGET" ]; then
        config_invalid "target" "$line_no" "$line"
        return 1
    fi

    case "$CRON_USER" in
        ''|*[!A-Za-z0-9._-]*)
            config_invalid "owner" "$line_no" "$CRON_USER"
            return 1
            ;;
    esac

    if [ "${#CRON_USER}" -gt 64 ]; then
        config_invalid "owner" "$line_no" "$CRON_USER"
        return 1
    fi

    return 0
}

is_int() {
    case "$1" in
        ''|*[!0-9]*) return 1 ;;
        *) return 0 ;;
    esac
}

check_num_range() {
    value="$1"
    min="$2"
    max="$3"

    is_int "$value" || return 1
    [ "$value" -ge "$min" ] && [ "$value" -le "$max" ]
}

check_cron_atom() {
    atom="$1"
    min="$2"
    max="$3"

    if [ "$atom" = "*" ]; then
        return 0
    fi

    if echo "$atom" | grep -Eq '^[0-9]+$'; then
        check_num_range "$atom" "$min" "$max"
        return $?
    fi

    if echo "$atom" | grep -Eq '^[0-9]+-[0-9]+$'; then
        start="${atom%-*}"
        end="${atom#*-}"

        check_num_range "$start" "$min" "$max" || return 1
        check_num_range "$end" "$min" "$max" || return 1
        [ "$start" -le "$end" ]
        return $?
    fi

    return 1
}

check_cron_part_no_list() {
    part="$1"
    min="$2"
    max="$3"

    if echo "$part" | grep -Eq '^.+/[0-9]+$'; then
        base="${part%/*}"
        step="${part##*/}"

        is_int "$step" || return 1
        [ "$step" -ge 1 ] || return 1

        check_cron_atom "$base" "$min" "$max"
        return $?
    fi

    check_cron_atom "$part" "$min" "$max"
}

check_cron_field() {
    field="$1"
    min="$2"
    max="$3"

    [ -n "$field" ] || return 1

    # Cron fields support comma-separated lists; each list item may still be a
    # single number, range, wildcard, or stepped expression.
    old_ifs="$IFS"
    IFS=","

    set -f
    # shellcheck disable=SC2086
    set -- $field
    set +f

    IFS="$old_ifs"

    for part in "$@"; do
        [ -n "$part" ] || return 1
        check_cron_part_no_list "$part" "$min" "$max" || return 1
    done

    return 0
}

validate_cron_config() {
    config_file="${1:-$CRON_CONFIG_FILE}"
    line_no=0
    valid_count=0

    if [ ! -f "$config_file" ]; then
        config_log "[ERROR] Config file not found: $config_file"
        return 1
    fi

    if [ ! -r "$config_file" ]; then
        config_log "[ERROR] Config file is not readable: $config_file"
        return 1
    fi

    # Keep validation strict here because startup and WebUI reload both share
    # this path before writing the live /etc/cron.d file.
    while IFS= read -r line || [ -n "$line" ]; do
        line_no=$((line_no + 1))

        case "$line" in
            ""|\#*) continue ;;
        esac

        if ! split_cron_config_line "$line" "$line_no"; then
            return 1
        fi

        check_cron_field "$CRON_MINUTE" 0 59 || {
            config_invalid "minute" "$line_no" "$CRON_MINUTE"
            return 1
        }

        check_cron_field "$CRON_HOUR" 0 23 || {
            config_invalid "hour" "$line_no" "$CRON_HOUR"
            return 1
        }

        check_cron_field "$CRON_DAY" 1 31 || {
            config_invalid "day" "$line_no" "$CRON_DAY"
            return 1
        }

        check_cron_field "$CRON_MONTH" 1 12 || {
            config_invalid "month" "$line_no" "$CRON_MONTH"
            return 1
        }

        check_cron_field "$CRON_WEEKDAY" 0 7 || {
            config_invalid "weekday" "$line_no" "$CRON_WEEKDAY"
            return 1
        }

        valid_count=$((valid_count + 1))
    done < "$config_file"

    VALID_CRON_RULES_COUNT="$valid_count"
    return 0
}
