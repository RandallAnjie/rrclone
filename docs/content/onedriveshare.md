---
title: "OneDrive Share"
description: "Rclone docs for OneDrive Share"
versionIntroduced: "v1.76.1"
---

# OneDrive Share

[OneDrive Share](https://www.microsoft.com/microsoft-365/onedrive) OneDrive share link. share_key is the share URL or id.

## Configuration

```console
rclone config
```

```text
n/s/q> n
name> remote
Storage> onedriveshare
```

```console
rclone lsf remote:
```

Do not use a third-party token broker.

OneDrive share link. share_key is the share URL or id.

### Standard options

#### --onedriveshare-share_key

Share key or share id.
#### --onedriveshare-share_pwd

Share password, if any.

