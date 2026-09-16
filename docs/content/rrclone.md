---
title: "rrclone"
description: "rrclone 是 rclone 的增强分支：自定义 API、115 网盘、Drive 多账号轮换和状态看板"
---

# rrclone

rrclone 是 [rclone](https://rclone.org) 的增强分支。命令、后端和官方 rclone 兼容，额外补了国内更常用的能力：自定义 API 地址、115 网盘、Google Drive 多账号轮换、一键安装和本机状态看板。

- 仓库：[github.com/RandallAnjie/rrclone](https://github.com/RandallAnjie/rrclone)
- 上游：[rclone/rclone](https://github.com/rclone/rclone)
- 安装：[一键脚本](#安装)
- 看板：[dashboard/README.md](https://github.com/RandallAnjie/rrclone/blob/master/dashboard/README.md)

二进制仍然可以当 `rclone` 用。默认配置在 `~/.config/rrclone/rrclone.conf`，环境变量优先读 `RRCLONE_*`，没有时回退 `RCLONE_*`。

## 相比原版改了什么

| 能力 | 原版 rclone | rrclone |
| --- | --- | --- |
| 70+ 云存储后端、sync / copy / mount / crypt / RC | 有 | 有，持续跟上游 |
| 配置文件 | `~/.config/rclone/rclone.conf` | `~/.config/rrclone/rrclone.conf`（可用 `--migrate` 从官方配置拷过来） |
| 环境变量 | 只有 `RCLONE_*` | 先读 `RRCLONE_*`，没有再读 `RCLONE_*` |
| Google / Dropbox 等官方 API 被墙 | 只能改 OAuth 的 `auth_url` / `token_url`，数据面主机仍写死 | 再加 `endpoint`（以及 Dropbox / Box 的上传主机）走反代 |
| 115 网盘 | 无 | 用浏览器 cookie 走 web API，不用已停的 OpenAPI |
| Google Drive 多 OAuth 账号轮换 | 无 | `--drive-oauth-account-files`，quota / 限流 / `authError` 自动切号 |
| 本机状态看板 | 无独立 UI | `dashboard/` Next.js，读官方 RC |
| 一键安装 | `rclone.org/install.sh` | 本仓库 `install.sh`，默认装 `rrclone` 并可选替换 `rclone` 命令 |

上游能 merge 的代码尽量不改命令注册和核心接口。看板和安装脚本放在独立目录，方便继续跟官方。

## 特性

继承 rclone 的全部能力：

- 在本地和云、云和云之间 copy / sync / move / check
- 校验 checksum，尽量保留修改时间
- [mount](/commands/rclone_mount/) 成磁盘
- [crypt](/crypt/) / [chunker](/chunker/) / [compress](/compress/) / [union](/union/) 等虚拟后端
- RC API、`rclone rcd`、过滤、带宽限制、服务端 copy

本分支额外：

- **自定义 API**：官方域名不通时，把请求打到你自己的反代，见下面 [URL 怎么换](#自定义-apiurl-怎么换)
- **115 Drive**：浏览器 cookie 登录，列表 / 上传 / 下载 / 秒传，见 [115](/115/)
- **Drive 多账号**：多个 OAuth token 文件轮换，适合大量上传和挂载，见 [Drive OAuth account rotation](/drive/#oauth-account-rotation)
- **看板**：传输、远程、任务、挂载、多主机 RC 地址

## 后端

原版支持的后端这里都有（Drive、Dropbox、S3、OneDrive、WebDAV、本地磁盘等 70+）。完整列表见 [文档](/docs/) 和仓库 README。

本分支多出来的：

| 后端 | 说明 |
| --- | --- |
| [115 Drive](/115/) | 115 网盘，cookie 认证，web API |

下面这些后端可以把**官方 API 主机**换成反代（空着就是官方地址）：

| 后端 | 配置项 | 默认官方地址 |
| --- | --- | --- |
| Google Drive | `auth_url` + `token_url` + `endpoint` | 见下一节，**三个都要换** |
| Google Photos | `auth_url` + `token_url` + `endpoint` | Photos 库 API |
| Google Cloud Storage | `auth_url` + `token_url` + `endpoint` | 原版就有 `endpoint` |
| Dropbox | `auth_url` + `token_url` + `endpoint` + `content_endpoint` | API 和上传/下载是两台主机 |
| Box | `auth_url` + `token_url` + `endpoint` + `upload_endpoint` | API 和上传是两台主机 |
| OneDrive | `endpoint`，或国内世纪互联用 `region = cn` | Graph；OAuth 另有 `auth_url` / `token_url` |
| pCloud | `hostname` | 原版就有欧洲站 `eapi.pcloud.com` |

S3 兼容存储本来就用 `endpoint`（Cloudflare R2、MinIO、阿里云 OSS 等），不用再改。

## 自定义 API：URL 怎么换

只改 `client_id` 没用。国内访问失败，是因为 rclone 还在连官方域名。OAuth 和数据面往往还不是同一个域名。

反代要把官方路径原样转发出去（Host 指到官方，或按官方路径反代）。不要只换根域名却丢掉 `/drive/v3/` 这类路径。

### Google Drive：换三个 URL

Drive 会打三个官方主机，三个都要换成你的反代：

| # | 干什么 | 官方 URL | 配置项 / 命令行 |
| --- | --- | --- | --- |
| 1 | 浏览器授权 | `https://accounts.google.com/o/oauth2/auth` | `auth_url` / `--drive-auth-url` |
| 2 | 换 token、刷新 token、服务账号 JWT | `https://oauth2.googleapis.com/token` | `token_url` / `--drive-token-url` |
| 3 | Drive JSON API **和** 分片上传 | `https://www.googleapis.com`（含 `/drive/v3/` 和 `/upload/drive/v3/`） | `endpoint` / `--drive-endpoint` |

只换第 3 个，登录和刷新 token 仍会连 Google，国内一样失败。只换 1 和 2，列目录和上传仍走 `www.googleapis.com`。

交互登录要换 **1 + 2 + 3**。服务账号没有浏览器授权，换 **2 + 3** 即可。

```ini
[gdrive]
type = drive
client_id = YOUR_CLIENT_ID
client_secret = YOUR_CLIENT_SECRET
token = {"access_token":"..."}
auth_url = https://accounts.example.com/o/oauth2/auth
token_url = https://oauth2.example.com/token
endpoint = https://googleapis.example.com
```

`endpoint` 填反代的 origin（`https://googleapis.example.com`），不必把 `/drive/v3` 写进配置。rclone 会在这台主机上请求：

- `GET/POST /drive/v3/...` 列表、元数据、下载
- `POST /upload/drive/v3/files` 可恢复上传

如果 Google 返回的 `Location` 仍是 `www.googleapis.com`，rrclone 会改写到你的 `endpoint`。导出链接、部分媒体可能还走 `drive.google.com` / `googleusercontent.com`，这两个不在上述三个里面，反代覆盖不了。

### Dropbox

| 干什么 | 官方 URL | 配置项 |
| --- | --- | --- |
| 授权 | `https://www.dropbox.com/oauth2/authorize` | `auth_url` |
| 换 token | `https://api.dropboxapi.com/oauth2/token` | `token_url` |
| API | `https://api.dropboxapi.com` | `endpoint` |
| 上传/下载 | `https://content.dropboxapi.com` | `content_endpoint` |

`endpoint` 若是 `api.` 开头的主机、且没填 `content_endpoint`，会自动改成对应的 `content.` / `notify.`。

```ini
[dropbox]
type = dropbox
token = {"access_token":"..."}
auth_url = https://www.dropbox.example.com/oauth2/authorize
token_url = https://api.dropbox.example.com/oauth2/token
endpoint = https://api.dropbox.example.com
content_endpoint = https://content.dropbox.example.com
```

### Google Photos

和 Drive 一样要换授权、token、API 三个主机。`endpoint` 默认对应 `https://photoslibrary.googleapis.com/v1`。原图像素有时仍从 `googleusercontent.com` 拉，那个不受 `endpoint` 控制。

### Box

| 干什么 | 官方 URL | 配置项 |
| --- | --- | --- |
| 授权 / token | `https://app.box.com/api/oauth2/...` | `auth_url` / `token_url` |
| API | `https://api.box.com/2.0` | `endpoint` |
| 上传 | `https://upload.box.com/api/2.0` | `upload_endpoint` |

### OneDrive

国际版 Graph 被墙时设 `endpoint` 反代 `https://graph.microsoft.com`，OAuth 另设 `auth_url` / `token_url`（`login.microsoftonline.com`）。

国内世纪互联（Vnet）不要套国际 Graph 反代，直接：

```ini
region = cn
```

这会走 `https://microsoftgraph.chinacloudapi.cn` 和 `https://login.chinacloudapi.cn`。

### 已经自带 endpoint 的后端

- **S3 兼容**：`--s3-endpoint`
- **GCS**：`--gcs-endpoint`
- **pCloud**：`hostname`（欧洲站 `eapi.pcloud.com`）
- **B2 / Azure / 其它对象存储**：各自文档里的 endpoint

## 安装

Linux / macOS / BSD：

```console
sudo -v ; curl -fsSL https://raw.githubusercontent.com/RandallAnjie/rrclone/master/install.sh | sudo bash
```

从官方 rclone 把配置拷过来（不覆盖已有 rrclone 配置）：

```console
sudo -v ; curl -fsSL https://raw.githubusercontent.com/RandallAnjie/rrclone/master/install.sh | sudo bash -s -- --migrate
```

只装 `rrclone`、不动现在的 `rclone` 命令：

```console
sudo -v ; curl -fsSL https://raw.githubusercontent.com/RandallAnjie/rrclone/master/install.sh | sudo bash -s -- --no-replace
```

### 走代理更新

`sudo bash` 不会继承当前 shell 的 `https_proxy`。本机代理用 `-E` 或 `RRCLONE_PROXY`；国内 GitHub 拉不下来时用镜像前缀（脚本地址也要包一层）：

```console
# 本机 HTTP 代理（Clash 常见 7890）
export https_proxy=http://127.0.0.1:7890 http_proxy=http://127.0.0.1:7890
curl -fsSL https://raw.githubusercontent.com/RandallAnjie/rrclone/master/install.sh | sudo -E bash

# 或只把代理传给脚本
curl -fsSL https://raw.githubusercontent.com/RandallAnjie/rrclone/master/install.sh \
  | sudo RRCLONE_PROXY=http://127.0.0.1:7890 bash

# GitHub 镜像（把 ghfast.top 换成你能用的前缀）
GH=https://ghfast.top
curl -fsSL ${GH}/https://raw.githubusercontent.com/RandallAnjie/rrclone/master/install.sh \
  | sudo RRCLONE_GHPROXY=${GH} bash
```

Windows 从 [Releases](https://github.com/RandallAnjie/rrclone/releases) 下载 zip。

## 看板

先开 RC，再开 `dashboard/`：

```console
rclone rcd --rc-addr 127.0.0.1:5572 --rc-no-auth
cd dashboard && npm install && npm run dev
```

浏览器打开 <http://localhost:3000>。说明见 [dashboard/README.md](https://github.com/RandallAnjie/rrclone/blob/master/dashboard/README.md)。

## 相关文档

- [Google Drive](/drive/)（含自定义 API 和 OAuth 轮换）
- [Dropbox](/dropbox/)
- [Google Photos](/googlephotos/)
- [Box](/box/)
- [OneDrive](/onedrive/)
- [115 Drive](/115/)
- 上游用法：[rclone.org/docs](https://rclone.org/docs/)
