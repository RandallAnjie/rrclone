---
title: "Aliyun Drive"
description: "Rclone docs for Aliyun Drive"
versionIntroduced: "v1.76.1"
---

# Aliyun Drive

[Aliyun Drive](https://www.alipan.com) (阿里云盘) is a Chinese cloud
storage provider. This backend uses the **Aliyun Drive Open API**
(`openapi.alipan.com`).

Create an app at [alipan.com/developer](https://www.alipan.com/developer)
and complete OAuth to obtain a refresh token, client ID and client
secret. Do not use a third-party token broker.

## Configuration

```console
rclone config
```

```text
n/s/q> n
name> remote
Storage> alipan
refresh_token> YOUR_REFRESH_TOKEN
client_id> YOUR_CLIENT_ID
client_secret> YOUR_CLIENT_SECRET
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

Aliyun Drive refresh token.

#### --alipan-client_id

OAuth client ID.

#### --alipan-client_secret

OAuth client secret.

### Advanced options

#### --alipan-access_token

Access token. Optional if refresh credentials are set.

#### --alipan-drive_id

Drive ID. Leave blank to use the account default drive.

#### --alipan-root_folder_id

ID of the root folder. Leave blank to use `root`.

#### --alipan-endpoint

Endpoint for the Aliyun Drive Open API. Default `https://openapi.alipan.com`.
