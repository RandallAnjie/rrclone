---
title: "Quark Share"
description: "Rclone docs for Quark Drive shares"
versionIntroduced: "v1.76.1"
---

# Quark Share

This backend lists and downloads a [Quark Drive](https://pan.quark.cn)
share (`pan.quark.cn/s/<pwd_id>`). It uses the sharepage token/detail
API on `drive-pc.quark.cn`. Uploading into a share is not supported.

A logged-in cookie is required for download on most shares.

## Configuration

```console
rclone config
```

```text
n/s/q> n
name> remote
Storage> quarkshare
cookie> __uid=...; __puus=...
share_key> SHARE_PWD_ID
share_pwd> OPTIONAL_PASSCODE
```

```console
rclone lsf remote:
rclone copy remote:file.txt /tmp/
```

### Standard options

#### --quarkshare-cookie

Quark web cookie string.

#### --quarkshare-share_key

Share `pwd_id`.

#### --quarkshare-share_pwd

Share passcode, if any.
