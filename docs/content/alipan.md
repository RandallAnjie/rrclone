---
title: "Aliyun Drive"
description: "Rclone docs for Aliyun Drive"
versionIntroduced: "v1.76.1"
---

# Aliyun Drive

[Aliyun Drive](https://www.alipan.com) (阿里云盘) is a Chinese cloud
storage provider.

The easy path is a **web refresh_token** only. Log in at
[alipan.com](https://www.alipan.com), open DevTools → Application →
Local Storage, and copy the `refresh_token` (or the token JSON's
`refresh_token` field). rclone uses the web API
(`auth.alipan.com` / `api.alipan.com`) and does not need a developer
app.

To use the official Open API (`openapi.alipan.com`) instead, create
an app at [alipan.com/developer](https://www.alipan.com/developer)
and set `client_id` and `client_secret` together with the Open API
refresh token. Do not use a third-party token broker.

## Configuration

```console
rclone config
```

```text
n/s/q> n
name> remote
Storage> alipan
refresh_token> YOUR_REFRESH_TOKEN
```

```console
rclone lsf remote:
rclone copy /home/source remote:backup
```

### Modification times and hashes

Aliyun Drive returns RFC3339 modification times. rclone cannot set
them. SHA-1 is used for rapid upload when the source provides it.

### Standard options

#### --alipan-refresh_token

Aliyun Drive refresh token from www.alipan.com, or from an Open API
app.

### Advanced options

#### --alipan-client_id

OAuth client ID. Leave empty for web refresh_token login.

#### --alipan-client_secret

OAuth client secret. Required with client_id for the Open API.

#### --alipan-access_token

Access token. Optional if refresh_token is set.

#### --alipan-drive_id

Drive ID. Leave blank to use the account default drive.

#### --alipan-root_folder_id

ID of the root folder. Leave blank to use `root`.

#### --alipan-endpoint

Endpoint for the Aliyun Drive API. Web login defaults to
`https://api.alipan.com`. Open API default is
`https://openapi.alipan.com`.

#### --alipan-token_endpoint

Token URL for web refresh_token login. Default
`https://auth.alipan.com/v2/account/token`.

## Direct download links

`rclone link remote:path/to/file` returns a time-limited download URL
from Aliyun Drive (typically a few hours). Directories cannot be linked.
