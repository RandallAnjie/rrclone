---
title: "GitHub Releases"
description: "Rclone docs for GitHub Releases"
versionIntroduced: "v1.76.1"
---

# GitHub Releases

[GitHub Releases](https://docs.github.com/rest/releases/releases) GitHub Releases assets.

## Configuration

```console
rclone config
```

```text
n/s/q> n
name> remote
Storage> githubreleases
```

```console
rclone lsf remote:
```

Do not use a third-party token broker.

GitHub Releases assets.

### Standard options

#### --githubreleases-token

GitHub Releases API token.
#### --githubreleases-owner

Repository owner.
#### --githubreleases-repo

Repository name.

