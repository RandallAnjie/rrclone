---
title: "UC Drive"
description: "Rclone docs for UC Drive"
versionIntroduced: "v1.76.1"
---

# UC Drive

[UC Drive](https://drive.uc.cn) (UC 网盘) is a Chinese cloud storage
provider. This backend uses the **UC web API** (`pc-api.uc.cn`) with
the cookies from a logged-in browser session. The protocol is the
same as Quark Drive with a different host.

## Configuration

Log in to [drive.uc.cn](https://drive.uc.cn) and copy the Cookie
header from DevTools → Network.

```console
rclone config
```

```text
n/s/q> n
name> remote
Storage> uc
cookie> ...
```

```console
rclone lsf remote:
```

Cookies expire. Copy a fresh cookie string if rclone starts returning
login errors.

### Standard options

#### --uc-cookie

UC Drive web cookie string.
