---
title: "AList / OpenList"
description: "Rclone docs for AList and OpenList v3"
versionIntroduced: "v1.76.1"
---

# AList / OpenList

This backend talks to **your own** [AList](https://alist.nn.ci) or
[OpenList](https://github.com/OpenListTeam/OpenList) server over the
public v3 HTTP API. Use it to mount a self-hosted index; do not point
it at a third-party token broker.

## Configuration

```console
rclone config
```

```text
n/s/q> n
name> remote
Storage> openlist
url> https://alist.example.com
username> admin
password> YOUR_PASSWORD
```

You can paste an API token instead of username and password.

```console
rclone lsf remote:
rclone copy /home/source remote:backup
```

### Modification times and hashes

AList returns RFC3339 modification times. MD5 is used when the
upstream storage provides `hash_info`.

### Standard options

#### --openlist-url

URL of the AList or OpenList server.

#### --openlist-username

Username. Optional if token is set.

#### --openlist-password

Password. Optional if token is set.

### Advanced options

#### --openlist-token

API token. Optional if username and password are set.
