# 合并语音验收

一轮使用一个正式 App 身份、一个语音工具和一个输入框。准备前选定句子，准备完成且输出 `READY` 后才让用户按键。沿用 `live_acceptance.py`，不另开监听器、不预触发快捷键。

```sh
python3 script/live_acceptance.py prepare \
  --go /absolute/path/to/go \
  --audio-backend mimac-compat --target-pid TARGET_PID \
  --expected-text '现在检查完整语音，声音来自小米遥控器，最后是测试完成。'
python3 script/live_acceptance.py status /absolute/path/to/run
python3 script/live_acceptance.py finish /absolute/path/to/run
```

Apple 本机识别不用 `--audio-backend` 或 `--target-pid`。已核验的固定包使用原有 `--reuse-build --previous-run`；这些参数不会跳过当前权限、连接、输入法和焦点检查。

同一轮分别收集以下证据，不能用其中一项替代其他项：

| 验收范围 | 同轮证据 | 判定边界 |
| --- | --- | --- |
| 按下、松开与会话清理 | host.json、voice.json | 单会话、按键释放、正常退出 |
| 源 PCM 与本地投递 | voice.json | 源样本、投递计数和未知接受量；不等于目标逐样本消费 |
| 接收设备 | target-timeline.jsonl | 本次实际 PID 和输入 UID；只适用于外部输入法 |
| 输入焦点与输入法 | timeline.jsonl | 从开始至提交期间的连续采样 |
| 正文及尾句完整性 | final-field.json、预设句子 | 只匹配 baseline 后的本轮新增文字，忽略标点和空白；缺字、错字、额外旧字均不通过 |
| 清理后迟到文字 | post-cleanup-field.json、field/events.jsonl | 单列观察，不补救清理前的完整性失败 |
| 电平传给球体 | voice.json 的 sample/render | 只证明观测及调用结果，视觉随声仍须看实际画面 |

不提供 `--expected-text` 时，报告明确标记 `NOT_REQUESTED`，原有“新增文字”通过不能视为整句完整或普遍识别准确率通过。固定句子只用于预先约定的测试；自由口述不用这个判据。

若需要区别源音频缺失与外部输入法漏字，在用户已经同意该诊断时加原有 `--source-check`。它复用本次源 PCM 的内存镜像做 Apple 对照，不额外开麦、不落盘录音；以报告的完整捕获及重放检查为准。

组合顺序：正常完整句一轮，再在相同输入框接一句较短句，覆盖结束、下一次开始、前缀保留和旧结果隔离。冷启动、断连或撤权是独立前置条件，不能混进同一轮后宣称都通过。每轮结束先读所有结果，再决定是否还需要现场操作。
