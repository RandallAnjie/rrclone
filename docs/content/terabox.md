---
title: "TeraBox"
description: "Rclone docs for TeraBox"
versionIntroduced: "v1.76.1"
---

# TeraBox

[TeraBox](https://www.terabox.com) TeraBox is the international Baidu Netdisk. Copy the Cookie header from a logged-in www.terabox.com session.

## Configuration

```console
rclone config
```

```text
n/s/q> n
name> remote
Storage> terabox
```

```console
rclone lsf remote:
```

Do not use a third-party token broker.

TeraBox is the international Baidu Netdisk. Copy the Cookie header from a logged-in www.terabox.com session.

### Standard options

#### --terabox-cookie

TeraBox web cookie from www.terabox.com.

