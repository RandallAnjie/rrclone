---
title: "IPFS"
description: "Rclone docs for IPFS"
versionIntroduced: "v1.76.1"
---

# IPFS

[IPFS](https://docs.ipfs.tech) IPFS HTTP API.

## Configuration

```console
rclone config
```

```text
n/s/q> n
name> remote
Storage> ipfs
```

```console
rclone lsf remote:
```

Do not use a third-party token broker.

IPFS HTTP API.

### Standard options

#### --ipfs-token

IPFS API token.

