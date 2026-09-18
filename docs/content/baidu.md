---
title: "Baidu Netdisk"
description: "Rclone docs for Baidu Netdisk (百度网盘)"
versionIntroduced: "v1.76.1"
---

# Baidu Netdisk

[Baidu Netdisk](https://pan.baidu.com) (百度网盘) is a Chinese consumer
cloud drive. This backend talks to `pan.baidu.com` with either a
browser cookie (easier) or an [open platform](https://pan.baidu.com/union/doc)
OAuth token.

It does **not** use third-party token brokers. Bring your own cookie
or your own Baidu open-platform app.

## Configuration

### Cookie login (recommended)

Sign in at [pan.baidu.com](https://pan.baidu.com) in a browser, open
DevTools → Application → Cookies, and copy **BDUSS** and **STOKEN**.
Paste them as one string:

```text
BDUSS=...; STOKEN=...
```

```console
rclone config
```

```text
n) New remote
name> pan
Storage> baidu
cookie> BDUSS=...; STOKEN=...
```

BDUSS is required. STOKEN is recommended for upload and delete.
Cookies expire; copy a fresh pair if rclone starts returning login
errors (`errno -6` / `-7`).

### Open platform token

Create an app at the [Baidu Netdisk open platform](https://pan.baidu.com/union/doc),
complete OAuth with the `netdisk` scope, and set:

```ini
[pan]
type = baidu
access_token = ...
refresh_token = ...
client_id = your-app-key
client_secret = your-secret-key
```

When `access_token` is set it takes precedence over `cookie`.
`refresh_token` plus `client_id` / `client_secret` renews the access
token when it expires.

## Usage

```console
rclone lsf pan:
rclone copy /home/source pan:backup
rclone mount pan: /mnt/baidu --vfs-cache-mode writes
```

Restrict rclone to a folder with `root_folder_path`, for example
`/backup`.

## Direct download links

`rclone link pan:path/to/file` returns Baidu's temporary **dlink**
(直链). wget/curl usually need:

```console
curl -A 'pan.baidu.com' -L 'https://...'
```

The URL expires (often within hours). Directories cannot be linked.
`--unlink` is not supported. Cookie downloads may also need the
session User-Agent; open-platform tokens use `pan.baidu.com`.

## Limitations

- Download links from the official API often require the User-Agent
  `pan.baidu.com` (the default when using `access_token`). Cookie
  downloads use a browser User-Agent.
- Cookie login uses the web `/api/*` endpoints plus `bdstoken`. An
  `access_token` uses the open-platform `xpan` API instead.
- Baidu may rate-limit or require a captcha (`errno 31034`, `-20`).
  rclone retries those; persistent failures mean wait or refresh the
  cookie.
- Empty directories are supported. Modification time cannot be set.
- Large uploads use 4 MiB slices via `superfile2`. Rapid upload
  (秒传) is used when the server already has the content.
- Listed MD5 values can be scrambled for some accounts; rclone still
  reports MD5 when the string looks like a checksum.
