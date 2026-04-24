#!/bin/bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${ROOT_DIR}"

TIMESTAMP="$(date +%Y%m%d-%H%M%S)"
BACKUP_DIR="${ROOT_DIR}/backups/setup-interactive-${TIMESTAMP}"
FIFO_PATH="${ROOT_DIR}/etc/goftpd.sitebot.fifo"
CERTS_DIR="${ROOT_DIR}/etc/certs"

say() {
    printf '%s\n' "$*"
}

prompt_yes_no() {
    local prompt="$1"
    local default_answer="${2:-Y}"
    local reply normalized
    while true; do
        read -r -p "${prompt} [${default_answer}/$( [ "${default_answer}" = "Y" ] && printf 'n' || printf 'y' )]: " reply
        if [ -z "${reply}" ]; then
            reply="${default_answer}"
        fi
        normalized="$(printf '%s' "${reply}" | tr '[:lower:]' '[:upper:]')"
        case "${normalized}" in
            Y|YES) return 0 ;;
            N|NO) return 1 ;;
        esac
        say "Please answer y or n."
    done
}

backup_and_remove() {
    local target="$1"
    if [ ! -e "${target}" ] && [ ! -L "${target}" ]; then
        return
    fi
    local rel
    rel="${target#${ROOT_DIR}/}"
    mkdir -p "${BACKUP_DIR}/$(dirname "${rel}")"
    mv "${target}" "${BACKUP_DIR}/${rel}"
    say "Moved ${rel} -> ${BACKUP_DIR}/${rel}"
}

collect_generated_configs() {
    local path
    printf '%s\n' "${ROOT_DIR}/etc/config.yml"
    printf '%s\n' "${ROOT_DIR}/sitebot/etc/config.yml"
    while IFS= read -r path; do
        printf '%s\n' "${path}"
    done < <(find "${ROOT_DIR}/plugins" "${ROOT_DIR}/sitebot/plugins" -type f -name 'config.yml' | sort)
}

say "=================================================="
say "GoFTPd Interactive Setup Cleanup"
say "=================================================="
say "This will back up generated configs so you can rerun ./setup-interactive.sh cleanly."
say ""
say "Backup destination:"
say "  ${BACKUP_DIR}"
say ""

if ! prompt_yes_no "Back up and remove generated interactive setup files?" "Y"; then
    say "Aborted."
    exit 0
fi

mkdir -p "${BACKUP_DIR}"

while IFS= read -r target; do
    backup_and_remove "${target}"
done < <(collect_generated_configs)

backup_and_remove "${FIFO_PATH}"
if [ -d "${CERTS_DIR}" ]; then
    backup_and_remove "${CERTS_DIR}"
fi

say ""
say "Cleanup complete."
say "Kept installer defaults file:"
say "  ${ROOT_DIR}/etc/setup-interactive.env"
say "Removed generated TLS certs too, so the next interactive setup can create fresh ones."
say ""
say "You can now run:"
say "  ./setup-interactive.sh"
