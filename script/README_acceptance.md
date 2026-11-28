# 合并语音验收

一轮使用一个正式 App 身份、一个语音工具和一个输入框。准备前选定单句或有序句子列表，准备完成且输出 `READY` 后才让用户按键。沿用 `live_acceptance.py`，不另开监听器、不预触发快捷键。

```sh
python3 script/live_acceptance.py prepare \
  --go /absolute/path/to/go \
  --audio-backend mimac-compat --target-pid TARGET_PID \
  --expected-text 'PLANNED_SENTENCE'
python3 script/live_acceptance.py status /absolute/path/to/run
python3 script/live_acceptance.py finish /absolute/path/to/run
```

默认合同仍是一次准备、一次按压和一个语音会话。若要在同一 run 连续验收多句，在 `prepare` 前一次性为每次按压重复 `--expected-text`，顺序就是预期会话顺序：

```sh
python3 script/live_acceptance.py prepare \
  --go /absolute/path/to/go \
  --audio-backend mimac-compat --target-pid TARGET_PID \
  --expected-text 'PLANNED_SENTENCE_1' \
  --expected-text 'PLANNED_SENTENCE_2'
```

每句按压必须产生一个独立会话；下一次按压开始前，夹具中必须已有上一句的精确、已提交文本。`finish` 按会话分别核对非空源 PCM 与本地投递、适用时的目标输入路径、会话期间及前句提交时的焦点，以及该句的精确新增文本。总文本正确不能代替逐句检查。单个 `--expected-text` 的调用仍使用原单句合同；未指定时仍不作整句准确性判断。

Apple 本机识别和 Hex 本机识别省略 `--audio-backend` 与 `--target-pid`。Hex 先创建并取消一条空流，确认引擎可用并释放占用后再准备遥控器。未加载模型时停在此步。已核验的固定包使用原有 `--reuse-build --previous-run`；这些参数不会跳过当前权限、连接、输入法和焦点检查。

冻结 Dev 的真机验收用 `prepare --fixed-dev-receipt /path/to/bundle-verification.json`。省略 `--audio-backend` 会沿用应用的正常选路，可验证已安装的声音组件是否被自动采用。

安装包专项验收用 `prepare --installed-beta-manifest /path/to/release.json`，外部输入法仍传 `--target-pid`，整句验收传 `--expected-text`；多句时重复该参数。该入口启动已安装且已退出的 `/Applications/MiMac.app`，沿用正常配置。绑定日用进程之前先结束日用会话，按准备阶段结果推进；Beta 安装、系统要求重开及球体实窗变化另行核对。

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

未重复 `--expected-text` 时，每个 `prepare/finish` run 仍只允许一个会话；多句 run 只允许与预先声明句数一致的会话数。`finish` 会关闭 App 和自有输入窗口。一个批次若安排不同合同的完整句和短句，应分别准备两个 run。连续输入、同窗口前缀保留和旧结果隔离仍需按对应会话逐句核对，不能由合并后的总文本或两次独立 run 推导。冷启动、断连或撤权也各需对应前置条件。每轮先收齐同一会话的所有证据，再决定是否还需要现场操作。

### Apple 和 Hex 的听写与字幕

在自有编辑器、有焦点但不可编辑的窗口分别开始听写，核对两者均收到源音频和最终结果。前者须在原选区只写入一次。后者及中途改换选区的场景，须在菜单中出现「复制本次听写」，主动点击才写剪贴板。取消会话后检查迟到的结果。用自有合成句验复制内容，保持日用剪贴板独立。

使用点阵时分别核对关闭字幕、Apple 实时文字、支持中间结果的 Hex 模型、只返回最终文字的模型。记录首个中间结果是否在松键前可见，以及最终文字是否重复。只点亮开关或弹出语音条不足以验证字幕。

### 输入法原生录音条

使用自有空白编辑器和合成音频，分别采集当前输入法的拼音候选窗、录音条及松键后的窗口。只对本次会话新出现且匹配当前工具的录音窗做位置验证。检查启用实验时录音全程和松键后仍不可见，跟随工具及关闭实验时正常显示，编辑器全程保有焦点，合成句只落字一次。换工具、取消和退出也要释放观察与窗口引用。每款输入法单独记录结果。

选择语音球，在同一轮记录球的实际窗口位置、出现与收起、原编辑器焦点，并对齐本次会话的准备、收音、电平和结束记录。在第三方录音条识别成功、没有匹配和关闭隐藏时，分别验证本地球仍独立显示。

记录同一窗口编号从移开、松键到自然关闭的连续坐标、可见性和采样间隔。独立观察器确认移出后才送合成音频。松键后任何一次可见回返都记为失败，即使最后一帧已消失、文字也已写入。

### 日用收音故障日志

在「控制台」选中当前 Mac，以类别 `remote-audio` 和应用的 Bundle ID 筛选。终端可查询最近十分钟的日用记录，独立 Dev 将子系统改为 `dev.mimac.app.dev`。

```sh
/usr/bin/log show --last 10m --style compact --predicate 'subsystem == "dev.mimac.app" AND category == "remote-audio"'
```

同一连接依次记录订阅就绪、遥控器开始发送、首个音频包和停止时收到的字节数。出现传输错误时记录数值错误码。日志只包含事件与计数；判断插件读入和整句结果仍用上面的现场流程。

打包运行时，应用数据目录的 `audio-session.log` 记录宿主侧边界。用 UTC 时间与系统日志对时，再按 PID、设备代际、按压编号和会话编号关联。按下后若没有建立会话，查看同一按压的启动前检查。已建立的会话按 `prepared`、`drained` 或 `failed` 跟踪；失败的 `stage` 区分音源读取、等待读端、读端停读、写入、排空与获取最终文字。ATVV 源时序失败只记录固定枚举：`source_audio_before_start`（AUDIO_START 前音频）、`source_audio_after_stop`（AUDIO_STOP 后的迟到音频）、`source_stop_without_active_stream`（无活动流时收到 AUDIO_STOP；不进一步断言是重复 stop 还是缺少 start）；不记录逐包事件或内容。`source_rebuild` 和 `source_recovery_rearmed` 记录自动恢复及重新获得恢复机会的时点。按同一连接对齐系统日志的 `remote-audio`。停止通知中的 `received_bytes` 是收到的蓝牙字节数，`stop_reason=-1` 表示设备未给原因，`host_close_sent` 表示 MiMac 在通知之前已向该流发出关闭请求。目标停读、遥控器停发与用户松键分别取各自的时间戳。

该文件按当前应用配置目录隔离，只写阶段和结果代号，达到 1 MiB 后在下一次写入时截断。普通启动即可保留这些事件。每个音频块的投递计数或识别文字仍需对应的现场验收证据。

离线按 press 核对可用 `script/inspect_audio_session_trace.py`。必须指定单个 `audio-session.log`、PID 和含时区的 UTC 起止时间，避免把进程重开或其他 PID 的事件拼在一起：

```sh
python3 script/inspect_audio_session_trace.py --trace "$HOME/Library/Application Support/MiMac/audio-session.log" --pid "$PID" --start "$START_UTC" --end "$END_UTC"
```

摘要只读取该日志的八个固定字段，不打开音频或听写正文。只有 `press` 记录里的非零 session 才作为该 press 接纳的 session；`press_during_session` 是新 press，其 `session` 仅作旧会话引用，不接收旧句的 `prepared`/`drained`/`failed` 回执。回执按 PID、generation 和接纳的 session 精确关联。范围内没有终态时标为 `unknown`，不会从释放或其他事件推断完成。`failure_count` 是失败 press 数，不是错误日志行数。可选 `--voice-report PATH` 用既有 `inspect_voice_report.py` 单独摘要本地 I/O 计数；报告没有 PID，因此会明确保持未关联，不按可重复的 session 编号合并。退出码 1 表示有失败、未知终态或未匹配生命周期事件，2 表示输入无效。

### Verify the ordinary-launch audio route

A session driven by an explicit backend argument verifies only that diagnostic launch. Check the ordinary launch with the same packaged identity, current tool, and installed audio endpoint before a Beta handoff. With a missing backend sidecar, a verified compatibility endpoint and a supported current tool select the existing route automatically. A saved or one-run empty value preserves shortcut-only mode. The independent restart verifier needs the expected automatic route for the fixed test machine. Confirm real source, target consumption and text in the live acceptance flow.
