---
title: "OpenList Share"
description: "Rclone docs for OpenList Share"
versionIntroduced: "v1.76.1"
---

# OpenList Share

[OpenList Share](https://github.com/OpenListTeam/OpenList) Public share on your own AList/OpenList server.

## Configuration

```console
rclone config
```

```text
n/s/q> n
name> remote
Storage> openlistshare
```

```console
rclone lsf remote:
```

Do not use a third-party token broker.

Public share on your own AList/OpenList server.

### Standard options

#### --openlistshare-share_key

Share key or share id.
#### --openlistshare-share_pwd

Share password, if any.

