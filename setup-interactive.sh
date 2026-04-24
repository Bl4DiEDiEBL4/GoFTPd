#!/bin/bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${ROOT_DIR}"
STATE_FILE="${ROOT_DIR}/etc/setup-interactive.env"
FIFO_PATH_DEFAULT="${ROOT_DIR}/etc/goftpd.sitebot.fifo"
SITEBOT_CONFIG_DEFAULT="${ROOT_DIR}/sitebot/etc/config.yml"

if [ -f "${STATE_FILE}" ]; then
    # shellcheck disable=SC1090
    . "${STATE_FILE}"
fi

say() {
    printf '%s\n' "$*"
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
        $0 ~ pattern { print replacement; next }
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
    local blowfish_key="$8"
    awk -v main_channel="${main_channel}" \
        -v spam_channel="${spam_channel}" \
        -v staff_channel="${staff_channel}" \
        -v foreign_channel="${foreign_channel}" \
        -v archive_channel="${archive_channel}" \
        -v nuke_channel="${nuke_channel}" \
        -v blowfish_key="${blowfish_key}" '
        BEGIN { in_keys = 0 }
        /^  keys:$/ {
            print
            print "    \"" main_channel "\": \"cbc:" blowfish_key "\""
            print "    \"" spam_channel "\": \"cbc:" blowfish_key "\""
            print "    \"" staff_channel "\": \"cbc:" blowfish_key "\""
            print "    \"" foreign_channel "\": \"cbc:" blowfish_key "\""
            print "    \"" archive_channel "\": \"cbc:" blowfish_key "\""
            print "    \"" nuke_channel "\": \"cbc:" blowfish_key "\""
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

    local main_channel spam_channel staff_channel foreign_channel archive_channel nuke_channel blowfish_key enabled_bool
    local fifo_path
    main_channel="$(prompt_default 'Main IRC channel' "${SETUP_MAIN_CHANNEL:-#goftpd}")"
    spam_channel="$(prompt_default 'Spam IRC channel' "${SETUP_SPAM_CHANNEL:-#goftpd-spam}")"
    staff_channel="$(prompt_default 'Staff IRC channel' "${SETUP_STAFF_CHANNEL:-#goftpd-staff}")"
    foreign_channel="$(prompt_default 'Foreign IRC channel' "${SETUP_FOREIGN_CHANNEL:-#goftpd-foreign}")"
    archive_channel="$(prompt_default 'Archive IRC channel' "${SETUP_ARCHIVE_CHANNEL:-#goftpd-archive}")"
    nuke_channel="$(prompt_default 'Nuke IRC channel' "${SETUP_NUKE_CHANNEL:-#goftpd-nuke}")"
    blowfish_key="$(prompt_default 'Shared Blowfish key for configured channels' "${SETUP_BLOWFISH_KEY:-YourBlowfishKeyHere123456}")"

    SETUP_MAIN_CHANNEL="${main_channel}"
    SETUP_SPAM_CHANNEL="${spam_channel}"
    SETUP_STAFF_CHANNEL="${staff_channel}"
    SETUP_FOREIGN_CHANNEL="${foreign_channel}"
    SETUP_ARCHIVE_CHANNEL="${archive_channel}"
    SETUP_NUKE_CHANNEL="${nuke_channel}"
    SETUP_BLOWFISH_KEY="${blowfish_key}"
    fifo_path="${SETUP_FIFO_PATH:-${FIFO_PATH_DEFAULT}}"
    SETUP_FIFO_PATH="${fifo_path}"
    SETUP_SITEBOT_CONFIG_PATH="${ROOT_DIR}/sitebot/etc/config.yml"

    replace_matching_line "${sitebot_config}" '^event_fifo:' "event_fifo: \"${fifo_path}\""

    set_sitebot_channel_anchor "${sitebot_config}" "main" "chan_main" "${main_channel}"
    set_sitebot_channel_anchor "${sitebot_config}" "spam" "chan_spam" "${spam_channel}"
    set_sitebot_channel_anchor "${sitebot_config}" "staff" "chan_staff" "${staff_channel}"
    set_sitebot_channel_anchor "${sitebot_config}" "foreign" "chan_foreign" "${foreign_channel}"
    set_sitebot_channel_anchor "${sitebot_config}" "archive" "chan_archive" "${archive_channel}"
    set_sitebot_channel_anchor "${sitebot_config}" "nuke" "chan_nuke" "${nuke_channel}"
    rewrite_sitebot_irc_channels "${sitebot_config}" "${main_channel}" "${spam_channel}" "${staff_channel}"
    rewrite_sitebot_invite_channel "${sitebot_config}" "${staff_channel}"
    rewrite_sitebot_encryption_keys "${sitebot_config}" "${main_channel}" "${spam_channel}" "${staff_channel}" "${foreign_channel}" "${archive_channel}" "${nuke_channel}" "${blowfish_key}"
    configure_sitebot_plugin_channels "${main_channel}" "${staff_channel}" "${nuke_channel}"

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
        "${ROOT_DIR}/build.sh" \
        "${ROOT_DIR}/generate_certs.sh" \
        "${ROOT_DIR}/setup-interactive.sh" \
        "${ROOT_DIR}/sitebot/build.sh"
    do
        if [ -f "${script_path}" ]; then
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
    write_state_var SETUP_MAIN_CHANNEL "${SETUP_MAIN_CHANNEL:-#goftpd}"
    write_state_var SETUP_SPAM_CHANNEL "${SETUP_SPAM_CHANNEL:-#goftpd-spam}"
    write_state_var SETUP_STAFF_CHANNEL "${SETUP_STAFF_CHANNEL:-#goftpd-staff}"
    write_state_var SETUP_FOREIGN_CHANNEL "${SETUP_FOREIGN_CHANNEL:-#goftpd-foreign}"
    write_state_var SETUP_ARCHIVE_CHANNEL "${SETUP_ARCHIVE_CHANNEL:-#goftpd-archive}"
    write_state_var SETUP_NUKE_CHANNEL "${SETUP_NUKE_CHANNEL:-#goftpd-nuke}"
    write_state_var SETUP_BLOWFISH_KEY "${SETUP_BLOWFISH_KEY:-YourBlowfishKeyHere123456}"
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

build_everything() {
    say ""
    say "Building daemon..."
    ./build.sh

    say ""
    say "Building sitebot..."
    (
        cd sitebot
        ./build.sh
    )
}

say "=================================================="
say "GoFTPd Interactive Setup"
say "=================================================="
say "This will only ask setup questions when a real config file is missing."

configure_daemon
configure_sitebot
save_state_file
ensure_script_permissions
ensure_fifo
build_everything

say ""
say "Interactive setup complete."
say "If a plugin config is missing later, GoFTPd and the sitebot will now"
say "tell you which config_file was checked and suggest copying the .dist file."
