---
title: "China Telecom Cloud 189"
description: "Rclone docs for China Telecom Cloud 189"
versionIntroduced: "v1.76.1"
---

# China Telecom Cloud 189

[Cloud 189](https://cloud.189.cn) (天翼云盘) is China Telecom's
consumer cloud storage. This backend uses the **Cloud 189 web API**
(`cloud.189.cn`) with the cookies from a logged-in browser session.

## Configuration

Log in to [cloud.189.cn](https://cloud.189.cn) and copy the Cookie
header from DevTools → Network.

```console
rclone config
```

```text
n/s/q> n
name> remote
Storage> cloud189
cookie> COOKIE_FROM_BROWSER
```

```console
rclone lsf remote:
```

Cookies expire. Copy a fresh cookie string if rclone starts returning
login errors.

### Modification times and hashes

Cloud 189 returns `lastOpTime` in China Standard Time. rclone cannot
set modification times. Hashes are not exposed by this API.

### Standard options

#### --cloud189-cookie

Cloud 189 web cookie string.

### Advanced options

#### --cloud189-root_folder_id

ID of the root folder. Leave blank to use the account root (`-11`).

#### --cloud189-endpoint

Endpoint for the Cloud 189 web API. Default `https://cloud.189.cn`.
