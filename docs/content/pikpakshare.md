---
title: "PikPak Share"
description: "Rclone docs for PikPak Share"
versionIntroduced: "v1.76.1"
---

# PikPak Share

[PikPak Share](https://mypikpak.com) PikPak public share. share_key is share_id.

## Configuration

```console
rclone config
```

```text
n/s/q> n
name> remote
Storage> pikpakshare
```

```console
rclone lsf remote:
```

Do not use a third-party token broker.

PikPak public share. share_key is share_id.

### Standard options

#### --pikpakshare-share_key

Share key or share id.
#### --pikpakshare-share_pwd

Share password, if any.

