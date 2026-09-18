<!-- markdownlint-disable-next-line first-line-heading no-inline-html -->
[<img src="https://rclone.org/img/logo_on_light__horizontal_color.svg" width="50%" alt="rclone logo">](https://rclone.org/#gh-light-mode-only)
<!-- markdownlint-disable-next-line no-inline-html -->
[<img src="https://rclone.org/img/logo_on_dark__horizontal_color.svg" width="50%" alt="rclone logo">](https://rclone.org/#gh-dark-mode-only)

[Website](https://rclone.org) |
[Documentation](https://rclone.org/docs/) |
[Download](https://rclone.org/downloads/) |
[Contributing](CONTRIBUTING.md) |
[Changelog](https://rclone.org/changelog/) |
[Installation](https://rclone.org/install/) |
[Forum](https://forum.rclone.org/)

[![Build Status](https://github.com/rclone/rclone/workflows/build/badge.svg)](https://github.com/rclone/rclone/actions?query=workflow%3Abuild)
[![Go Report Card](https://goreportcard.com/badge/github.com/rclone/rclone)](https://goreportcard.com/report/github.com/rclone/rclone)
[![GoDoc](https://godoc.org/github.com/rclone/rclone?status.svg)](https://godoc.org/github.com/rclone/rclone)
[![Docker Pulls](https://img.shields.io/docker/pulls/rclone/rclone)](https://hub.docker.com/r/rclone/rclone)

# Rclone

Rclone *("rsync for cloud storage")* 是一个用于在各种云存储之间同步文件和目录的命令行工具。

**rrclone** 是本仓库相对官方 rclone 的增强分支。和原版比改了什么、有哪些后端和特性、Google Drive 要换哪三个 URL，见落地页：

**[docs/content/rrclone.md](docs/content/rrclone.md)**

## 这个分支相比原版 rclone 更新了什么

详细对照表和配置示例在落地页。摘要：

- **自定义 API**：Drive / Dropbox / Photos / Box / OneDrive 可把官方 API 主机换成反代。Drive 要换三个地址（授权、token、API）
- **115 网盘**：浏览器 cookie + web API
- **文件直链**：`rclone link` 对 115、Google Drive、Dropbox、OneDrive、S3 给出可 wget 的下载地址
- **Google Drive 多 OAuth 账号轮换**：`--drive-oauth-account-files`
- **rclone 状态看板**：独立的 `dashboard/` Next.js 应用
- **一键安装**、默认配置 `~/.config/rrclone/rrclone.conf`

Google Drive 多账号轮换的细节仍在 Drive 文档的 **“OAuth account rotation”** 章节。看板启动方式见 [dashboard/README.md](dashboard/README.md)。

## 安装（从官方 rclone 一键切到 rrclone）

和官方 `https://rclone.org/install.sh` 一样，Linux / macOS / BSD 可以用一键脚本。默认会安装 `rrclone`，并替换 `rclone` 命令（先把原来的二进制备份成 `rclone.official.bak`）。

```console
sudo -v ; curl -fsSL https://raw.githubusercontent.com/RandallAnjie/rrclone/master/install.sh | sudo bash
```

已经在用官方 rclone、要把配置一起迁过来时加上 `--migrate`。这会把 `~/.config/rclone/rclone.conf` 复制到 `~/.config/rrclone/rrclone.conf`（目标文件不存在时才复制，不会覆盖已有 rrclone 配置）：

```console
sudo -v ; curl -fsSL https://raw.githubusercontent.com/RandallAnjie/rrclone/master/install.sh | sudo bash -s -- --migrate
```

只装 `rrclone`、不动现在的 `rclone` 命令：

```console
sudo -v ; curl -fsSL https://raw.githubusercontent.com/RandallAnjie/rrclone/master/install.sh | sudo bash -s -- --no-replace
```

发布包也可以从 GitHub Releases 下载（`rrclone-<os>-<arch>.zip`）。打 `v*` 标签或在 Actions 里手动跑 `release` workflow 就会生成这些 zip。在还没有 Release 的机器上，脚本会回退到本机 `go build`（需要已安装 Go）。

Windows 没有 bash 一键脚本，请从 [Releases](https://github.com/RandallAnjie/rrclone/releases) 下载 `rrclone-windows-amd64.zip`。

默认配置文件是 `~/.config/rrclone/rrclone.conf`，不是官方的 `~/.config/rclone/rclone.conf`。环境变量优先读 `RRCLONE_*`，没有时回退 `RCLONE_*`。

## Storage providers

- 115 Drive [:page_facing_up:](https://rclone.org/115/)
- 1Fichier [:page_facing_up:](https://rclone.org/fichier/)
- Akamai Netstorage [:page_facing_up:](https://rclone.org/netstorage/)
- Alibaba Cloud (Aliyun) Object Storage System (OSS) [:page_facing_up:](https://rclone.org/s3/#alibaba-oss)
- Amazon S3 [:page_facing_up:](https://rclone.org/s3/)
- ArvanCloud Object Storage (AOS) [:page_facing_up:](https://rclone.org/s3/#arvan-cloud-object-storage-aos)
- Bizfly Cloud Simple Storage [:page_facing_up:](https://rclone.org/s3/#bizflycloud)
- Backblaze B2 [:page_facing_up:](https://rclone.org/b2/)
- Box [:page_facing_up:](https://rclone.org/box/)
- Ceph [:page_facing_up:](https://rclone.org/s3/#ceph)
- China Mobile Ecloud Elastic Object Storage (EOS) [:page_facing_up:](https://rclone.org/s3/#china-mobile-ecloud-eos)
- Citrix ShareFile [:page_facing_up:](https://rclone.org/sharefile/)
- Cloudflare R2 [:page_facing_up:](https://rclone.org/s3/#cloudflare-r2)
- Cloudinary [:page_facing_up:](https://rclone.org/cloudinary/)
- Cubbit DS3 [:page_facing_up:](https://rclone.org/s3/#Cubbit)
- DigitalOcean Spaces [:page_facing_up:](https://rclone.org/s3/#digitalocean-spaces)
- Digi Storage [:page_facing_up:](https://rclone.org/koofr/#digi-storage)
- Dreamhost [:page_facing_up:](https://rclone.org/s3/#dreamhost)
- Drime [:page_facing_up:](https://rclone.org/s3/#drime)
- Dropbox [:page_facing_up:](https://rclone.org/dropbox/)
- Enterprise File Fabric [:page_facing_up:](https://rclone.org/filefabric/)
- Exaba [:page_facing_up:](https://rclone.org/s3/#exaba)
- Fastly Object Storage [:page_facing_up:](https://rclone.org/s3/#fastly)
- Fastmail Files [:page_facing_up:](https://rclone.org/webdav/#fastmail-files)
- FileLu [:page_facing_up:](https://rclone.org/filelu/)
- Filen [:page_facing_up:](https://rclone.org/filen/)
- Files.com [:page_facing_up:](https://rclone.org/filescom/)
- FlashBlade [:page_facing_up:](https://rclone.org/s3/#pure-storage-flashblade)
- FTP [:page_facing_up:](https://rclone.org/ftp/)
- GoFile [:page_facing_up:](https://rclone.org/gofile/)
- Google Cloud Storage [:page_facing_up:](https://rclone.org/googlecloudstorage/)
- Google Drive [:page_facing_up:](https://rclone.org/drive/)
- Google Photos [:page_facing_up:](https://rclone.org/googlephotos/)
- HDFS (Hadoop Distributed Filesystem) [:page_facing_up:](https://rclone.org/hdfs/)
- Hetzner Object Storage [:page_facing_up:](https://rclone.org/s3/#hetzner)
- Hetzner Storage Box [:page_facing_up:](https://rclone.org/sftp/#hetzner-storage-box)
- HiDrive [:page_facing_up:](https://rclone.org/hidrive/)
- Hitachi Content Platform (HCP) [:page_facing_up:](https://rclone.org/s3/#hcp)
- HTTP [:page_facing_up:](https://rclone.org/http/)
- Huawei Cloud Object Storage Service(OBS) [:page_facing_up:](https://rclone.org/s3/#huawei-obs)
- Huawei Drive [:page_facing_up:](https://rclone.org/huaweidrive/)
- iCloud Drive [:page_facing_up:](https://rclone.org/iclouddrive/)
- ImageKit [:page_facing_up:](https://rclone.org/imagekit/)
- Internet Archive [:page_facing_up:](https://rclone.org/internetarchive/)
- Internxt [:page_facing_up:](https://rclone.org/internxt/)
- Jottacloud [:page_facing_up:](https://rclone.org/jottacloud/)
- IBM COS S3 [:page_facing_up:](https://rclone.org/s3/#ibm-cos-s3)
- Impossible Cloud [:page_facing_up:](https://rclone.org/s3/#impossible-cloud)
- Intercolo Object Storage [:page_facing_up:](https://rclone.org/s3/#intercolo)
- IONOS Cloud [:page_facing_up:](https://rclone.org/s3/#ionos)
- Koofr [:page_facing_up:](https://rclone.org/koofr/)
- Leviia Object Storage [:page_facing_up:](https://rclone.org/s3/#leviia)
- Liara Object Storage [:page_facing_up:](https://rclone.org/s3/#liara-object-storage)
- Linkbox [:page_facing_up:](https://rclone.org/linkbox)
- Linode Object Storage [:page_facing_up:](https://rclone.org/s3/#linode)
- Magalu Object Storage [:page_facing_up:](https://rclone.org/s3/#magalu)
- Mail.ru Cloud [:page_facing_up:](https://rclone.org/mailru/)
- Memset Memstore [:page_facing_up:](https://rclone.org/swift/)
- MEGA [:page_facing_up:](https://rclone.org/mega/)
- MEGA S4 Object Storage [:page_facing_up:](https://rclone.org/s3/#mega)
- Memory [:page_facing_up:](https://rclone.org/memory/)
- Microsoft Azure Blob Storage [:page_facing_up:](https://rclone.org/azureblob/)
- Microsoft Azure Files Storage [:page_facing_up:](https://rclone.org/azurefiles/)
- Microsoft OneDrive [:page_facing_up:](https://rclone.org/onedrive/)
- Minio [:page_facing_up:](https://rclone.org/s3/#minio)
- Nextcloud [:page_facing_up:](https://rclone.org/webdav/#nextcloud)
- Blomp Cloud Storage [:page_facing_up:](https://rclone.org/swift/)
- OpenDrive [:page_facing_up:](https://rclone.org/opendrive/)
- OpenStack Swift [:page_facing_up:](https://rclone.org/swift/)
- Oracle Cloud Storage [:page_facing_up:](https://rclone.org/swift/)
- Oracle Object Storage [:page_facing_up:](https://rclone.org/oracleobjectstorage/)
- Outscale [:page_facing_up:](https://rclone.org/s3/#outscale)
- OVHcloud Object Storage (Swift) [:page_facing_up:](https://rclone.org/swift/)
- OVHcloud Object Storage (S3-compatible) [:page_facing_up:](https://rclone.org/s3/#ovhcloud)
- ownCloud [:page_facing_up:](https://rclone.org/webdav/#owncloud)
- pCloud [:page_facing_up:](https://rclone.org/pcloud/)
- Petabox [:page_facing_up:](https://rclone.org/s3/#petabox)
- PikPak [:page_facing_up:](https://rclone.org/pikpak/)
- Pixeldrain [:page_facing_up:](https://rclone.org/pixeldrain/)
- premiumize.me [:page_facing_up:](https://rclone.org/premiumizeme/)
- put.io [:page_facing_up:](https://rclone.org/putio/)
- Proton Drive [:page_facing_up:](https://rclone.org/protondrive/)
- QingStor [:page_facing_up:](https://rclone.org/qingstor/)
- Qiniu Cloud Object Storage (Kodo) [:page_facing_up:](https://rclone.org/s3/#qiniu)
- Rabata Cloud Storage [:page_facing_up:](https://rclone.org/s3/#Rabata)
- Quatrix [:page_facing_up:](https://rclone.org/quatrix/)
- Rackspace Cloud Files [:page_facing_up:](https://rclone.org/swift/)
- RackCorp Object Storage [:page_facing_up:](https://rclone.org/s3/#RackCorp)
- rsync.net [:page_facing_up:](https://rclone.org/sftp/#rsync-net)
- Scaleway [:page_facing_up:](https://rclone.org/s3/#scaleway)
- Scality (RING / ARTESCA) [:page_facing_up:](https://rclone.org/s3/#scality)
- Seafile [:page_facing_up:](https://rclone.org/seafile/)
- Seagate Lyve Cloud [:page_facing_up:](https://rclone.org/s3/#lyve)
- SeaweedFS [:page_facing_up:](https://rclone.org/s3/#seaweedfs)
- Selectel Object Storage [:page_facing_up:](https://rclone.org/s3/#selectel)
- Servercore Object Storage [:page_facing_up:](https://rclone.org/s3/#servercore)
- SFTP [:page_facing_up:](https://rclone.org/sftp/)
- Shade [:page_facing_up:](https://rclone.org/shade/)
- SMB / CIFS [:page_facing_up:](https://rclone.org/smb/)
- Spectra Logic [:page_facing_up:](https://rclone.org/s3/#spectralogic)
- Storj [:page_facing_up:](https://rclone.org/storj/)
- SugarSync [:page_facing_up:](https://rclone.org/sugarsync/)
- Synology C2 Object Storage [:page_facing_up:](https://rclone.org/s3/#synology-c2)
- Tencent Cloud Object Storage (COS) [:page_facing_up:](https://rclone.org/s3/#tencent-cos)
- Uloz.to [:page_facing_up:](https://rclone.org/ulozto/)
- US3 Object Storage [:page_facing_up:](https://rclone.org/s3/#us3)
- Wasabi [:page_facing_up:](https://rclone.org/s3/#wasabi)
- WebDAV [:page_facing_up:](https://rclone.org/webdav/)
- Yandex Disk [:page_facing_up:](https://rclone.org/yandex/)
- Zadara Object Storage [:page_facing_up:](https://rclone.org/s3/#zadara)
- Zero Services (ZERO-Z3) [:page_facing_up:](https://rclone.org/s3/#zero-z3)
- Zoho WorkDrive [:page_facing_up:](https://rclone.org/zoho/)
- Zata.ai [:page_facing_up:](https://rclone.org/s3/#Zata)
- The local filesystem [:page_facing_up:](https://rclone.org/local/)

Please see [the full list of all storage providers and their features](https://rclone.org/overview/)

### Virtual storage providers

These backends adapt or modify other storage providers

- Alias: rename existing remotes [:page_facing_up:](https://rclone.org/alias/)
- Archive: read archive files [:page_facing_up:](https://rclone.org/archive/)
- Cache: cache remotes (DEPRECATED) [:page_facing_up:](https://rclone.org/cache/)
- Chunker: split large files [:page_facing_up:](https://rclone.org/chunker/)
- Combine: combine multiple remotes into a directory tree [:page_facing_up:](https://rclone.org/combine/)
- Compress: compress files [:page_facing_up:](https://rclone.org/compress/)
- Crypt: encrypt files [:page_facing_up:](https://rclone.org/crypt/)
- Hasher: hash files [:page_facing_up:](https://rclone.org/hasher/)
- Union: join multiple remotes to work together [:page_facing_up:](https://rclone.org/union/)

## Features

- MD5/SHA-1 hashes checked at all times for file integrity
- Timestamps preserved on files
- Partial syncs supported on a whole file basis
- [Copy](https://rclone.org/commands/rclone_copy/) mode to just copy new/changed
  files
- [Sync](https://rclone.org/commands/rclone_sync/) (one way) mode to make a directory
  identical
- [Bisync](https://rclone.org/bisync/) (two way) to keep two directories in sync
  bidirectionally
- [Check](https://rclone.org/commands/rclone_check/) mode to check for file hash
  equality
- Can sync to and from network, e.g. two different cloud accounts
- Optional large file chunking ([Chunker](https://rclone.org/chunker/))
- Optional transparent compression ([Compress](https://rclone.org/compress/))
- Optional encryption ([Crypt](https://rclone.org/crypt/))
- Optional FUSE mount ([rclone mount](https://rclone.org/commands/rclone_mount/))
- Multi-threaded downloads to local disk
- Can [serve](https://rclone.org/commands/rclone_serve/) local or remote files
  over HTTP/WebDAV/FTP/SFTP/DLNA

## Installation & documentation

Please see the [rclone website](https://rclone.org/) for:

- [Installation](https://rclone.org/install/)
- [Documentation & configuration](https://rclone.org/docs/)
- [Changelog](https://rclone.org/changelog/)
- [FAQ](https://rclone.org/faq/)
- [Storage providers](https://rclone.org/overview/)
- [Forum](https://forum.rclone.org/)
- ...and more

Google Drive users who rotate between multiple OAuth accounts can find setup
and logging details in the Drive docs under “OAuth account rotation”.

## Downloads

- <https://rclone.org/downloads/>

## License

This is free software under the terms of the MIT license (check the
[COPYING file](/COPYING) included in this package).
