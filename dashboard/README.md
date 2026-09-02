# rrclone dashboard

本机 rclone 状态看板。它活在仓库的 `dashboard/` 里，不改官方 rclone 的 Go 命令树，所以以后官方更新可以继续按原来的方式 merge / PR 进来。

当前只接 **一台本机** 的 rclone RC。主机页已经按「多机器」建模：每台机器就是一个 RC 地址，后面加远程节点不用改协议。

## 为什么放在这个仓库

- 直接基于 rrclone / rclone 源码分支，不另起一个无关仓库
- 看板只新增 `dashboard/`，不碰 `cmd/all/all.go`、`rclone.go` 这些官方会频繁改的文件
- 上游 rclone 更新继续 `git fetch upstream master && git merge upstream/master`

看板通过官方 [Remote Control API](https://rclone.org/rc/) 读状态，不重新实现 rclone。

## 启动

先在本机打开 rclone RC：

```bash
./rclone rcd --rc-addr 127.0.0.1:5572 --rc-no-auth
```

有认证时改成：

```bash
./rclone rcd --rc-addr 127.0.0.1:5572 --rc-user gui --rc-pass secret
```

然后另开终端：

```bash
cd dashboard
npm install
npm run dev
```

浏览器打开 <http://localhost:3000>。默认主机是 `http://127.0.0.1:5572`。如果 RC 开了用户名密码，到「主机」页编辑本机条目。

## 页面

- **概览**：速度、进行中传输、错误、远程 / 任务 / 挂载、进程和内存
- **传输**：活动传输 + `core/transferred` 最近完成记录
- **远程**：`config/dump`，敏感字段打码
- **任务**：`job/list` / `job/status`，可停止运行中的任务
- **挂载**：`mount/listmounts`
- **主机**：本机默认条目 + 后续多机器的 RC 地址

## UI

界面用 [HeroUI v3](https://heroui.com)（`@heroui/react` + `@heroui/styles`）。HeroUI Pro 的组件包需要有效的 `HEROUI_AUTH_TOKEN`（CI/CD license key，不是 personal token）才能在安装时拉下 Pro artifact。环境里的 personal token 无法通过 Pro 安装，所以看板按 Pro 的 AppLayout / Sidebar / KPI / Widget 结构用 OSS 组件搭好了。拿到 CI/CD token 之后可以再换成 `@heroui-pro/react` 的现成壳。

## 开发

```bash
npm test
npm run lint
npm run build
```
