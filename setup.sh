#!/bin/bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${ROOT_DIR}"

if [ -t 1 ]; then
    C_RESET="$(printf '\033[0m')"
    C_BOLD="$(printf '\033[1m')"
    C_CYAN="$(printf '\033[36m')"
    C_GREEN="$(printf '\033[32m')"
    C_YELLOW="$(printf '\033[33m')"
else
    C_RESET=""
    C_BOLD=""
    C_CYAN=""
    C_GREEN=""
    C_YELLOW=""
fi

show_banner() {
    printf '%b\n' "${C_CYAN}${C_BOLD}==================================================${C_RESET}"
    printf '%b\n' "${C_CYAN}${C_BOLD}   GoFTPd Setup${C_RESET}"
    printf '%b\n' "${C_CYAN}${C_BOLD}==================================================${C_RESET}"
    printf '%b\n' "${C_GREEN}      ____      ___________ _____     __${C_RESET}"
    printf '%b\n' "${C_GREEN}     / ___| ___|  ___|_   _|  _  \\   / /${C_RESET}"
    printf '%b\n' "${C_GREEN}    | |  _ / _ \\ |_    | | | |_) | / / ${C_RESET}"
    printf '%b\n' "${C_GREEN}    | |_| |  __/  _|   | | |  __/ / /  ${C_RESET}"
    printf '%b\n' "${C_GREEN}     \\____|\\___|_|     |_| |_|   /_/   ${C_RESET}"
    printf '%b\n' ""
}

show_usage() {
    show_banner
    printf '%b\n' "${C_BOLD}GoFTPd setup${C_RESET}"
    printf '%s\n' ""
    printf '%b\n' "${C_YELLOW}Usage:${C_RESET}"
    printf '%s\n' "  ./setup.sh install   Run guided interactive setup"
    printf '%s\n' "  ./setup.sh clean     Back up generated configs and reset install state"
    printf '%s\n' "  ./setup.sh backup    Alias for clean"
    printf '%s\n' "  ./setup.sh help      Show this help"
}

case "${1:-help}" in
    install)
        exec "${ROOT_DIR}/setup-interactive.sh" install
        ;;
    clean|backup)
        exec "${ROOT_DIR}/setup-interactive.sh" clean
        ;;
    help|--help|-h|"")
        show_usage
        exit 0
        ;;
    *)
        printf '%b' "${C_YELLOW}" >&2
        printf 'Unknown command: %s\n\n' "${1}" >&2
        printf '%b' "${C_RESET}" >&2
        show_usage >&2
        exit 1
        ;;
esac
