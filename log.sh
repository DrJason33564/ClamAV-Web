#!/bin/sh
set -eu

LOG_MAX_BYTES="${SCAN_LOG_MAX_BYTES:-5242880}"

log_rotate_if_needed() {
    log_file="$1"
    max_bytes="${2:-$LOG_MAX_BYTES}"

    log_dir="$(dirname "$log_file")"
    mkdir -p "$log_dir"

    if [ ! -f "$log_file" ]; then
        : > "$log_file"
        return 0
    fi

    size="$(wc -c < "$log_file" 2>/dev/null || echo 0)"
    if [ "$size" -le "$max_bytes" ]; then
        return 0
    fi

    base="$(basename "$log_file")"
    name="${base%.*}"
    ext="${base##*.}"
    ts="$(date '+%y%m%d%H%M%S')"

    # Preserve the original extension when rotating human-facing log files.
    if [ "$name" = "$base" ]; then
        rotated="${log_dir}/${base}_${ts}"
    else
        rotated="${log_dir}/${name}_${ts}.${ext}"
    fi

    mv "$log_file" "$rotated"
    : > "$log_file"
    printf '%s [INFO] Previous log exceeded %s bytes, rotated to %s\n' \
        "$(date '+%Y-%m-%d %H:%M:%S')" "$max_bytes" "$rotated" >> "$log_file"
}

log_line() {
    log_file="$1"
    shift

    # All scripts write through this helper so rotation behavior stays uniform.
    log_rotate_if_needed "$log_file"
    printf '%s %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*" >> "$log_file"
}
