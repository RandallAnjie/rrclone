---
title: "115 Share"
description: "Rclone docs for 115 Share"
versionIntroduced: "v1.76.1"
---

# 115 Share

[115 Share](https://115.com) 115 Drive public share. share_key is the share code; share_pwd is the receive code.

## Configuration

```console
rclone config
```

```text
n/s/q> n
name> remote
Storage> 115share
```

```console
rclone lsf remote:
```

Do not use a third-party token broker.

115 Drive public share. share_key is the share code; share_pwd is the receive code.

### Standard options

#### --115share-share_key

Share key or share id.
#### --115share-share_pwd

Share password, if any.

