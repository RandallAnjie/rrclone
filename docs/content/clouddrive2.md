---
title: "CloudDrive2"
description: "Rclone docs for CloudDrive2"
versionIntroduced: "v1.76.1"
---

# CloudDrive2

[CloudDrive2](https://www.clouddrive2.com/) is a local (or remote) gRPC
service that mounts many cloud accounts as one tree. This backend
talks to **your** CloudDrive2 instance using the published gRPC API
(`clouddrive.proto`). It does not copy any third-party plugin code.

Create an [API token](https://www.clouddrive2.com/en/help.html) in the
CloudDrive2 UI, or use the account username and password (GetToken).

## Configuration

Default listen address is `localhost:19798`.

```console
rclone config
```

```text
n/s/q> n
name> remote
Storage> clouddrive2
address> localhost:19798
username> YOUR_USER
password> YOUR_PASSWORD
```

Or paste a token:

```text
token> YOUR_API_TOKEN
```

```console
rclone lsf remote:
rclone copy /home/source remote:backup
```

Set `root_folder` to a CloudNAS mount if you only want one cloud, for
example `/CloudNAS/115`.

### Modification times and hashes

CloudDrive2 returns protobuf timestamps with second precision. rclone
cannot set them. MD5 and SHA-1 are used when the server fills
`fileHashes`.

### Standard options

#### --clouddrive2-address

CloudDrive2 gRPC address (`host:port` or an `http(s)` URL).

#### --clouddrive2-username

Username. Optional if token is set.

#### --clouddrive2-password

Password. Optional if token is set.

### Advanced options

#### --clouddrive2-token

JWT or API token. Optional if username and password are set.

#### --clouddrive2-totp

TOTP code for GetToken when 2FA is enabled.

#### --clouddrive2-root_folder

Path inside CloudDrive2 to use as the remote root.

#### --clouddrive2-tls

Use TLS for gRPC. Implied when address starts with `https://`.

#### --clouddrive2-insecure_skip_verify

Skip TLS certificate verification.
