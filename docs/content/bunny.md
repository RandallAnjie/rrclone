---
title: "Bunny Storage"
description: "Rclone docs for Bunny Storage"
versionIntroduced: "v1.76.1"
---

# Bunny Storage

[Bunny Storage](https://docs.bunny.net/docs/storage-api) Bunny Storage. Token is the storage zone AccessKey.

## Configuration

```console
rclone config
```

```text
n/s/q> n
name> remote
Storage> bunny
```

```console
rclone lsf remote:
```

Do not use a third-party token broker.

Bunny Storage. Token is the storage zone AccessKey.

### Standard options

#### --bunny-token

Bunny Storage API token.

