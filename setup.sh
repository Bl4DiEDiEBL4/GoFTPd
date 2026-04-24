#!/bin/bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${ROOT_DIR}"
STATE_FILE="${ROOT_DIR}/etc/setup-interactive.env"
FIFO_PATH_DEFAULT="${ROOT_DIR}/etc/goftpd.sitebot.fifo"
SITEBOT_CONFIG_DEFAULT="${ROOT_DIR}/sitebot/etc/config.yml"
TIMESTAMP="$(date +%Y%m%d-%H%M%S)"
BACKUP_DIR="${ROOT_DIR}/backups/setup-interactive-${TIMESTAMP}"

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

say() {
    printf '%s\n' "$*"
}

say_color() {
    local color="$1"
    shift
    printf '%b%s%b\n' "${color}" "$*" "${C_RESET}"
}

show_banner() {
    say_color "${C_CYAN}${C_BOLD}" "=================================================="
    say_color "${C_CYAN}${C_BOLD}" "   GoFTPd Setup"
    say_color "${C_CYAN}${C_BOLD}" "=================================================="
    say_color "${C_GREEN}" "      ____      ___________ _____     __"
    say_color "${C_GREEN}" "     / ___| ___|  ___|_   _|  _  \\   / /"
    say_color "${C_GREEN}" "    | |  _ / _ \\ |_    | | | |_) | / / "
    say_color "${C_GREEN}" "    | |_| |  __/  _|   | | |  __/ / /  "
    say_color "${C_GREEN}" "     \\____|\\___|_|     |_| |_|   /_/   "
    printf '\n'
}

prompt_default() {
    local prompt="$1"
    local default_value="$2"
    local reply
    read -r -p "${prompt} [${default_value}]: " reply
    if [ -z "${reply}" ]; then
        printf '%s' "${default_value}"
    else
        printf '%s' "${reply}"
    fi
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

run_cleanup_mode() {
    local certs_dir="${ROOT_DIR}/etc/certs"

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

    show_banner
    say_color "${C_YELLOW}${C_BOLD}" "Cleanup mode"
    say "This will back up generated configs so you can rerun ./setup.sh cleanly."
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

    backup_and_remove "${SETUP_FIFO_PATH:-${FIFO_PATH_DEFAULT}}"
    if [ -d "${certs_dir}" ]; then
        backup_and_remove "${certs_dir}"
    fi

    say ""
    say "Cleanup complete."
    say "Kept installer defaults file:"
    say "  ${STATE_FILE}"
    say "Removed generated TLS certs too, so the next interactive setup can create fresh ones."
    say ""
    say "You can now run:"
    say "  ./setup.sh install"
    exit 0
}

if [ -f "${STATE_FILE}" ]; then
    if prompt_yes_no "Load saved setup defaults from ${STATE_FILE}?" "Y"; then
        # shellcheck disable=SC1090
        . "${STATE_FILE}"
        say "Loaded saved setup defaults."
    else
        say "Ignoring saved setup defaults for this run."
    fi
fi

bool_to_prompt_default() {
    local value="${1:-}"
    case "${value}" in
        true|TRUE|yes|YES|y|Y|1) printf 'Y' ;;
        false|FALSE|no|NO|n|N|0) printf 'N' ;;
        *) printf 'Y' ;;
    esac
}

prompt_mode() {
    local default_mode="${1:-master}"
    local reply normalized
    while true; do
        read -r -p "Daemon mode (master/slave) [${default_mode}]: " reply
        if [ -z "${reply}" ]; then
            reply="${default_mode}"
        fi
        normalized="$(printf '%s' "${reply}" | tr '[:upper:]' '[:lower:]')"
        case "${normalized}" in
            master|slave)
                printf '%s' "${normalized}"
                return 0
                ;;
        esac
        say "Please type master or slave."
    done
}

copy_if_missing() {
    local src="$1"
    local dst="$2"
    if [ ! -f "${dst}" ]; then
        mkdir -p "$(dirname "${dst}")"
        cp "${src}" "${dst}"
        say "Created ${dst}"
    fi
}

copy_dist_configs_if_missing() {
    local base_dir="$1"
    while IFS= read -r dist_file; do
        local target_file="${dist_file%.dist}"
        copy_if_missing "${dist_file}" "${target_file}"
    done < <(find "${base_dir}" -type f -name 'config.yml.dist' | sort)
}

replace_matching_line() {
    local file="$1"
    local pattern="$2"
    local replacement="$3"
    awk -v pattern="${pattern}" -v replacement="${replacement}" '
        function comment_pos(line,    i, ch, in_single, in_double, prev) {
            in_single = 0
            in_double = 0
            prev = ""
            for (i = 1; i <= length(line); i++) {
                ch = substr(line, i, 1)
                if (ch == "\"" && !in_single && prev != "\\") {
                    in_double = !in_double
                } else if (ch == "'"'"'" && !in_double && prev != "\\") {
                    in_single = !in_single
                } else if (ch == "#" && !in_single && !in_double) {
                    return i
                }
                prev = ch
            }
            return 0
        }
        $0 ~ pattern {
            line = $0
            comment = ""
            hash_pos = comment_pos(line)
            if (hash_pos > 0) {
                comment = substr(line, hash_pos)
                sub(/[[:space:]]+$/, "", replacement)
                print replacement " " comment
            } else {
                print replacement
            }
            next
        }
        { print }
    ' "${file}" > "${file}.tmp"
    mv "${file}.tmp" "${file}"
}

set_daemon_plugin_enabled() {
    local file="$1"
    local plugin_name="$2"
    local enabled_value="$3"
    awk -v plugin_name="${plugin_name}" -v enabled_value="${enabled_value}" '
        BEGIN { in_plugins = 0; in_target = 0 }
        /^plugins:$/ { in_plugins = 1; print; next }
        in_plugins && $0 ~ ("^  " plugin_name ":$") { in_target = 1; print; next }
        in_target && $0 ~ /^  [A-Za-z0-9_-]+:$/ { in_target = 0 }
        in_target && $0 ~ /^    enabled: / { print "    enabled: " enabled_value; next }
        { print }
    ' "${file}" > "${file}.tmp"
    mv "${file}.tmp" "${file}"
}

set_sitebot_plugin_enabled() {
    local file="$1"
    local plugin_name="$2"
    local enabled_value="$3"
    awk -v plugin_name="${plugin_name}" -v enabled_value="${enabled_value}" '
        BEGIN { in_plugins = 0; in_enabled = 0 }
        /^plugins:$/ { in_plugins = 1; print; next }
        in_plugins && /^  enabled:$/ { in_enabled = 1; print; next }
        in_enabled && /^  config:$/ { in_enabled = 0; print; next }
        in_enabled && $0 ~ ("^    " plugin_name ": ") {
            print "    " plugin_name ": " enabled_value
            next
        }
        { print }
    ' "${file}" > "${file}.tmp"
    mv "${file}.tmp" "${file}"
}

set_sitebot_channel_anchor() {
    local file="$1"
    local key="$2"
    local anchor="$3"
    local channel_name="$4"
    awk -v key="${key}" -v anchor="${anchor}" -v channel_name="${channel_name}" '
        $0 ~ ("^  " key ":") {
            print "  " key ":    &" anchor "    [\"" channel_name "\"]"
            next
        }
        { print }
    ' "${file}" > "${file}.tmp"
    mv "${file}.tmp" "${file}"
}

rewrite_sitebot_irc_channels() {
    local file="$1"
    local main_channel="$2"
    local spam_channel="$3"
    local staff_channel="$4"
    awk -v main_channel="${main_channel}" -v spam_channel="${spam_channel}" -v staff_channel="${staff_channel}" '
        BEGIN { replacing = 0 }
        /^  channels:$/ {
            print
            print "    - \"" main_channel "\""
            print "    - \"" spam_channel "\""
            print "    - \"" staff_channel "\""
            replacing = 1
            next
        }
        replacing && $0 ~ /^    - / { next }
        replacing { replacing = 0 }
        { print }
    ' "${file}" > "${file}.tmp"
    mv "${file}.tmp" "${file}"
}

rewrite_sitebot_invite_channel() {
    local file="$1"
    local staff_channel="$2"
    awk -v staff_channel="${staff_channel}" '
        BEGIN { in_invite = 0; emitted = 0 }
        /^invite_channels:$/ {
            print
            print "  - channel: \"" staff_channel "\""
            print "    flags: \"1\""
            in_invite = 1
            emitted = 1
            next
        }
        in_invite && ($0 ~ /^  - / || $0 ~ /^    /) { next }
        in_invite { in_invite = 0 }
        { print }
    ' "${file}" > "${file}.tmp"
    mv "${file}.tmp" "${file}"
}

rewrite_sitebot_encryption_keys() {
    local file="$1"
    local main_channel="$2"
    local spam_channel="$3"
    local staff_channel="$4"
    local foreign_channel="$5"
    local archive_channel="$6"
    local nuke_channel="$7"
    local main_key="$8"
    local spam_key="$9"
    local staff_key="${10}"
    local foreign_key="${11}"
    local archive_key="${12}"
    local nuke_key="${13}"
    awk -v main_channel="${main_channel}" \
        -v spam_channel="${spam_channel}" \
        -v staff_channel="${staff_channel}" \
        -v foreign_channel="${foreign_channel}" \
        -v archive_channel="${archive_channel}" \
        -v nuke_channel="${nuke_channel}" \
        -v main_key="${main_key}" \
        -v spam_key="${spam_key}" \
        -v staff_key="${staff_key}" \
        -v foreign_key="${foreign_key}" \
        -v archive_key="${archive_key}" \
        -v nuke_key="${nuke_key}" '
        BEGIN { in_keys = 0 }
        /^  keys:$/ {
            print
            print "    \"" main_channel "\": \"cbc:" main_key "\""
            print "    \"" spam_channel "\": \"cbc:" spam_key "\""
            print "    \"" staff_channel "\": \"cbc:" staff_key "\""
            print "    \"" foreign_channel "\": \"cbc:" foreign_key "\""
            print "    \"" archive_channel "\": \"cbc:" archive_key "\""
            print "    \"" nuke_channel "\": \"cbc:" nuke_key "\""
            in_keys = 1
            next
        }
        in_keys && $0 ~ /^    "/ { next }
        in_keys { in_keys = 0 }
        { print }
    ' "${file}" > "${file}.tmp"
    mv "${file}.tmp" "${file}"
}

set_yaml_array_line() {
    local file="$1"
    local key_pattern="$2"
    local replacement="$3"
    replace_matching_line "${file}" "${key_pattern}" "${replacement}"
}

set_sitebot_scalar() {
    local file="$1"
    local key="$2"
    local value="$3"
    replace_matching_line "${file}" "^  ${key}:" "  ${key}: ${value}"
}

configure_sitebot_plugin_channels() {
    local main_channel="$1"
    local staff_channel="$2"
    local nuke_channel="$3"

    if [ -f "sitebot/plugins/announce/config.yml" ]; then
        cat > "sitebot/plugins/announce/config.yml" <<EOF
default_channel: "${staff_channel}"
theme_file: "./etc/templates/pzsng.theme"
type_routes:
  NUKE: ["${nuke_channel}"]
  UNNUKE: ["${nuke_channel}"]
  NEWDAY: ["${main_channel}"]
  SPEEDTEST: ["${main_channel}"]
EOF
    fi

    if [ -f "sitebot/plugins/news/config.yml" ]; then
        set_yaml_array_line "sitebot/plugins/news/config.yml" '^channels:' "channels: [\"${main_channel}\"]"
        set_yaml_array_line "sitebot/plugins/news/config.yml" '^staff_channels:' "staff_channels: [\"${staff_channel}\"]"
    fi

    if [ -f "sitebot/plugins/request/config.yml" ]; then
        set_yaml_array_line "sitebot/plugins/request/config.yml" '^channels:' "channels: [\"${main_channel}\"]"
        set_yaml_array_line "sitebot/plugins/request/config.yml" '^staff_channels:' "staff_channels: [\"${staff_channel}\"]"
    fi

    if [ -f "sitebot/plugins/bnc/config.yml" ]; then
        set_yaml_array_line "sitebot/plugins/bnc/config.yml" '^channels:' "channels: [\"${main_channel}\"]"
    fi

    if [ -f "sitebot/plugins/bw/config.yml" ]; then
        set_yaml_array_line "sitebot/plugins/bw/config.yml" '^channels:' "channels: [\"${main_channel}\"]"
    fi

    if [ -f "sitebot/plugins/admincommander/config.yml" ]; then
        set_yaml_array_line "sitebot/plugins/admincommander/config.yml" '^staff_channels:' "staff_channels: [\"${staff_channel}\"]"
    fi
}

configure_sitebot_plugin_connections() {
    local ftp_host="$1"
    local ftp_port="$2"
    local ftp_user="$3"
    local ftp_password="$4"
    local ftp_tls="$5"
    local ftp_insecure="$6"
    local bnc_target_host="$7"
    local bnc_target_port="$8"

    if [ -f "sitebot/plugins/request/config.yml" ]; then
        set_yaml_array_line "sitebot/plugins/request/config.yml" '^host:' "host: \"${ftp_host}\""
        set_yaml_array_line "sitebot/plugins/request/config.yml" '^port:' "port: ${ftp_port}"
        set_yaml_array_line "sitebot/plugins/request/config.yml" '^user:' "user: \"${ftp_user}\""
        set_yaml_array_line "sitebot/plugins/request/config.yml" '^password:' "password: \"${ftp_password}\""
        set_yaml_array_line "sitebot/plugins/request/config.yml" '^tls:' "tls: ${ftp_tls}"
        set_yaml_array_line "sitebot/plugins/request/config.yml" '^insecure_skip_verify:' "insecure_skip_verify: ${ftp_insecure}"
    fi

    if [ -f "sitebot/plugins/bw/config.yml" ]; then
        set_yaml_array_line "sitebot/plugins/bw/config.yml" '^host:' "host: \"${ftp_host}\""
        set_yaml_array_line "sitebot/plugins/bw/config.yml" '^port:' "port: ${ftp_port}"
        set_yaml_array_line "sitebot/plugins/bw/config.yml" '^user:' "user: \"${ftp_user}\""
        set_yaml_array_line "sitebot/plugins/bw/config.yml" '^password:' "password: \"${ftp_password}\""
        set_yaml_array_line "sitebot/plugins/bw/config.yml" '^tls:' "tls: ${ftp_tls}"
        set_yaml_array_line "sitebot/plugins/bw/config.yml" '^insecure_skip_verify:' "insecure_skip_verify: ${ftp_insecure}"
    fi

    if [ -f "sitebot/plugins/admincommander/config.yml" ]; then
        set_yaml_array_line "sitebot/plugins/admincommander/config.yml" '^host:' "host: \"${ftp_host}\""
        set_yaml_array_line "sitebot/plugins/admincommander/config.yml" '^port:' "port: ${ftp_port}"
        set_yaml_array_line "sitebot/plugins/admincommander/config.yml" '^user:' "user: \"${ftp_user}\""
        set_yaml_array_line "sitebot/plugins/admincommander/config.yml" '^password:' "password: \"${ftp_password}\""
        set_yaml_array_line "sitebot/plugins/admincommander/config.yml" '^tls:' "tls: ${ftp_tls}"
        set_yaml_array_line "sitebot/plugins/admincommander/config.yml" '^insecure_skip_verify:' "insecure_skip_verify: ${ftp_insecure}"
    fi

    if [ -f "sitebot/plugins/bnc/config.yml" ]; then
        set_yaml_array_line "sitebot/plugins/bnc/config.yml" '^user:' "user: \"${ftp_user}\""
        set_yaml_array_line "sitebot/plugins/bnc/config.yml" '^password:' "password: \"${ftp_password}\""
        set_yaml_array_line "sitebot/plugins/bnc/config.yml" '^tls:' "tls: ${ftp_tls}"
        set_yaml_array_line "sitebot/plugins/bnc/config.yml" '^insecure_skip_verify:' "insecure_skip_verify: ${ftp_insecure}"
        awk -v target_host="${bnc_target_host}" -v target_port="${bnc_target_port}" '
            BEGIN { in_targets = 0; emitted = 0 }
            /^targets:$/ {
                print
                print "  - name: \"sitename\""
                print "    host: \"" target_host "\""
                print "    port: " target_port
                in_targets = 1
                emitted = 1
                next
            }
            in_targets && ($0 ~ /^  - / || $0 ~ /^    /) { next }
            in_targets { in_targets = 0 }
            { print }
        ' "sitebot/plugins/bnc/config.yml" > "sitebot/plugins/bnc/config.yml.tmp"
        mv "sitebot/plugins/bnc/config.yml.tmp" "sitebot/plugins/bnc/config.yml"
    fi
}

configure_daemon() {
    local daemon_config="etc/config.yml"
    if [ -f "${daemon_config}" ]; then
        say "Daemon config already exists at ${daemon_config}; skipping daemon setup questions."
        return
    fi

    say ""
    say "Configuring daemon..."
    copy_if_missing "etc/config-example.yml" "${daemon_config}"
    copy_dist_configs_if_missing "plugins"

    local daemon_mode long_name short_name cert_name enabled_bool
    local listen_port public_ip passthrough_mode master_listen_host master_control_port
    local slave_name slave_master_host slave_master_port slave_roots slave_bind_ip fifo_path
    daemon_mode="$(prompt_mode "${SETUP_DAEMON_MODE:-master}")"
    long_name="$(prompt_default 'Daemon site name' "${SETUP_SITE_NAME:-GoFTPd}")"
    short_name="$(prompt_default 'Daemon short site tag' "${SETUP_SITE_SHORT:-${long_name}}")"
    cert_name="$(prompt_default 'TLS certificate display name' "${SETUP_CERT_NAME:-${long_name}}")"

    SETUP_DAEMON_MODE="${daemon_mode}"
    SETUP_SITE_NAME="${long_name}"
    SETUP_SITE_SHORT="${short_name}"
    SETUP_CERT_NAME="${cert_name}"
    fifo_path="${SETUP_FIFO_PATH:-${FIFO_PATH_DEFAULT}}"
    SETUP_FIFO_PATH="${fifo_path}"

    replace_matching_line "${daemon_config}" '^mode:' "mode:         ${daemon_mode}"
    replace_matching_line "${daemon_config}" '^sitename_long:' "sitename_long:  \"${long_name}\""
    replace_matching_line "${daemon_config}" '^sitename_short:' "sitename_short: \"${short_name}\""
    replace_matching_line "${daemon_config}" '^event_fifo:' "event_fifo:     \"${fifo_path}\""
    replace_matching_line "${daemon_config}" '^sitebot_config:' "sitebot_config: \"${SETUP_SITEBOT_CONFIG_PATH:-${SITEBOT_CONFIG_DEFAULT}}\""

    if [ "${daemon_mode}" = "master" ]; then
        listen_port="$(prompt_default 'FTP listen port' "${SETUP_LISTEN_PORT:-21212}")"
        public_ip="$(prompt_default 'Public PASV IP address' "${SETUP_PUBLIC_IP:-1.2.3.4}")"
        master_listen_host="$(prompt_default 'Master control listen host' "${SETUP_MASTER_LISTEN_HOST:-0.0.0.0}")"
        master_control_port="$(prompt_default 'Master control port' "${SETUP_MASTER_CONTROL_PORT:-1099}")"
        passthrough_mode="$(prompt_default 'Transfer mode (passthrough/proxy)' "${SETUP_TRANSFER_MODE:-passthrough}")"
        case "$(printf '%s' "${passthrough_mode}" | tr '[:upper:]' '[:lower:]')" in
            proxy)
                enabled_bool="false"
                passthrough_mode="proxy"
                ;;
            *)
                enabled_bool="true"
                passthrough_mode="passthrough"
                ;;
        esac

        SETUP_LISTEN_PORT="${listen_port}"
        SETUP_PUBLIC_IP="${public_ip}"
        SETUP_MASTER_LISTEN_HOST="${master_listen_host}"
        SETUP_MASTER_CONTROL_PORT="${master_control_port}"
        SETUP_TRANSFER_MODE="${passthrough_mode}"

        replace_matching_line "${daemon_config}" '^listen_port:' "listen_port:  ${listen_port}"
        replace_matching_line "${daemon_config}" '^public_ip:' "public_ip:    \"${public_ip}\""
        replace_matching_line "${daemon_config}" '^passthrough:' "passthrough:  ${enabled_bool}"
        replace_matching_line "${daemon_config}" '^  listen_host:' "  listen_host:       \"${master_listen_host}\""
        replace_matching_line "${daemon_config}" '^  control_port:' "  control_port:      ${master_control_port}"
    else
        slave_name="$(prompt_default 'Slave name' "${SETUP_SLAVE_NAME:-SLAVE1}")"
        slave_master_host="$(prompt_default 'Slave master host/IP' "${SETUP_SLAVE_MASTER_HOST:-127.0.0.1}")"
        slave_master_port="$(prompt_default 'Slave master control port' "${SETUP_SLAVE_MASTER_PORT:-1099}")"
        slave_roots="$(prompt_default 'Slave storage root' "${SETUP_SLAVE_ROOTS:-./site}")"
        slave_bind_ip="$(prompt_default 'Slave PASV bind IP (blank = auto-detect)' "${SETUP_SLAVE_BIND_IP:-}")"

        SETUP_SLAVE_NAME="${slave_name}"
        SETUP_SLAVE_MASTER_HOST="${slave_master_host}"
        SETUP_SLAVE_MASTER_PORT="${slave_master_port}"
        SETUP_SLAVE_ROOTS="${slave_roots}"
        SETUP_SLAVE_BIND_IP="${slave_bind_ip}"

        replace_matching_line "${daemon_config}" '^  name:' "  name:          \"${slave_name}\""
        replace_matching_line "${daemon_config}" '^  master_host:' "  master_host:   \"${slave_master_host}\""
        replace_matching_line "${daemon_config}" '^  master_port:' "  master_port:   ${slave_master_port}"
        replace_matching_line "${daemon_config}" '^  roots:' "  roots:         [\"${slave_roots}\"]"
        replace_matching_line "${daemon_config}" '^  bind_ip:' "  bind_ip:       \"${slave_bind_ip}\""
    fi

    local daemon_plugins=()
    if [ "${daemon_mode}" = "master" ]; then
        daemon_plugins=(dateddirs tvmaze imdb mediainfo speedtest request pre)
    fi

    local plugin_name
    for plugin_name in "${daemon_plugins[@]}"; do
        local var_name="SETUP_DAEMON_PLUGIN_$(printf '%s' "${plugin_name}" | tr '[:lower:]-' '[:upper:]_')"
        local default_answer
        default_answer="$(bool_to_prompt_default "${!var_name:-true}")"
        if prompt_yes_no "Enable daemon plugin ${plugin_name}?" "${default_answer}"; then
            enabled_bool="true"
        else
            enabled_bool="false"
        fi
        printf -v "${var_name}" '%s' "${enabled_bool}"
        set_daemon_plugin_enabled "${daemon_config}" "${plugin_name}" "${enabled_bool}"
    done

    if [ ! -f "etc/certs/server.crt" ] || [ ! -f "etc/certs/server.key" ]; then
        if prompt_yes_no "Generate TLS certificates now?" "$(bool_to_prompt_default "${SETUP_GENERATE_CERTS:-true}")"; then
            SETUP_GENERATE_CERTS="true"
            ./generate_certs.sh "${cert_name}"
        else
            SETUP_GENERATE_CERTS="false"
        fi
    else
        say "TLS certificates already exist in etc/certs; skipping generation."
    fi
}

configure_sitebot() {
    local sitebot_config="sitebot/etc/config.yml"
    if [ "${SETUP_DAEMON_MODE:-master}" = "slave" ]; then
        if ! prompt_yes_no "Configure sitebot on this slave too?" "$(bool_to_prompt_default "${SETUP_CONFIGURE_SITEBOT_ON_SLAVE:-false}")"; then
            SETUP_CONFIGURE_SITEBOT_ON_SLAVE="false"
            say "Skipping sitebot setup for slave mode."
            return
        fi
        SETUP_CONFIGURE_SITEBOT_ON_SLAVE="true"
    fi
    if [ -f "${sitebot_config}" ]; then
        say "Sitebot config already exists at ${sitebot_config}; skipping sitebot setup questions."
        return
    fi

    say ""
    say "Configuring sitebot..."
    copy_if_missing "sitebot/etc/config.yml.example" "${sitebot_config}"
    copy_dist_configs_if_missing "sitebot/plugins"

    local irc_host irc_port irc_nick irc_user irc_realname irc_password irc_ssl
    local ftp_host ftp_port ftp_user ftp_password ftp_tls ftp_insecure bnc_target_host bnc_target_port
    local main_channel spam_channel staff_channel foreign_channel archive_channel nuke_channel enabled_bool
    local main_key spam_key staff_key foreign_key archive_key nuke_key
    local fifo_path
    irc_host="$(prompt_default 'IRC host' "${SETUP_IRC_HOST:-irc.example.net}")"
    irc_port="$(prompt_default 'IRC port' "${SETUP_IRC_PORT:-6697}")"
    irc_nick="$(prompt_default 'IRC nick' "${SETUP_IRC_NICK:-GoSitebot}")"
    irc_user="$(prompt_default 'IRC user' "${SETUP_IRC_USER:-sitebot}")"
    irc_realname="$(prompt_default 'IRC realname' "${SETUP_IRC_REALNAME:-GoSitebot v1.0}")"
    irc_password="$(prompt_default 'IRC server password' "${SETUP_IRC_PASSWORD:-changeme}")"
    if prompt_yes_no "Use SSL for IRC?" "$(bool_to_prompt_default "${SETUP_IRC_SSL:-true}")"; then
        irc_ssl="true"
    else
        irc_ssl="false"
    fi
    ftp_host="$(prompt_default 'Sitebot FTP host for plugins' "${SETUP_PLUGIN_FTP_HOST:-127.0.0.1}")"
    ftp_port="$(prompt_default 'Sitebot FTP port for plugins' "${SETUP_PLUGIN_FTP_PORT:-21212}")"
    ftp_user="$(prompt_default 'Sitebot FTP user for plugins' "${SETUP_PLUGIN_FTP_USER:-goftpd}")"
    ftp_password="$(prompt_default 'Sitebot FTP password for plugins' "${SETUP_PLUGIN_FTP_PASSWORD:-goftpd}")"
    if prompt_yes_no "Use TLS for sitebot FTP plugins?" "$(bool_to_prompt_default "${SETUP_PLUGIN_FTP_TLS:-true}")"; then
        ftp_tls="true"
    else
        ftp_tls="false"
    fi
    if prompt_yes_no "Skip TLS verify for sitebot FTP plugins?" "$(bool_to_prompt_default "${SETUP_PLUGIN_FTP_INSECURE:-true}")"; then
        ftp_insecure="true"
    else
        ftp_insecure="false"
    fi
    bnc_target_host="$(prompt_default 'BNC target host' "${SETUP_BNC_TARGET_HOST:-${ftp_host}}")"
    bnc_target_port="$(prompt_default 'BNC target port' "${SETUP_BNC_TARGET_PORT:-${ftp_port}}")"
    main_channel="$(prompt_default 'Main IRC channel' "${SETUP_MAIN_CHANNEL:-#goftpd}")"
    spam_channel="$(prompt_default 'Spam IRC channel' "${SETUP_SPAM_CHANNEL:-#goftpd-spam}")"
    staff_channel="$(prompt_default 'Staff IRC channel' "${SETUP_STAFF_CHANNEL:-#goftpd-staff}")"
    foreign_channel="$(prompt_default 'Foreign IRC channel' "${SETUP_FOREIGN_CHANNEL:-#goftpd-foreign}")"
    archive_channel="$(prompt_default 'Archive IRC channel' "${SETUP_ARCHIVE_CHANNEL:-#goftpd-archive}")"
    nuke_channel="$(prompt_default 'Nuke IRC channel' "${SETUP_NUKE_CHANNEL:-#goftpd-nuke}")"
    main_key="$(prompt_default 'Blowfish key for main channel' "${SETUP_BLOWFISH_KEY_MAIN:-YourBlowfishKeyHere123456}")"
    spam_key="$(prompt_default 'Blowfish key for spam channel' "${SETUP_BLOWFISH_KEY_SPAM:-${main_key}}")"
    staff_key="$(prompt_default 'Blowfish key for staff channel' "${SETUP_BLOWFISH_KEY_STAFF:-${main_key}}")"
    foreign_key="$(prompt_default 'Blowfish key for foreign channel' "${SETUP_BLOWFISH_KEY_FOREIGN:-${main_key}}")"
    archive_key="$(prompt_default 'Blowfish key for archive channel' "${SETUP_BLOWFISH_KEY_ARCHIVE:-${main_key}}")"
    nuke_key="$(prompt_default 'Blowfish key for nuke channel' "${SETUP_BLOWFISH_KEY_NUKE:-${main_key}}")"

    SETUP_IRC_HOST="${irc_host}"
    SETUP_IRC_PORT="${irc_port}"
    SETUP_IRC_NICK="${irc_nick}"
    SETUP_IRC_USER="${irc_user}"
    SETUP_IRC_REALNAME="${irc_realname}"
    SETUP_IRC_PASSWORD="${irc_password}"
    SETUP_IRC_SSL="${irc_ssl}"
    SETUP_PLUGIN_FTP_HOST="${ftp_host}"
    SETUP_PLUGIN_FTP_PORT="${ftp_port}"
    SETUP_PLUGIN_FTP_USER="${ftp_user}"
    SETUP_PLUGIN_FTP_PASSWORD="${ftp_password}"
    SETUP_PLUGIN_FTP_TLS="${ftp_tls}"
    SETUP_PLUGIN_FTP_INSECURE="${ftp_insecure}"
    SETUP_BNC_TARGET_HOST="${bnc_target_host}"
    SETUP_BNC_TARGET_PORT="${bnc_target_port}"
    SETUP_MAIN_CHANNEL="${main_channel}"
    SETUP_SPAM_CHANNEL="${spam_channel}"
    SETUP_STAFF_CHANNEL="${staff_channel}"
    SETUP_FOREIGN_CHANNEL="${foreign_channel}"
    SETUP_ARCHIVE_CHANNEL="${archive_channel}"
    SETUP_NUKE_CHANNEL="${nuke_channel}"
    SETUP_BLOWFISH_KEY_MAIN="${main_key}"
    SETUP_BLOWFISH_KEY_SPAM="${spam_key}"
    SETUP_BLOWFISH_KEY_STAFF="${staff_key}"
    SETUP_BLOWFISH_KEY_FOREIGN="${foreign_key}"
    SETUP_BLOWFISH_KEY_ARCHIVE="${archive_key}"
    SETUP_BLOWFISH_KEY_NUKE="${nuke_key}"
    fifo_path="${SETUP_FIFO_PATH:-${FIFO_PATH_DEFAULT}}"
    SETUP_FIFO_PATH="${fifo_path}"
    SETUP_SITEBOT_CONFIG_PATH="${ROOT_DIR}/sitebot/etc/config.yml"

    set_sitebot_scalar "${sitebot_config}" "host" "\"${irc_host}\""
    set_sitebot_scalar "${sitebot_config}" "port" "${irc_port}"
    set_sitebot_scalar "${sitebot_config}" "nick" "\"${irc_nick}\""
    set_sitebot_scalar "${sitebot_config}" "user" "\"${irc_user}\""
    set_sitebot_scalar "${sitebot_config}" "realname" "\"${irc_realname}\""
    set_sitebot_scalar "${sitebot_config}" "password" "\"${irc_password}\""
    set_sitebot_scalar "${sitebot_config}" "ssl" "${irc_ssl}"
    replace_matching_line "${sitebot_config}" '^event_fifo:' "event_fifo: \"${fifo_path}\""

    set_sitebot_channel_anchor "${sitebot_config}" "main" "chan_main" "${main_channel}"
    set_sitebot_channel_anchor "${sitebot_config}" "spam" "chan_spam" "${spam_channel}"
    set_sitebot_channel_anchor "${sitebot_config}" "staff" "chan_staff" "${staff_channel}"
    set_sitebot_channel_anchor "${sitebot_config}" "foreign" "chan_foreign" "${foreign_channel}"
    set_sitebot_channel_anchor "${sitebot_config}" "archive" "chan_archive" "${archive_channel}"
    set_sitebot_channel_anchor "${sitebot_config}" "nuke" "chan_nuke" "${nuke_channel}"
    rewrite_sitebot_irc_channels "${sitebot_config}" "${main_channel}" "${spam_channel}" "${staff_channel}"
    rewrite_sitebot_invite_channel "${sitebot_config}" "${staff_channel}"
    rewrite_sitebot_encryption_keys "${sitebot_config}" "${main_channel}" "${spam_channel}" "${staff_channel}" "${foreign_channel}" "${archive_channel}" "${nuke_channel}" "${main_key}" "${spam_key}" "${staff_key}" "${foreign_key}" "${archive_key}" "${nuke_key}"
    configure_sitebot_plugin_channels "${main_channel}" "${staff_channel}" "${nuke_channel}"
    configure_sitebot_plugin_connections "${ftp_host}" "${ftp_port}" "${ftp_user}" "${ftp_password}" "${ftp_tls}" "${ftp_insecure}" "${bnc_target_host}" "${bnc_target_port}"

    local sitebot_plugins=(Announce TVMaze IMDB News Free Affils Request BNC BW AdminCommander)
    local plugin_name
    for plugin_name in "${sitebot_plugins[@]}"; do
        local var_name="SETUP_SITEBOT_PLUGIN_$(printf '%s' "${plugin_name}" | tr '[:lower:]-' '[:upper:]_')"
        local default_answer
        default_answer="$(bool_to_prompt_default "${!var_name:-true}")"
        if prompt_yes_no "Enable sitebot plugin ${plugin_name}?" "${default_answer}"; then
            enabled_bool="true"
        else
            enabled_bool="false"
        fi
        printf -v "${var_name}" '%s' "${enabled_bool}"
        set_sitebot_plugin_enabled "${sitebot_config}" "${plugin_name}" "${enabled_bool}"
    done
}

ensure_fifo() {
    local fifo_path="${SETUP_FIFO_PATH:-${FIFO_PATH_DEFAULT}}"
    local fifo_dir
    fifo_dir="$(dirname "${fifo_path}")"
    mkdir -p "${fifo_dir}"
    if [ -e "${fifo_path}" ] && [ ! -p "${fifo_path}" ]; then
        say "Warning: ${fifo_path} exists but is not a FIFO; leaving it untouched."
        return
    fi
    if [ ! -p "${fifo_path}" ]; then
        mkfifo "${fifo_path}"
        chmod 666 "${fifo_path}" || true
        say "Created FIFO at ${fifo_path}"
    else
        say "FIFO already exists at ${fifo_path}"
    fi
}

ensure_script_permissions() {
    local script_path
    for script_path in \
        "${ROOT_DIR}/generate_certs.sh" \
        "${ROOT_DIR}/setup.sh" \
        "${ROOT_DIR}/sitebot/sitebot"
    do
        if [ -e "${script_path}" ]; then
            chmod +x "${script_path}" 2>/dev/null || true
        fi
    done
}

save_state_file() {
    mkdir -p "$(dirname "${STATE_FILE}")"
    : > "${STATE_FILE}"
    write_state_var() {
        local key="$1"
        local value="$2"
        printf '%s=%q\n' "${key}" "${value}" >> "${STATE_FILE}"
    }
    write_state_var SETUP_SITE_NAME "${SETUP_SITE_NAME:-GoFTPd}"
    write_state_var SETUP_SITE_SHORT "${SETUP_SITE_SHORT:-GoFTPd}"
    write_state_var SETUP_CERT_NAME "${SETUP_CERT_NAME:-GoFTPd}"
    write_state_var SETUP_GENERATE_CERTS "${SETUP_GENERATE_CERTS:-true}"
    write_state_var SETUP_FIFO_PATH "${SETUP_FIFO_PATH:-${FIFO_PATH_DEFAULT}}"
    write_state_var SETUP_SITEBOT_CONFIG_PATH "${SETUP_SITEBOT_CONFIG_PATH:-${SITEBOT_CONFIG_DEFAULT}}"
    write_state_var SETUP_DAEMON_MODE "${SETUP_DAEMON_MODE:-master}"
    write_state_var SETUP_LISTEN_PORT "${SETUP_LISTEN_PORT:-21212}"
    write_state_var SETUP_PUBLIC_IP "${SETUP_PUBLIC_IP:-1.2.3.4}"
    write_state_var SETUP_MASTER_LISTEN_HOST "${SETUP_MASTER_LISTEN_HOST:-0.0.0.0}"
    write_state_var SETUP_MASTER_CONTROL_PORT "${SETUP_MASTER_CONTROL_PORT:-1099}"
    write_state_var SETUP_TRANSFER_MODE "${SETUP_TRANSFER_MODE:-passthrough}"
    write_state_var SETUP_SLAVE_NAME "${SETUP_SLAVE_NAME:-SLAVE1}"
    write_state_var SETUP_SLAVE_MASTER_HOST "${SETUP_SLAVE_MASTER_HOST:-127.0.0.1}"
    write_state_var SETUP_SLAVE_MASTER_PORT "${SETUP_SLAVE_MASTER_PORT:-1099}"
    write_state_var SETUP_SLAVE_ROOTS "${SETUP_SLAVE_ROOTS:-./site}"
    write_state_var SETUP_SLAVE_BIND_IP "${SETUP_SLAVE_BIND_IP:-}"
    write_state_var SETUP_IRC_HOST "${SETUP_IRC_HOST:-irc.example.net}"
    write_state_var SETUP_IRC_PORT "${SETUP_IRC_PORT:-6697}"
    write_state_var SETUP_IRC_NICK "${SETUP_IRC_NICK:-GoSitebot}"
    write_state_var SETUP_IRC_USER "${SETUP_IRC_USER:-sitebot}"
    write_state_var SETUP_IRC_REALNAME "${SETUP_IRC_REALNAME:-GoSitebot v1.0}"
    write_state_var SETUP_IRC_PASSWORD "${SETUP_IRC_PASSWORD:-changeme}"
    write_state_var SETUP_IRC_SSL "${SETUP_IRC_SSL:-true}"
    write_state_var SETUP_PLUGIN_FTP_HOST "${SETUP_PLUGIN_FTP_HOST:-127.0.0.1}"
    write_state_var SETUP_PLUGIN_FTP_PORT "${SETUP_PLUGIN_FTP_PORT:-21212}"
    write_state_var SETUP_PLUGIN_FTP_USER "${SETUP_PLUGIN_FTP_USER:-goftpd}"
    write_state_var SETUP_PLUGIN_FTP_PASSWORD "${SETUP_PLUGIN_FTP_PASSWORD:-goftpd}"
    write_state_var SETUP_PLUGIN_FTP_TLS "${SETUP_PLUGIN_FTP_TLS:-true}"
    write_state_var SETUP_PLUGIN_FTP_INSECURE "${SETUP_PLUGIN_FTP_INSECURE:-true}"
    write_state_var SETUP_BNC_TARGET_HOST "${SETUP_BNC_TARGET_HOST:-127.0.0.1}"
    write_state_var SETUP_BNC_TARGET_PORT "${SETUP_BNC_TARGET_PORT:-21212}"
    write_state_var SETUP_MAIN_CHANNEL "${SETUP_MAIN_CHANNEL:-#goftpd}"
    write_state_var SETUP_SPAM_CHANNEL "${SETUP_SPAM_CHANNEL:-#goftpd-spam}"
    write_state_var SETUP_STAFF_CHANNEL "${SETUP_STAFF_CHANNEL:-#goftpd-staff}"
    write_state_var SETUP_FOREIGN_CHANNEL "${SETUP_FOREIGN_CHANNEL:-#goftpd-foreign}"
    write_state_var SETUP_ARCHIVE_CHANNEL "${SETUP_ARCHIVE_CHANNEL:-#goftpd-archive}"
    write_state_var SETUP_NUKE_CHANNEL "${SETUP_NUKE_CHANNEL:-#goftpd-nuke}"
    write_state_var SETUP_BLOWFISH_KEY_MAIN "${SETUP_BLOWFISH_KEY_MAIN:-YourBlowfishKeyHere123456}"
    write_state_var SETUP_BLOWFISH_KEY_SPAM "${SETUP_BLOWFISH_KEY_SPAM:-${SETUP_BLOWFISH_KEY_MAIN:-YourBlowfishKeyHere123456}}"
    write_state_var SETUP_BLOWFISH_KEY_STAFF "${SETUP_BLOWFISH_KEY_STAFF:-${SETUP_BLOWFISH_KEY_MAIN:-YourBlowfishKeyHere123456}}"
    write_state_var SETUP_BLOWFISH_KEY_FOREIGN "${SETUP_BLOWFISH_KEY_FOREIGN:-${SETUP_BLOWFISH_KEY_MAIN:-YourBlowfishKeyHere123456}}"
    write_state_var SETUP_BLOWFISH_KEY_ARCHIVE "${SETUP_BLOWFISH_KEY_ARCHIVE:-${SETUP_BLOWFISH_KEY_MAIN:-YourBlowfishKeyHere123456}}"
    write_state_var SETUP_BLOWFISH_KEY_NUKE "${SETUP_BLOWFISH_KEY_NUKE:-${SETUP_BLOWFISH_KEY_MAIN:-YourBlowfishKeyHere123456}}"
    write_state_var SETUP_DAEMON_PLUGIN_DATEDDIRS "${SETUP_DAEMON_PLUGIN_DATEDDIRS:-true}"
    write_state_var SETUP_DAEMON_PLUGIN_TVMAZE "${SETUP_DAEMON_PLUGIN_TVMAZE:-true}"
    write_state_var SETUP_DAEMON_PLUGIN_IMDB "${SETUP_DAEMON_PLUGIN_IMDB:-true}"
    write_state_var SETUP_DAEMON_PLUGIN_MEDIAINFO "${SETUP_DAEMON_PLUGIN_MEDIAINFO:-true}"
    write_state_var SETUP_DAEMON_PLUGIN_SPEEDTEST "${SETUP_DAEMON_PLUGIN_SPEEDTEST:-true}"
    write_state_var SETUP_DAEMON_PLUGIN_REQUEST "${SETUP_DAEMON_PLUGIN_REQUEST:-true}"
    write_state_var SETUP_DAEMON_PLUGIN_PRE "${SETUP_DAEMON_PLUGIN_PRE:-true}"
    write_state_var SETUP_CONFIGURE_SITEBOT_ON_SLAVE "${SETUP_CONFIGURE_SITEBOT_ON_SLAVE:-false}"
    write_state_var SETUP_SITEBOT_PLUGIN_ANNOUNCE "${SETUP_SITEBOT_PLUGIN_ANNOUNCE:-true}"
    write_state_var SETUP_SITEBOT_PLUGIN_TVMAZE "${SETUP_SITEBOT_PLUGIN_TVMAZE:-true}"
    write_state_var SETUP_SITEBOT_PLUGIN_IMDB "${SETUP_SITEBOT_PLUGIN_IMDB:-true}"
    write_state_var SETUP_SITEBOT_PLUGIN_NEWS "${SETUP_SITEBOT_PLUGIN_NEWS:-true}"
    write_state_var SETUP_SITEBOT_PLUGIN_FREE "${SETUP_SITEBOT_PLUGIN_FREE:-true}"
    write_state_var SETUP_SITEBOT_PLUGIN_AFFILS "${SETUP_SITEBOT_PLUGIN_AFFILS:-true}"
    write_state_var SETUP_SITEBOT_PLUGIN_REQUEST "${SETUP_SITEBOT_PLUGIN_REQUEST:-true}"
    write_state_var SETUP_SITEBOT_PLUGIN_BNC "${SETUP_SITEBOT_PLUGIN_BNC:-true}"
    write_state_var SETUP_SITEBOT_PLUGIN_BW "${SETUP_SITEBOT_PLUGIN_BW:-true}"
    write_state_var SETUP_SITEBOT_PLUGIN_ADMINCOMMANDER "${SETUP_SITEBOT_PLUGIN_ADMINCOMMANDER:-true}"
    say "Saved setup defaults to ${STATE_FILE}"
}

ensure_go_available() {
    if command -v go >/dev/null 2>&1; then
        say "Go already installed: $(go version)"
        return 0
    fi

    if [ -x /usr/local/go/bin/go ]; then
        export PATH="${PATH}:/usr/local/go/bin"
        say "Go already installed at /usr/local/go: $(go version)"
        return 0
    fi

    say "Go was not found. Installing the official Go toolchain..."

    local go_version os arch tarball url profile
    go_version="1.26.2"
    os="$(uname -s | tr '[:upper:]' '[:lower:]')"
    arch="$(uname -m)"
    case "${arch}" in
        x86_64|amd64) arch="amd64" ;;
        aarch64|arm64) arch="arm64" ;;
        *)
            say "Unsupported architecture: ${arch}"
            exit 1
            ;;
    esac

    tarball="go${go_version}.${os}-${arch}.tar.gz"
    url="https://go.dev/dl/${tarball}"

    say "Downloading: ${url}"
    curl -fL -o "${tarball}" "${url}" || {
        say "Download failed."
        exit 1
    }

    say "Extracting Go to /usr/local/go"
    sudo rm -rf /usr/local/go
    sudo tar -C /usr/local -xzf "${tarball}" || {
        say "Extract failed."
        exit 1
    }

    profile="${HOME}/.bashrc"
    [ "${os}" = "darwin" ] && profile="${HOME}/.zshrc"
    if ! grep -q "/usr/local/go/bin" "${profile}" 2>/dev/null; then
        echo 'export PATH=$PATH:/usr/local/go/bin' >> "${profile}"
    fi

    export PATH="${PATH}:/usr/local/go/bin"
    rm -f "${tarball}"

    if ! command -v go >/dev/null 2>&1; then
        say "Go installation failed. Add /usr/local/go/bin to your PATH and try again."
        exit 1
    fi

    say "Go successfully installed: $(go version)"
}

build_daemon_binary() {
    local daemon_config fifo_path
    daemon_config="${ROOT_DIR}/etc/config.yml"
    fifo_path="${SETUP_FIFO_PATH:-${FIFO_PATH_DEFAULT}}"

    say ""
    say_color "${C_YELLOW}${C_BOLD}" "Building daemon"
    say "Step 1: Download dependencies..."
    go mod download

    say ""
    say "Step 2: Tidy modules..."
    go mod tidy

    say ""
    say "Step 3: Build daemon..."
    if [ ! -d "${ROOT_DIR}/cmd/goftpd" ]; then
        say "Error: ${ROOT_DIR}/cmd/goftpd not found."
        exit 1
    fi

    go build -o "${ROOT_DIR}/goftpd" ./cmd/goftpd

    if [ ! -f "${ROOT_DIR}/goftpd" ]; then
        say "Daemon build failed."
        exit 1
    fi

    say ""
    say "Daemon build complete:"
    ls -lh "${ROOT_DIR}/goftpd"
    say ""
    say "Daemon config:"
    say "  ${daemon_config}"
    say ""
    say "Shared FIFO path:"
    say "  ${fifo_path}"
    say ""
    say "Run:"
    say "  ./goftpd"
}

build_sitebot_binary() {
    local sitebot_dir fifo_path expected_module current_module main_file main_dir
    sitebot_dir="${ROOT_DIR}/sitebot"
    fifo_path="${SETUP_FIFO_PATH:-${FIFO_PATH_DEFAULT}}"
    expected_module="goftpd/sitebot"

    say ""
    say_color "${C_YELLOW}${C_BOLD}" "Building sitebot"

    (
        cd "${sitebot_dir}"

        say "Step 1: Ensure Go module exists and is correct..."
        if [ ! -f "go.mod" ]; then
            say "No go.mod found, creating one..."
            go mod init "${expected_module}"
        else
            current_module="$(awk '/^module /{print $2; exit}' go.mod)"
            if [ "${current_module}" != "${expected_module}" ]; then
                say "Updating module path from '${current_module}' to '${expected_module}'"
                rm -f go.mod go.sum
                go mod init "${expected_module}"
            else
                say "go.mod already correct: ${current_module}"
            fi
        fi

        say ""
        say "Step 2: Download dependencies..."
        go mod download || true

        say ""
        say "Step 3: Tidy modules..."
        go mod tidy

        say ""
        say "Step 4: Detect build target..."
        main_file="$(grep -Rsl --include="*.go" '^package main$' . | head -n 1 || true)"
        if [ -z "${main_file}" ]; then
            say "Error: Could not find any Go file with 'package main'"
            exit 1
        fi

        main_dir="$(dirname "${main_file}")"
        say "Found main package in: ${main_dir}"

        say ""
        say "Step 5: Build sitebot..."
        go build -o sitebot "${main_dir}"

        if [ ! -f "sitebot" ]; then
            say "Sitebot build failed."
            exit 1
        fi
    )

    say ""
    say "Sitebot build complete:"
    ls -lh "${ROOT_DIR}/sitebot/sitebot"
    say ""
    say "Sitebot config:"
    say "  ${ROOT_DIR}/sitebot/etc/config.yml"
    say ""
    say "Shared FIFO path:"
    say "  ${fifo_path}"
    say ""
    say "Run:"
    say "  ./sitebot/sitebot -config ./sitebot/etc/config.yml"
}

build_everything() {
    ensure_go_available
    build_daemon_binary
    build_sitebot_binary
}

show_usage() {
    show_banner
    say_color "${C_BOLD}" "GoFTPd setup"
    say ""
    say_color "${C_YELLOW}" "Usage:"
    say "  ./setup.sh install   Run guided install/config setup"
    say "  ./setup.sh build     Build daemon and sitebot only"
    say "  ./setup.sh clean     Back up generated configs and reset install state"
    say "  ./setup.sh backup    Alias for clean"
    say "  ./setup.sh help      Show this help"
    say ""
    say "Notes:"
    say "  - 'install' loads ${STATE_FILE} as defaults when you allow it."
    say "  - 'build' just runs the daemon and sitebot build scripts."
    say "  - 'clean' keeps ${STATE_FILE} but backs up generated configs, FIFO, and certs."
}

case "${1:-help}" in
    --clean|clean|backup|--reset|reset)
        run_cleanup_mode
        ;;
    build|--build)
        show_banner
        ensure_script_permissions
        build_everything
        say ""
        say "Build complete."
        exit 0
        ;;
    install|--install)
        ;;
    help|--help|-h|"")
        show_usage
        exit 0
        ;;
    *)
        say "Unknown command: ${1}"
        say ""
        show_usage
        exit 1
        ;;
esac

show_banner
say "This will only ask setup questions when a real config file is missing."

configure_daemon
configure_sitebot
save_state_file
ensure_script_permissions
ensure_fifo
build_everything

say ""
say "Setup complete."
say "If a plugin config is missing later, GoFTPd and the sitebot will now"
say "tell you which config_file was checked and suggest copying the .dist file."
