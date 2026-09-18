---
title: "Alibaba Cloud OSS"
description: "Rclone docs for Alibaba Cloud OSS (阿里云对象存储)"
versionIntroduced: "v1.76.1"
---

# Alibaba Cloud OSS

[Alibaba Cloud Object Storage Service (OSS)](https://www.alibabacloud.com/product/oss/)
is S3-compatible object storage. This backend is a thin wrapper around
rclone's [S3](/s3/) backend with `provider = Alibaba` already set, so
`rclone config` shows **oss** instead of hunting through the S3
provider list.

You can still use `type = s3` / `provider = Alibaba` — it is the same
implementation. See [S3 / Alibaba OSS](/s3/#alibaba-oss).

## Configuration

Create an AccessKey in the Alibaba Cloud console, then:

```console
rclone config
```

```text
n) New remote
name> oss
Storage> oss
access_key_id> your-access-key-id
secret_access_key> your-access-key-secret
endpoint> oss-cn-hangzhou.aliyuncs.com
```

Or write the config file:

```ini
[oss]
type = oss
access_key_id = your-access-key-id
secret_access_key = your-access-key-secret
endpoint = oss-cn-hangzhou.aliyuncs.com
```

Paths are `remote:bucket` or `remote:bucket/path`.

```console
rclone lsd oss:
rclone ls oss:bucket
rclone copy /home/source oss:bucket/path
```

Pick the endpoint for the bucket's region (Hangzhou, Beijing, Shenzhen,
Hong Kong, accelerate, and so on). The wrapper copies the same region
list as the S3 Alibaba provider.
