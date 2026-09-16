---
title: "Upyun USS"
description: "Rclone docs for Upyun USS"
versionIntroduced: "v1.76.1"
---

# Upyun USS

[Upyun USS](https://www.upyun.com) Upyun USS. Username is the operator name; password is the operator password.

## Configuration

```console
rclone config
```

```text
n/s/q> n
name> remote
Storage> upyun
```

```console
rclone lsf remote:
```

Do not use a third-party token broker.

Upyun USS. Username is the operator name; password is the operator password.

### Standard options

#### --upyun-username

Account username.
#### --upyun-password

Account password.
#### --upyun-bucket

USS bucket name.

