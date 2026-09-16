---
title: "Lenovo NAS Share"
description: "Rclone docs for Lenovo NAS Share"
versionIntroduced: "v1.76.1"
---

# Lenovo NAS Share

[Lenovo NAS Share](https://siot-share.lenovo.com.cn) Lenovo NAS public share.

## Configuration

```console
rclone config
```

```text
n/s/q> n
name> remote
Storage> lenovonas
```

```console
rclone lsf remote:
```

Do not use a third-party token broker.

Lenovo NAS public share.

### Standard options

#### --lenovonas-share_key

Share key or share id.
#### --lenovonas-share_pwd

Share password, if any.

