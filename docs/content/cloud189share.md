---
title: "China Telecom Cloud 189 Share"
description: "Rclone docs for Cloud 189 shares"
versionIntroduced: "v1.76.1"
---

# China Telecom Cloud 189 Share

This backend lists and downloads a [Cloud 189](https://cloud.189.cn)
share (`cloud.189.cn/t/<code>`). Uploading into a share is not
supported.

## Configuration

```console
rclone config
```

```text
n/s/q> n
name> remote
Storage> cloud189share
share_code> SHARE_CODE
share_pwd> OPTIONAL_ACCESS_CODE
```

```console
rclone lsf remote:
rclone copy remote:file.txt /tmp/
```

Some shares need a logged-in cookie from cloud.189.cn to download.

### Standard options

#### --cloud189share-share_code

Share code.

#### --cloud189share-share_pwd

Share access code, if any.

### Advanced options

#### --cloud189share-cookie

Optional Cloud 189 cookie.
