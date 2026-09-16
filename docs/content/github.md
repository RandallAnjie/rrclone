---
title: "GitHub"
description: "Rclone docs for GitHub"
versionIntroduced: "v1.76.1"
---

# GitHub

[GitHub](https://docs.github.com/rest/repos/contents) GitHub repository contents. Token, owner and repo are required.

## Configuration

```console
rclone config
```

```text
n/s/q> n
name> remote
Storage> github
```

```console
rclone lsf remote:
```

Do not use a third-party token broker.

GitHub repository contents. Token, owner and repo are required.

### Standard options

#### --github-token

GitHub API token.
#### --github-owner

Repository owner.
#### --github-repo

Repository name.

