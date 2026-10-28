# MiMac 本地工作台

开发入口，集中查看品牌、执行中的设计 token、组件布局样本与架构流程。工作台不进入 App 包，也不读取用户配置、麦克风或私人文档仓。

从项目根目录运行：

```sh
./script/workbench.sh
```

打开终端显示的 `http://127.0.0.1:4173`。`Ctrl+C` 结束服务。需要 Node.js 20+ 和项目 Go 工具链；可使用 `MIMAC_GO=/path/to/go`、`MIMAC_WORKBENCH_PORT=4174`。首次运行按锁文件安装开发依赖。`./script/workbench.sh --build` 只构建，不监听端口。

数值和浅深色来自 Go AST 对 `cmd/mimac/design.go` 的导出。图表以 `diagrams/*.mmd` 为源，清单记录对应产品源码；修改流程时一并更新。构建使用固定版本 Pele 生成 SVG，浏览器不加载图表引擎或 CDN。每个源码入口链接构建时的只读快照，内容哈希在 `dist/data.json` 中。源码变动后重新运行构建。

组件页是 token 和布局的 Web 样本，原生菜单、SwiftUI Form、系统绿色及 CoreAnimation 效果以实际 App 为准。它不会触发产品功能。正式设计契约见项目根目录 `DESIGN.md`。

Pele 为 MIT 授权：[obsidianmd/pele](https://github.com/obsidianmd/pele)。构建输出保留其许可证。
