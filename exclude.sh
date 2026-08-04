#!/bin/sh
set -eu

# Build a local ClamAV SHA256 allow-list database from exclude.conf.
# Each non-empty, non-comment line contains a trusted target and its owner:
#   /path/without/spaces alice
#   "/path/with spaces" alice
#
# Regular files are added directly. Directories are expanded recursively and
# every regular file under them is added.

EXCLUDE_CONFIG_FILE="${EXCLUDE_CONFIG_FILE:-/config/exclude.conf}"
EXCLUDED_DB="${EXCLUDED_DB:-/var/lib/clamav/excluded.sfp}"

STARTUP_LOG_DIR="${STARTUP_LOG_DIR:-/log}"
STARTUP_LOG="${STARTUP_LOG_FILE:-${STARTUP_LOG_DIR}/startup.log}"
LOG_SCRIPT="${LOG_SCRIPT:-/log.sh}"

. "$LOG_SCRIPT"

mkdir -p "$(dirname "$EXCLUDED_DB")" "$STARTUP_LOG_DIR"
touch "$STARTUP_LOG"

exclude_log() {
    log_line "$STARTUP_LOG" "$@"
}

trim_line() {
    printf '%s\n' "$1" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//'
}

# Parse one exclude.conf line into PARSED_TARGET.
# Only double-quoted paths may contain spaces.
parse_exclude_line() {
    raw_line="$1"
    line_no="$2"

    line="$(trim_line "$raw_line")"

    case "$line" in
        ''|'#'*)
            PARSED_TARGET=''
            return 0
            ;;
    esac

    case "$line" in
        \"*)
            rest="${line#\"}"
            case "$rest" in
                *\"*)
                    PARSED_TARGET="${rest%%\"*}"
                    after_quote="${rest#*\"}"
                    after_quote="$(trim_line "$after_quote")"
                    set -f
                    # shellcheck disable=SC2086
                    set -- $after_quote
                    set +f
                    if [ "$#" -ne 1 ]; then
                        exclude_log "[WARN] Invalid exclude line $line_no: expected owner after quoted path, skipped: $raw_line"
                        PARSED_TARGET=''
                        return 0
                    fi
                    PARSED_USER="$1"
                    ;;
                *)
                    exclude_log "[WARN] Invalid exclude line $line_no: unterminated quoted path, skipped: $raw_line"
                    PARSED_TARGET=''
                    return 0
                    ;;
            esac
            ;;
        *)
            set -f
            # shellcheck disable=SC2086
            set -- $line
            set +f

            if [ "$#" -ne 2 ]; then
                exclude_log "[WARN] Invalid exclude line $line_no: expected path and owner, skipped: $raw_line"
                PARSED_TARGET=''
                return 0
            fi
            PARSED_TARGET="$1"
            PARSED_USER="$2"
            ;;
    esac

    if [ -z "$PARSED_TARGET" ]; then
        exclude_log "[WARN] Empty exclude target at line $line_no, skipped."
    fi

    case "${PARSED_USER:-}" in
        ''|*[!A-Za-z0-9._-]*)
            exclude_log "[WARN] Invalid exclude owner at line $line_no, skipped: $raw_line"
            PARSED_TARGET=''
            ;;
    esac
    if [ -n "$PARSED_TARGET" ] && [ "${#PARSED_USER}" -gt 64 ]; then
        exclude_log "[WARN] Exclude owner is too long at line $line_no, skipped: $raw_line"
        PARSED_TARGET=''
    fi

    return 0
}

add_file_hash() {
    file_path="$1"
    tmp_db="$2"

    if sigtool --sha256 "$file_path" >> "$tmp_db" 2>> "$STARTUP_LOG"; then
        return 0
    fi

    exclude_log "[WARN] Failed to generate SHA256 allow-list signature for: $file_path"
    return 0
}

add_target_hashes() {
    target="$1"
    tmp_db="$2"

    if [ ! -e "$target" ]; then
        exclude_log "[WARN] Exclude target does not exist, skipped: $target"
        return 0
    fi

    if [ -f "$target" ]; then
        add_file_hash "$target" "$tmp_db"
        return 0
    fi

    if [ -d "$target" ]; then
        exclude_log "[INFO] Expanding exclude directory recursively: $target"
        # Use find -exec so paths with spaces are handled safely.
        if ! find "$target" -type f -exec sigtool --sha256 {} \; >> "$tmp_db" 2>> "$STARTUP_LOG"; then
            exclude_log "[WARN] Some files under exclude directory failed to hash: $target"
        fi
        return 0
    fi

    exclude_log "[WARN] Exclude target is neither a regular file nor a directory, skipped: $target"
    return 0
}

if [ ! -f "$EXCLUDE_CONFIG_FILE" ]; then
    exclude_log "[INFO] Exclude config not found, creating empty file: $EXCLUDE_CONFIG_FILE"
    mkdir -p "$(dirname "$EXCLUDE_CONFIG_FILE")"
    : > "$EXCLUDE_CONFIG_FILE"
fi

if [ ! -r "$EXCLUDE_CONFIG_FILE" ]; then
    exclude_log "[ERROR] Exclude config file is not readable: $EXCLUDE_CONFIG_FILE"
    exit 1
fi

TMP_DB="$(mktemp)"
trap 'rm -f "$TMP_DB"' EXIT INT TERM

line_no=0
target_count=0
while IFS= read -r line || [ -n "$line" ]; do
    line_no=$((line_no + 1))

    parse_exclude_line "$line" "$line_no"
    [ -n "$PARSED_TARGET" ] || continue

    target_count=$((target_count + 1))
    add_target_hashes "$PARSED_TARGET" "$TMP_DB"
done < "$EXCLUDE_CONFIG_FILE"

if [ -s "$TMP_DB" ]; then
    sort -u "$TMP_DB" > "$EXCLUDED_DB"
    chmod 0644 "$EXCLUDED_DB"

    signature_count="$(wc -l < "$EXCLUDED_DB" 2>/dev/null || echo 0)"
    exclude_log "[INFO] Exclude allow-list database generated: $EXCLUDED_DB ($signature_count signatures)"
else
    # ClamAV treats an empty local signature database as malformed.
    # When there are no effective allow-list entries, remove the generated DB
    # instead of leaving an empty .sfp file in /var/lib/clamav.
    rm -f "$EXCLUDED_DB"

    if [ "$target_count" -eq 0 ]; then
        exclude_log "[INFO] Exclude config is empty. No allow-list database generated."
    else
        exclude_log "[INFO] No valid exclude signatures generated. Removed allow-list database: $EXCLUDED_DB"
    fi
fi

exit 0
