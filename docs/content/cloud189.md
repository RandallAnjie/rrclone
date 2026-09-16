---
title: "China Telecom Cloud 189"
description: "Rclone docs for China Telecom Cloud 189"
versionIntroduced: "v1.76.1"
---

# China Telecom Cloud 189

[Cloud 189](https://cloud.189.cn) (天翼云盘) is China Telecom's
consumer cloud storage. This backend uses the **Cloud 189 web API**
(`cloud.189.cn`).

The easy path is **username and password**. rclone logs in through
`open.e.189.cn` (RSA-encrypted password, same flow as the website)
and stores the session cookie.

You can still paste a Cookie header from a logged-in browser session
if you prefer, or if the account requires a captcha.

## Configuration

```console
rclone config
```

```text
n/s/q> n
name> remote
Storage> cloud189
user> YOUR_PHONE_OR_EMAIL
pass> YOUR_PASSWORD
```

```console
rclone lsf remote:
```

If login starts returning errors, rclone will try user/pass again
when they are set. Otherwise copy a fresh cookie string.

### Modification times and hashes

Cloud 189 returns `lastOpTime` in China Standard Time. rclone cannot
set modification times. Hashes are not exposed by this API.

### Standard options

#### --cloud189-user

Cloud 189 username (phone number or email).

#### --cloud189-pass

Cloud 189 password.

#### --cloud189-cookie

Cloud 189 web cookie string. Optional if user and pass are set.

### Advanced options

#### --cloud189-root_folder_id

ID of the root folder. Leave blank to use the account root (`-11`).

#### --cloud189-endpoint

Endpoint for the Cloud 189 web API. Default `https://cloud.189.cn`.

#### --cloud189-auth_endpoint

Endpoint for Cloud 189 SSO login. Default `https://open.e.189.cn`.
