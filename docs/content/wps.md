---
title: "WPS Drive"
description: "Rclone docs for WPS Drive"
versionIntroduced: "v1.76.1"
---

# WPS Drive

[WPS Drive](https://drive.wps.cn) WPS / Kdocs drive. Cookie from drive.wps.cn.

## Configuration

```console
rclone config
```

```text
n/s/q> n
name> remote
Storage> wps
```

```console
rclone lsf remote:
```

Do not use a third-party token broker.

WPS / Kdocs drive. Cookie from drive.wps.cn.

### Standard options

#### --wps-cookie

WPS Drive cookie string.

