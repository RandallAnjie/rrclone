---
title: "WoPan"
description: "Rclone docs for WoPan"
versionIntroduced: "v1.76.1"
---

# WoPan

[WoPan](https://pan.wo.cn) China Unicom WoPan. Token is a refresh_token.

## Configuration

```console
rclone config
```

```text
n/s/q> n
name> remote
Storage> wopan
```

```console
rclone lsf remote:
```

Do not use a third-party token broker.

China Unicom WoPan. Token is a refresh_token.

### Standard options

#### --wopan-token

WoPan API token.

