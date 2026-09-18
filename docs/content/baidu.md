---
title: "Baidu Netdisk"
description: "Rclone docs for Baidu Netdisk"
versionIntroduced: "v1.76.1"
---

# Baidu Netdisk

[Baidu Netdisk](https://pan.baidu.com) (百度网盘) is a Chinese cloud
storage provider. This backend uses the **Baidu Open API**
(`pan.baidu.com/rest/2.0`).

Create an app at
[pan.baidu.com/union/console/applist](https://pan.baidu.com/union/console/applist)
and complete OAuth. rclone refreshes the access token with `client_id`
and `client_secret`. Do not use a third-party token broker.

## Configuration

```console
rclone config
```

```text
n/s/q> n
name> remote
Storage> baidu
refresh_token> YOUR_REFRESH_TOKEN
client_id> YOUR_CLIENT_ID
client_secret> YOUR_CLIENT_SECRET
```

```console
rclone lsf remote:
rclone copy /home/source remote:backup
```

### Modification times and hashes

Baidu Netdisk returns second-precision modification times. rclone
cannot set them. MD5 is returned for files; Baidu's MD5 is not always
a plain content hash.

### Standard options

#### --baidu-refresh_token

Baidu Netdisk refresh token.

#### --baidu-client_id

OAuth client ID.

#### --baidu-client_secret

OAuth client secret.

### Advanced options

#### --baidu-access_token

Access token. Optional if refresh credentials are set.

#### --baidu-root_folder_path

Root folder path. Leave blank to use `/`.

#### --baidu-endpoint

Endpoint for the Baidu Netdisk REST API. Default `https://pan.baidu.com/rest/2.0`.
