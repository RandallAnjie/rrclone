---
title: "Misskey"
description: "Rclone docs for Misskey"
versionIntroduced: "v1.76.1"
---

# Misskey

[Misskey](https://misskey-hub.net) Misskey drive. Token from the instance.

## Configuration

```console
rclone config
```

```text
n/s/q> n
name> remote
Storage> misskey
```

```console
rclone lsf remote:
```

Do not use a third-party token broker.

Misskey drive. Token from the instance.

### Standard options

#### --misskey-token

Misskey API token.

