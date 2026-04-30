import json
import os
import re
import sys
from datetime import datetime

userdata_root, out_root, import_log, existing_passwd_path, existing_group_path = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4], sys.argv[5]
users_dir = os.path.join(userdata_root, "users", "javabeans")
groups_dir = os.path.join(userdata_root, "groups", "javabeans")
etc_dir = os.path.join(out_root, "etc")
out_users_dir = os.path.join(etc_dir, "users")
out_groups_dir = os.path.join(etc_dir, "groups")
os.makedirs(out_users_dir, exist_ok=True)
os.makedirs(out_groups_dir, exist_ok=True)

existing_group_ids = {}
existing_user_ids = {}

def load_existing_ids():
    if os.path.isfile(existing_group_path):
        with open(existing_group_path, "r", encoding="utf-8", errors="ignore") as handle:
            for line in handle:
                line = line.strip()
                if not line or line.startswith("#"):
                    continue
                parts = line.split(":")
                if len(parts) >= 3 and parts[0]:
                    try:
                        existing_group_ids[parts[0]] = int(parts[2])
                    except Exception:
                        pass
    if os.path.isfile(existing_passwd_path):
        with open(existing_passwd_path, "r", encoding="utf-8", errors="ignore") as handle:
            for line in handle:
                line = line.strip()
                if not line or line.startswith("#"):
                    continue
                parts = line.split(":")
                if len(parts) >= 4 and parts[0]:
                    try:
                        existing_user_ids[parts[0]] = int(parts[2])
                    except Exception:
                        pass

load_existing_ids()

def safe_int(value, default=0):
    try:
        if isinstance(value, bool):
            return int(value)
        if isinstance(value, (int, float)):
            return int(value)
        if isinstance(value, str) and value.strip():
            return int(float(value))
    except Exception:
        pass
    return default

def safe_float(value, default=0.0):
    try:
        if isinstance(value, bool):
            return float(value)
        if isinstance(value, (int, float)):
            return float(value)
        if isinstance(value, str) and value.strip():
            return float(value)
    except Exception:
        pass
    return default

def flatten_masks(obj, out):
    if isinstance(obj, dict):
        host = ""
        for key in ("_hostMask", "_hostmask", "_mask", "mask", "hostMask"):
            val = obj.get(key)
            if isinstance(val, str) and val.strip():
                host = val.strip()
                break
        ident = ""
        for key in ("_ident", "ident"):
            val = obj.get(key)
            if isinstance(val, str) and val.strip():
                ident = val.strip()
                break
        if host:
            out.append(f"{ident}@{host}" if ident and ident != "*" else host)
        for value in obj.values():
            flatten_masks(value, out)
    elif isinstance(obj, list):
        for item in obj:
            flatten_masks(item, out)

def extract_keyed_map(data):
    result = {}
    if not isinstance(data, dict):
        return result
    keys = data.get("@keys")
    items = data.get("@items")
    if not isinstance(keys, list) or not isinstance(items, list):
        return result
    for idx, key_meta in enumerate(keys):
        if idx >= len(items):
            break
        key_name = None
        if isinstance(key_meta, dict):
            key_name = key_meta.get("_key")
        if not key_name:
            continue
        item = items[idx]
        value = item.get("value") if isinstance(item, dict) and "value" in item else item
        result[str(key_name).lower()] = value
    return result

def stat_tuple_from_arrays(bytes_arr, files_arr, index):
    if not isinstance(bytes_arr, list) or not isinstance(files_arr, list):
        return (0, 0, 0)
    if index >= len(bytes_arr) or index >= len(files_arr):
        return (0, 0, 0)
    return (safe_int(files_arr[index]), safe_int(bytes_arr[index]), 0)

def epoch_from_value(value):
    if isinstance(value, (int, float)):
        return int(value)
    if isinstance(value, str):
        text = value.strip()
        if not text:
            return 0
        if re.fullmatch(r"\d+", text):
            return int(text)
        try:
            if text.endswith("Z"):
                text = text[:-1] + "+00:00"
            return int(datetime.fromisoformat(text).timestamp())
        except Exception:
            return 0
    if isinstance(value, dict):
        for key in ("value", "_time", "time", "millis", "epoch", "timestamp"):
            if key in value:
                return epoch_from_value(value[key])
    return 0

generated_groups = {}
group_order = []
generated_users = []
next_gid = max([299, *existing_group_ids.values()]) + 1 if existing_group_ids else 300
next_uid = max([999, *existing_user_ids.values()]) + 1 if existing_user_ids else 1000

def ensure_group(name):
    global next_gid
    clean = (name or "").strip()
    if not clean:
        clean = "NoGroup"
    if clean not in generated_groups:
        if clean in existing_group_ids:
            generated_groups[clean] = existing_group_ids[clean]
        else:
            generated_groups[clean] = next_gid
            next_gid += 1
        group_order.append(clean)
    return clean

for filename in sorted(os.listdir(users_dir)):
    src_path = os.path.join(users_dir, filename)
    if os.path.isdir(src_path) or not filename.lower().endswith(".json"):
        continue
    with open(src_path, "r", encoding="utf-8") as handle:
        data = json.load(handle)

    username = (data.get("_username") or os.path.splitext(filename)[0]).strip()
    if not username:
        continue

    primary_group = ensure_group(data.get("_group") or "NoGroup")
    secondary_groups = []
    for group in data.get("_groups", []) or []:
        if not isinstance(group, str):
            continue
        group = ensure_group(group)
        if group != primary_group and group not in secondary_groups:
            secondary_groups.append(group)

    keyed = extract_keyed_map(data.get("_data"))
    ratio = safe_int(keyed.get("ratio"), 3)
    if ratio < 0:
        ratio = 0
    credits = safe_int(data.get("_credits"), 0)
    added = epoch_from_value(keyed.get("created"))
    last_seen = epoch_from_value(keyed.get("lastseen"))
    deleted = primary_group.lower() == "deleted" or any(g.lower() == "deleted" for g in secondary_groups)
    flags = "3"
    if "siteop" in {primary_group.lower(), *(g.lower() for g in secondary_groups)} and "1" not in flags:
        flags += "1"
    if deleted and "6" not in flags:
        flags += "6"
    tagline = str(keyed.get("tagline") or "Imported from DrFTPD v3").strip()
    if not tagline:
        tagline = "Imported from DrFTPD v3"

    masks = []
    flatten_masks(data.get("_hostMasks"), masks)
    dedup_masks = []
    seen_masks = set()
    for mask in masks:
        mask = str(mask).strip()
        if mask and mask not in seen_masks:
            seen_masks.add(mask)
            dedup_masks.append(mask)

    up_bytes = data.get("_uploadedBytes")
    up_files = data.get("_uploadedFiles")
    dn_bytes = data.get("_downloadedBytes")
    dn_files = data.get("_downloadedFiles")

    stats = {
        "ALLUP": stat_tuple_from_arrays(up_bytes, up_files, 0),
        "DAYUP": stat_tuple_from_arrays(up_bytes, up_files, 1),
        "WKUP": stat_tuple_from_arrays(up_bytes, up_files, 2),
        "MONTHUP": stat_tuple_from_arrays(up_bytes, up_files, 3),
        "ALLDN": stat_tuple_from_arrays(dn_bytes, dn_files, 0),
        "DAYDN": stat_tuple_from_arrays(dn_bytes, dn_files, 1),
        "WKDN": stat_tuple_from_arrays(dn_bytes, dn_files, 2),
        "MONTHDN": stat_tuple_from_arrays(dn_bytes, dn_files, 3),
    }

    generated_users.append({
        "username": username,
        "uid": existing_user_ids.get(username, next_uid),
        "gid": generated_groups[primary_group],
        "group": primary_group,
        "groups": secondary_groups,
        "flags": "".join(sorted(set(flags), key=flags.index)),
        "tagline": tagline,
        "credits": credits,
        "ratio": ratio,
        "added": added,
        "last_seen": last_seen,
        "masks": dedup_masks,
        "stats": stats,
    })
    if username not in existing_user_ids:
        next_uid += 1

if os.path.isdir(groups_dir):
    for filename in sorted(os.listdir(groups_dir)):
        if filename.lower().endswith(".json"):
            ensure_group(os.path.splitext(filename)[0])

with open(os.path.join(etc_dir, "group"), "w", encoding="utf-8", newline="\n") as handle:
    for group in group_order:
        desc = "Imported from DrFTPD v3"
        handle.write(f"{group}:{desc}:{generated_groups[group]}:\n")

with open(os.path.join(etc_dir, "passwd"), "w", encoding="utf-8", newline="\n") as handle:
    for entry in generated_users:
        handle.write(
            f"{entry['username']}:!drftpd-reset-required!:{entry['uid']}:{entry['gid']}:drftpd3:/site:/bin/false\n"
        )

for group in group_order:
    with open(os.path.join(out_groups_dir, group), "w", encoding="utf-8", newline="\n") as handle:
        handle.write(f"GROUP {group}\n")
        handle.write("SLOTS -1 0 0 0\n")
        handle.write("GROUPNFO Imported from DrFTPD v3\n")
        handle.write("SIMULT 0\n")

for entry in generated_users:
    user_path = os.path.join(out_users_dir, entry["username"])
    with open(user_path, "w", encoding="utf-8", newline="\n") as handle:
        handle.write("USER Imported from DrFTPD v3\n")
        handle.write("GENERAL 0,0 -1 0 0\n")
        handle.write("LOGINS 2 0 -1 -1\n")
        handle.write("TIMEFRAME 0 0\n")
        handle.write(f"FLAGS {entry['flags']}\n")
        handle.write(f"TAGLINE {entry['tagline']}\n")
        handle.write("HOMEDIR /\n")
        handle.write("DIR /\n")
        handle.write(f"ADDED {entry['added']}\n")
        handle.write("EXPIRES 0\n")
        handle.write(f"CREDITS {entry['credits']}\n")
        handle.write(f"RATIO {entry['ratio']}\n")
        handle.write("UPLOADSLOTS 0\n")
        handle.write("DOWNLOADSLOTS 0\n")
        for key in ("ALLUP", "ALLDN", "WKUP", "WKDN", "DAYUP", "DAYDN", "MONTHUP", "MONTHDN"):
            files, bytes_value, meta = entry["stats"].get(key, (0, 0, 0))
            handle.write(f"{key} {files} {bytes_value} {meta}\n")
        handle.write("NUKE 0 0 0\n")
        handle.write(f"TIME 0 {entry['last_seen']} 0 0\n")
        handle.write(f"PRIMARY_GROUP {entry['group']}\n")
        handle.write(f"GROUP {entry['group']} 0\n")
        for group in sorted(entry["groups"]):
            handle.write(f"GROUP {group} 0\n")
        for mask in entry["masks"]:
            handle.write(f"IP {mask}\n")

with open(import_log, "w", encoding="utf-8", newline="\n") as handle:
    handle.write(f"mode: drftpd3\n")
    handle.write(f"users_source: {users_dir}\n")
    handle.write(f"groups_source: {groups_dir if os.path.isdir(groups_dir) else '(derived from users)'}\n")
    handle.write(f"imported_users: {len(generated_users)}\n")
    handle.write(f"imported_groups: {len(group_order)}\n")
    handle.write("passwords: reset-required\n")
    for entry in generated_users:
        groups = ",".join([entry["group"], *entry["groups"]])
        handle.write(
            f"user:{entry['username']} uid:{entry['uid']} gid:{entry['gid']} primary:{entry['group']} groups:{groups}\n"
        )
