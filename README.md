# MiMac

macOS 小米遥控器桥接应用，连接现有语音输入工具，保留遥控器各按钮的自定义映射。首个硬件目标为小米 Pro 2。

当前为开发预览：MyGo 菜单栏与设置窗口、原图标、独立状态核心和 Typeless 只读配置模块已实现；真实遥控器、系统按键与音频链路尚未接入。

开发需要 macOS、Python 3 和 Go 1.27.1+。

```sh
./script/build_and_run.sh
./script/build_and_run.sh --verify
go test ./internal/bridge ./internal/adapters/typeless
```

可用 `MIMAC_GO=/path/to/go` 指定工具链。`--verify` 执行有界的设置窗口启动与退出检查。
