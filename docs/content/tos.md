---
title: "Volcengine TOS"
description: "Rclone docs for Volcengine TOS (火山引擎对象存储)"
versionIntroduced: "v1.76.1"
---

# Volcengine TOS

[Volcengine Object Storage (TOS)](https://www.volcengine.com/product/tos)
(火山引擎对象存储) is ByteDance's S3-compatible object storage. This
backend wraps rclone's [S3](/s3/) backend with `provider = Volcengine`.

You can also configure `type = s3` and `provider = Volcengine`. See
[S3 / Volcengine TOS](/s3/#volcengine-tos).

## Configuration

Create an Access Key in the Volcengine console (TOS), then:

```console
rclone config
```

```text
n) New remote
name> tos
Storage> tos
access_key_id> your-access-key-id
secret_access_key> your-secret-access-key
endpoint> tos-s3-cn-beijing.volces.com
```

If `region` is blank, rclone infers it from the endpoint host
(`tos-s3-cn-beijing.volces.com` → `cn-beijing`). SigV4 needs that
region to match the bucket.

```ini
[tos]
type = tos
access_key_id = your-access-key-id
secret_access_key = your-secret-access-key
endpoint = tos-s3-cn-beijing.volces.com
region = cn-beijing
```

Common S3 endpoints:

| Region | Endpoint |
| --- | --- |
| cn-beijing | tos-s3-cn-beijing.volces.com |
| cn-shanghai | tos-s3-cn-shanghai.volces.com |
| cn-guangzhou | tos-s3-cn-guangzhou.volces.com |
| cn-hongkong | tos-s3-cn-hongkong.volces.com |
| ap-southeast-1 | tos-s3-ap-southeast-1.volces.com |

```console
rclone lsd tos:
rclone copy /home/source tos:bucket/path
```
