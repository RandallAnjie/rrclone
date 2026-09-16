---
title: "Quark Drive"
description: "Rclone docs for Quark Drive"
versionIntroduced: "v1.76.1"
---

# Quark Drive

[Quark Drive](https://pan.quark.cn) (夸克网盘) is a Chinese cloud
storage provider. This backend uses the **Quark web API**
(`drive.quark.cn`) with the cookies from a logged-in browser session.

## Configuration

Log in to [pan.quark.cn](https://pan.quark.cn) and copy the Cookie
header from DevTools → Network.

```console
rclone config
```

```text
n/s/q> n
name> remote
Storage> quark
cookie> __uid=...; __puus=...
```

```console
rclone lsf remote:
```

Cookies expire. Copy a fresh cookie string if rclone starts returning
login errors.

### Modification times and hashes

Quark Drive returns millisecond modification times. rclone cannot
set them. Hash reuse on upload uses MD5 and SHA-1 when the server
accepts them.

### Standard options

#### --quark-cookie

Quark web cookie string.

### Advanced options

#### --quark-root_folder_id

ID of the root folder. Leave blank to use the account root.

#### --quark-endpoint

Endpoint for the Quark Drive API. Default `https://drive.quark.cn/1/clouddrive`.
