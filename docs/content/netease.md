---
title: "Netease Music"
description: "Rclone docs for Netease Music"
versionIntroduced: "v1.76.1"
---

# Netease Music

[Netease Music](https://music.163.com) NetEase Cloud Music cloud disk. Cookie from music.163.com.

## Configuration

```console
rclone config
```

```text
n/s/q> n
name> remote
Storage> netease
```

```console
rclone lsf remote:
```

Do not use a third-party token broker.

NetEase Cloud Music cloud disk. Cookie from music.163.com.

### Standard options

#### --netease-cookie

Netease Music cookie string.

