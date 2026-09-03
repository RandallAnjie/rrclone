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

- **概览**：速度折线、传输/字节/远程饼图、操作柱状图、内存曲线、活动传输
- **传输**：实时速度、完成结果饼图、文件体积对比、活动/完成列表
- **远程**：类型分布、用量柱状图、`config/dump`（敏感字段打码）+ `operations/about`
- **任务**：状态饼图、耗时对比、`job/list` / `job/status`，可停止运行中的任务
- **挂载**：`mount/listmounts` + VFS 缓存饼图
- **主机**：本机默认条目 + 后续多机器的 RC 地址

## UI

界面用 [HeroUI Pro](https://heroui.pro)（`@heroui-pro/react`）加上 HeroUI OSS v3（`@heroui/react` + `@heroui/styles`）。壳是 Pro 的 `AppLayout` / `Sidebar` / `Navbar`，概览用 Pro `KPI` / `Widget`。

`npm install` 需要 [HeroUI 控制台](https://heroui.pro/dashboard) 里的 **CI/CD license key**，写成环境变量 `HEROUI_AUTH_TOKEN`。这不是 MCP 用的 personal token。参考 [CI/CD 安装说明](https://heroui.pro/docs/react/getting-started/installation#cicd)。

```bash
export HEROUI_AUTH_TOKEN=your-cicd-token
cd dashboard
npm install
npm run dev
```

本地用 `http://127.0.0.1:3000` 访问时，`next.config.ts` 已配置 `allowedDevOrigins`，避免 Next.js 16 把本机开发资源当成跨域拦掉。

## 开发

```bash
npm test
npm run lint
npm run build
```
