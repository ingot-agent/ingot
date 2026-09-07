# `tool.shell` Windows UTF-8 Stdio 归一化实现方案

## 1. 背景

当前 Windows 环境下，`tool.shell` 使用 `cmd.exe` 执行命令时，会稳定出现类似错误：

```text
tool "shell_exec" call "...":
tool "shell_exec" returned invalid content:
invalid tool result:
invalid content:
part 0:
text is not valid UTF-8
```

该错误并非模型调用错误，也不是 Tool Result schema 错误，而是 Windows 子进程输出编码与 Ingot SDK Text Content 的 UTF-8 contract 不一致导致。

当前执行链路大致为：

```text
Windows Shell / Process
        │
        │ stdout / stderr raw bytes
        ▼
outputWriter.Write(p []byte)
        │
        ▼
outputCollector.write()
        │
        ▼
bytes.Buffer
        │
        ▼
Buffer.String()
        │
        ▼
content.FromText(...)
        │
        ▼
SDK UTF-8 validation
```

当前 `outputCollector` 直接保存原始 bytes，最终通过：

```go
stdout, stderr := c.stdout.String(), c.stderr.String()
```

构造字符串，没有任何编码转换。

而最终 Result 又直接进入：

```go
content.FromText(collector.format(...))
```

因此只要 Windows Shell 输出 CP936、CP437、CP950 等非 UTF-8 字节序列，就会产生非法 UTF-8 Text。

现有 Progress 路径其实已经部分意识到了这一问题：如果一个 Write chunk 不是合法 UTF-8，就把它作为 `application/octet-stream` 发送，而不是 Text。

因此当前实际上存在两个不同的输出 contract：

```text
ToolProgress
invalid UTF-8
    ↓
binary content
    ↓
不会破坏 SDK invariant

Final Tool Result
invalid UTF-8
    ↓
强制 content.FromText
    ↓
SDK 拒绝
```

本次修改需要彻底统一这两个路径。

---

# 2. 核心设计目标

本次改动的核心目标不是：

> 强制所有 Windows 程序自身使用 UTF-8。

Windows pipe 本质上只是字节流，Shell、内建命令和第三方程序可能采用不同编码，`tool.shell` 无法要求所有外部程序遵循统一编码。

真正应该建立的 contract 是：

> **`tool.shell` 将 Shell/process stdio 视为不可信字节边界；所有文本在进入 Ingot SDK、Observation、Agent 和 Model 之前统一归一化为合法 UTF-8。**

最终架构：

```text
                Agent / Model
                     ▲
                     │
                 UTF-8 Text
                     │
              Ingot SDK Content
                     ▲
                     │
          ┌─────────────────────┐
          │   tool.shell        │
          │                     │
          │ Stdio Normalization │
          │ bytes → UTF-8       │
          └─────────────────────┘
                     ▲
                     │
                  raw bytes
                     │
           Windows Shell / CLI
```

建立以下 invariant：

```go
utf8.ValidString(toolResultText) == true
```

以及：

```text
所有以 content.KindText 暴露的 ToolProgress
必须是合法 UTF-8。
```

---

# 3. 本次修改范围

本次完整方案处理：

- stdout 编码归一化；
- stderr 编码归一化；
- Windows native code page → UTF-8；
- 原生 UTF-8 输出透传；
- Write chunk 跨字符边界；
- final Tool Result；
- streaming ToolProgress；
- output truncation 与字符边界；
- timeout 场景中的 pending bytes；
- 非法/无法识别字节的安全 fallback。

本次不增加：

```json
{
  "stdin": "..."
}
```

因为当前 `shell_exec` Input Schema 只有：

```json
{
  "command": "...",
  "timeout_seconds": 30
}
```



所以当前没有真正的 stdin 数据流需要转码。

这里所说的：

> UTF-8 stdio contract

本次实际落地范围为：

```text
stdout
stderr
```

未来如果增加 stdin，再使用同一 abstraction 做：

```text
UTF-8 Agent stdin
        ↓
Windows native encoder
        ↓
child stdin
```

即可。

---

# 4. 非目标

本次明确不做以下事情。

## 4.1 不强制执行 `chcp 65001`

不修改用户命令为：

```cmd
chcp 65001 >nul & <command>
```

原因：

1. 修改了真实 command semantics；
2. `chcp` 主要影响 console code page；
3. stdout 当前实际通过 pipe 捕获；
4. 第三方程序可以自行决定输出编码；
5. PowerShell/native executable 行为不同；
6. 无法把它作为可靠 invariant。

Windows 官方推荐新程序使用 Unicode/UTF-8，并提供 Console input/output code page，但 console code page 本身不能被简单等价为“所有 redirected pipe 的编码”。

---

## 4.2 不硬编码 GBK

不能写：

```go
decodeGBK(...)
```

因为：

```text
中文 Windows     CP936
繁体 Windows     CP950
日文 Windows     CP932
韩文 Windows     CP949
其他区域          其他 code page
```

系统代码页是运行环境属性。

Windows 提供 OEM/ANSI/Console code page API，因此应由运行时获取实际候选 code page。

---

## 4.3 不把 Shell dialect 重构塞进本次修改

暂时不新增完整：

```go
type ShellKind ...
type ShellDriver interface ...
```

虽然未来 Shell dialect 很可能同时决定：

```text
command arguments
+
stdio encoding policy
```

但本次先建立独立的 Stdio Normalization 层。

---

# 5. 总体架构

建议将当前：

```text
process
 ↓
outputWriter
 ↓
outputCollector
```

修改为：

```text
Process stdout/stderr
        │
        ▼
 normalizedOutputWriter
        │
        ▼
 Stateful Decoder
 raw bytes → UTF-8 bytes
        │
        ├───────────────► ToolProgress
        │                  UTF-8 Text
        │
        ▼
 outputCollector
 UTF-8 only
        │
        ▼
 deterministic formatter
        │
        ▼
 content.FromText()
```

关键变化是：

> `outputCollector` 不再负责保存 arbitrary bytes。

修改之后：

```text
outputWriter 之前：
外部世界 / 不可信 bytes

outputWriter 之后：
Ingot 内部 / UTF-8
```

也就是说，Encoding Boundary 被放在最靠近 OS process 的位置。

---

# 6. 新增 Stdio Decoder abstraction

建议新增：

```text
plugins/tool-shell/
├── encoding.go
├── encoding_windows.go
├── encoding_other.go
```

公共接口不要暴露到 SDK。

可以设计为：

```go
type streamDecoder interface {
    Write([]byte) ([]byte, error)
    Flush() ([]byte, error)
}
```

或者语义更明确：

```go
type textDecoder interface {
    Feed([]byte) []byte
    Flush() []byte
}
```

我更倾向第二种。

Encoding ambiguity 不应该让整个 Shell command 失败，因此 decoder 应该具备：

> loss-tolerant contract

即：

```text
任意 []byte 输入
        ↓
decoder
        ↓
一定得到合法 UTF-8
```

真正无法解释的字节：

```text
→ U+FFFD replacement character
```

而不是：

```text
→ Invoke error
```

---

# 7. `textDecoder` Contract

建议：

```go
type textDecoder interface {
    Feed(p []byte) []byte
    Flush() []byte
}
```

必须满足：

### Feed

```text
输入：
任意 raw bytes

输出：
0 或多个完整 UTF-8 字节

允许：
保留末尾未完成字符到内部 pending buffer
```

### Flush

Process stdout/stderr EOF 后调用：

```text
pending bytes
    ↓
尽最大可能完成 decoding
    ↓
无法转换部分
    ↓
U+FFFD
```

Flush 后：

```text
decoder 不再保留 pending data
```

---

# 8. 非 Windows Decoder

Linux/macOS/Unix-like 默认 contract 仍然是 UTF-8。

但即使外部程序真的输出非法字节，也不能让非法 UTF-8 进入 SDK。

因此 `encoding_other.go` 使用：

```text
UTF-8 incremental decoder
```

而不是：

```go
string(p)
```

核心规则：

```text
完整 UTF-8 rune
    ↓
正常输出

末尾 incomplete rune
    ↓
保留到下一次 Feed

内部 invalid sequence
    ↓
U+FFFD

Flush 时仍 incomplete
    ↓
U+FFFD
```

这样即使：

```text
Write #1:
E4 B8

Write #2:
AD
```

也能正常得到：

```text
中
```

而不会因为 Write chunk 边界制造乱码。

---

# 9. Windows Decoder

Windows 是本次改动的重点。

Windows Decoder 需要支持两类最常见输出：

```text
UTF-8
Windows native code page
```

因此不应该简单：

```text
所有 Windows bytes
→ CP936
```

也不能：

```text
所有 bytes
→ UTF-8
```

而应该使用：

```text
UTF-8 first / native fallback
```

的策略。

---

# 10. Windows source code page 获取

创建 decoder 时解析一个：

```go
nativeCodePage uint32
```

推荐优先级：

```text
GetConsoleOutputCP()
        ↓
如果有效
        使用 console output CP

否则
        ↓
GetOEMCP()
```

`GetConsoleOutputCP()` 返回当前关联 console 的输出 code page；无法获取时返回 0。

`GetOEMCP()` 可以提供当前操作系统的 OEM code page。

因此：

```go
func windowsNativeOutputCodePage() uint32 {
    if cp := getConsoleOutputCP(); cp != 0 {
        return cp
    }

    return getOEMCP()
}
```

注意：

> 这仍然是 native encoding hint，而不是“任意子进程输出编码的绝对真相”。

因此 decoder 仍然需要 UTF-8 autodetection 和最终 fallback。

---

# 11. Windows 编码模式

建议内部定义：

```go
type decoderMode uint8

const (
    decoderUndecided decoderMode = iota
    decoderUTF8
    decoderNative
)
```

每个 stdout/stderr decoder 独立维护 mode。

初始：

```text
decoderUndecided
```

---

# 12. 为什么需要 `Undecided`

假设系统是中文 Windows：

```text
nativeCodePage = CP936
```

但执行：

```cmd
python utf8_program.py
```

该 Python 程序完全可能主动输出 UTF-8。

如果所有输出都直接：

```text
CP936 → UTF-8
```

会把本来正确的 UTF-8 解坏。

反过来：

```cmd
dir
```

又可能输出 Windows native bytes。

因此：

```text
Windows
≠
所有 stdout 都是同一种编码
```

需要一个轻量 autodetection。

---

# 13. Windows autodetection 策略

初始阶段：

```text
ASCII
```

可以直接输出。

因为：

```text
ASCII bytes 0x00–0x7F
```

在 UTF-8 和常见 Windows code page 中一致。

因此无需立即决定 encoding。

第一次遇到非 ASCII 内容时：

```text
pending + current bytes
        ↓
尝试作为合法 UTF-8 sequence
```

### 如果存在明确合法 UTF-8 多字节序列

切换：

```text
decoderUTF8
```

### 如果 UTF-8 明确非法

切换：

```text
decoderNative
```

然后使用：

```text
nativeCodePage
```

转换。

这可以覆盖当前主要场景：

```text
cmd 中文
    ↓
CP936 bytes
    ↓
UTF-8 invalid
    ↓
native decoder
```

以及：

```text
现代 UTF-8 CLI
    ↓
valid UTF-8
    ↓
UTF-8 decoder
```

---

# 14. Autodetection 的边界

必须承认：

> 无编码元数据的 arbitrary byte stream 无法 100% 自动判断编码。

例如某些 CP936 字节序列理论上也可能偶然组成合法 UTF-8。

所以这里解决的是：

```text
practical Windows shell text interoperability
```

不是数学意义上的：

```text
perfect charset detection
```

这也是为什么系统最终 contract 应定义成：

> 自动归一化，并保证合法 UTF-8。

而不是：

> 保证恢复任何未知编码的原始语义。

---

# 15. Windows native 转换

Native conversion 不建议引入 GBK/Shift-JIS 等 Go 第三方 mapping。

当前 plugin 已依赖：

```text
golang.org/x/sys/windows
```



Windows 本身提供：

```text
MultiByteToWideChar
```

可以将指定 Windows code page 的 byte string 转成 UTF-16。

转换链：

```text
native code page bytes
        ↓
MultiByteToWideChar
        ↓
UTF-16
        ↓
utf16.Decode()
        ↓
Go string / UTF-8
```

即：

```text
CP936
CP950
CP932
...
    ↓
Win32
    ↓
Unicode
    ↓
UTF-8
```

这样不需要自己维护 code page mapping。

---

# 16. `MultiByteToWideChar` 使用原则

建议：

```go
func decodeWindowsCodePage(
    data []byte,
    codePage uint32,
    final bool,
) (string, []byte)
```

其中：

```text
string
    已完成 UTF-8 文本

[]byte
    暂时未完成的 trailing bytes
```

Windows 官方文档说明：

```text
MB_ERR_INVALID_CHARS
```

可用于在无效输入时失败；对于 DBCS，如果最后只有 lead byte 而没有 trail byte，也会视为 invalid。

这个行为非常适合 incremental decoder。

---

# 17. Chunk Boundary 处理

这是这次不能忽略的关键点。

`io.Writer.Write(p)` 并不保证一个字符完整出现在一次 Write 中。

例如 CP936：

```text
"中" = D6 D0
```

可能出现：

```text
Write #1
D6

Write #2
D0
```

UTF-8 也同样：

```text
E4 B8 AD
```

可能：

```text
Write #1
E4 B8

Write #2
AD
```

因此：

```go
decode(p)
```

逐 chunk 独立转换是错误设计。

必须维护：

```go
pending []byte
```

流程：

```text
pending
   +
new p
   ↓
combined
   ↓
找到最大可完整解码前缀
   ↓
emit UTF-8
   ↓
剩余 incomplete suffix
   ↓
pending
```

---

# 18. Stateful Decoder

建议：

```go
type windowsTextDecoder struct {
    mode     decoderMode
    codePage uint32
    pending  []byte
}
```

UTF-8 模式：

```text
utf8.FullRune
utf8.DecodeRune
```

Native 模式：

```text
MultiByteToWideChar
```

Undecided 模式：

```text
ASCII
→ 立即输出

non-ASCII
→ UTF-8 probe
→ 决定 UTF8/native
```

---

# 19. Conversion Failure Policy

编码问题不应该变成：

```text
tool execution failure
```

例如：

```go
return tool.Result{}, err
```

不合适。

如果 native decoder 遇到无法恢复的数据：

```text
bad sequence
```

应该：

```text
replace invalid sequence with U+FFFD
```

最终必须满足：

```go
utf8.ValidString(result) == true
```

Windows Vista 以后，`MultiByteToWideChar` 在不使用严格 invalid flag 时可以将非法输入替换成 U+FFFD，这可以作为最终 fallback 的一部分。

---

# 20. 新 `outputWriter`

当前：

```go
type outputWriter struct {
    ctx         context.Context
    collector   *outputCollector
    observation observation.Consumer
    channel     string
    stderr      bool
}
```

建议修改为：

```go
type outputWriter struct {
    mu          sync.Mutex
    ctx         context.Context
    decoder     textDecoder
    collector   *outputCollector
    observation observation.Consumer
    channel     string
    stderr      bool
}
```

必须使用 pointer：

```go
stdoutWriter := &outputWriter{...}
stderrWriter := &outputWriter{...}

command.Stdout = stdoutWriter
command.Stderr = stderrWriter
```

因为 command 完成以后还需要：

```go
stdoutWriter.Flush()
stderrWriter.Flush()
```

---

# 21. `Write()` 新行为

伪代码：

```go
func (w *outputWriter) Write(p []byte) (int, error) {
    w.mu.Lock()
    defer w.mu.Unlock()

    normalized := w.decoder.Feed(p)

    if len(normalized) > 0 {
        w.collector.writeUTF8(w.stderr, normalized)
        w.emitProgress(normalized)
    }

    return len(p), nil
}
```

注意：

即使 decoder 将最后几个 byte 暂存在 pending：

```go
len(normalized) < len(p)
```

`Write()` 仍然必须：

```go
return len(p), nil
```

因为所有原始输入已经被 decoder 接受。

不能返回 short write。

---

# 22. `Flush()`

新增：

```go
func (w *outputWriter) Flush() {
    w.mu.Lock()
    defer w.mu.Unlock()

    normalized := w.decoder.Flush()

    if len(normalized) == 0 {
        return
    }

    w.collector.writeUTF8(w.stderr, normalized)
    w.emitProgress(normalized)
}
```

Flush 应在：

```text
command.Wait()
```

完成以后调用。

因为此时 os/exec 已结束 stdout/stderr pipe copy，可以确认不会再有新的 Write。

---

# 23. `Invoke()` 集成

当前：

```go
command.Stdout = outputWriter{...}
command.Stderr = outputWriter{...}
```

改为：

```go
stdoutWriter := newOutputWriter(...)
stderrWriter := newOutputWriter(...)

command.Stdout = stdoutWriter
command.Stderr = stderrWriter
```

Process 结束：

```text
command.Wait()
    ↓
stdoutWriter.Flush()
stderrWriter.Flush()
    ↓
collector.format()
```

正常退出、non-zero exit、timeout 三条路径都必须完成 Flush。

因此建议增加统一 helper：

```go
func flushOutputWriters(
    stdoutWriter *outputWriter,
    stderrWriter *outputWriter,
)
```

或者在 `Invoke()` 的 wait 完成点统一处理。

---

# 24. Timeout 路径

当前 timeout 后：

```text
terminate
↓
command.Wait()
↓
formatTimeout()
```



修改后必须变成：

```text
terminate
↓
command.Wait()
↓
stdout.Flush()
stderr.Flush()
↓
formatTimeout()
```

否则：

```text
process timeout
+
最后一个字符刚好 pending
```

可能遗漏输出。

---

# 25. `outputCollector` 职责调整

当前 collector：

```text
arbitrary bytes
+
output quota
+
UTF-8 truncation repair
+
formatting
```

职责太多。

新设计：

```text
Decoder
    bytes → UTF-8

Collector
    UTF-8 quota

Formatter
    deterministic envelope
```

因此：

```go
func (c *outputCollector) write(...)
```

调整语义为：

```go
func (c *outputCollector) writeUTF8(...)
```

并增加 invariant：

```go
if !utf8.Valid(p) {
    panic / internal invariant violation
}
```

生产代码不建议因为外部输入 panic，因此实际可以：

```go
if !utf8.Valid(p) {
    p = bytes.ToValidUTF8(p, []byte("\uFFFD"))
}
```

作为 defensive assertion。

但正常路径绝不应该触发。

---

# 26. Output Limit 应以归一化后的 UTF-8 为准

当前 `max_output_bytes` 是输出 payload 上限。

引入 encoding normalization 后，我建议明确：

> `max_output_bytes` 对归一化后的 UTF-8 payload 计数。

例如：

```text
CP936:
2 bytes / 中文

UTF-8:
3 bytes / 中文
```

如果仍按原始 CP936 byte 数计数，最终返回给模型的 UTF-8 Result 有可能超过配置 limit。

因此：

```text
raw bytes
    ↓
decode
    ↓
UTF-8 bytes
    ↓
quota
```

比：

```text
raw quota
↓
decode
```

更符合：

> 控制 Agent/Model 实际接收的数据量

这一安全目标。

---

# 27. UTF-8 截断

Collector 现在接收的已经全部是 UTF-8。

因此如果 quota 落在一个 UTF-8 rune 中间：

```text
remaining = 2

incoming rune:
E4 B8 AD
```

不能写：

```text
E4 B8
```

而应该只写完整 rune 前缀。

增加：

```go
func utf8PrefixWithinLimit(
    data []byte,
    limit int,
) []byte
```

确保：

```go
utf8.Valid(prefix) == true
```

这样 Buffer 本身始终合法 UTF-8。

---

# 28. 删除 `trimIncompleteUTF8Suffix`

当前：

```go
trimIncompleteUTF8Suffix()
```

是在 format 阶段补救：

```text
collector 已经存进非法 UTF-8
↓
最后再试图修尾部
```



新架构中：

```text
decoder
保证 UTF-8

collector
只保存完整 UTF-8 rune
```

因此该函数不再必要，可以删除。

这是一个很重要的改进：

> UTF-8 validity 从“最终修补”升级为“整个内部 pipeline invariant”。

---

# 29. ToolProgress

当前：

```text
valid UTF-8 chunk
→ Text

invalid UTF-8 chunk
→ application/octet-stream
```

修改以后：

```text
raw bytes
↓
stateful decoder
↓
UTF-8
↓
ToolProgress Text
```

因此正常 stdout/stderr progress 全部变成：

```go
content.FromText(string(normalized))
```

不再因为 Windows native encoding 将普通文本标成：

```text
application/octet-stream
```

这会明显改善：

- CLI/TUI 实时输出；
- Web UI；
- observation consumer；
- debugging；
- trace inspection。

---

# 30. Progress 与 Final Result 使用同一 decoder

不能实现两套：

```text
Progress decoder A

Final decoder B
```

否则可能出现：

```text
Progress:
中文

Final:
乱码
```

或者反过来。

必须：

```text
Process bytes
      ↓
唯一 decoder
      ↓
UTF-8
   ┌──┴───┐
   ▼      ▼
Progress Collector
           ↓
         Result
```

一个 byte stream 只能经过一次 decoding。

---

# 31. stdout/stderr Decoder 独立

必须创建：

```go
stdoutDecoder
stderrDecoder
```

而不是共享。

原因：

不同流理论上甚至可能由不同程序行为产生，例如：

```text
stdout → UTF-8
stderr → native code page
```

所以 autodetection state 必须 stream-scoped。

---

# 32. 建议代码结构

最终：

```text
plugins/tool-shell/
│
├── toolshell.go
├── toolshell_test.go
│
├── encoding.go
├── encoding_other.go
├── encoding_windows.go
├── encoding_windows_test.go
│
├── output.go
├── output_test.go
│
├── process_windows.go
├── process_unix.go
└── process_other.go
```

为了控制本次重构规模，也可以暂时保留：

```text
outputCollector
outputWriter
```

在 `toolshell.go`。

但从长期维护性看，我倾向于把它们迁到：

```text
output.go
```

因为 `toolshell.go` 当前已经同时承担：

- Config；
- Environment；
- Tool Definition；
- Invoke；
- Process output；
- Output formatting；
- Argument decode。

Encoding 加入以后继续堆进去会越来越重。

---

# 33. `encoding.go`

平台无关 contract：

```go
type textDecoder interface {
    Feed([]byte) []byte
    Flush() []byte
}

func newTextDecoder() textDecoder
```

通过 build-tag 文件实现平台差异。

---

# 34. `encoding_other.go`

```go
//go:build !windows
```

实现：

```text
incremental UTF-8 validation
+
replacement fallback
```

不依赖任何 locale 环境。

---

# 35. `encoding_windows.go`

```go
//go:build windows
```

包含：

```text
windowsTextDecoder
decoderMode
windowsNativeOutputCodePage()
decodeCodePage()
UTF-8 probe
pending byte management
```

依赖：

```text
golang.org/x/sys/windows
```

不需要新增通用 charset library。

---

# 36. 与 Windows Process Controller 的关系

当前 Windows process controller 使用：

```text
Job Object
CREATE_NEW_PROCESS_GROUP
CREATE_SUSPENDED
```

并在加入 Job 后恢复执行。

Encoding normalization 不应该进入：

```text
process_windows.go
```

因为：

```text
process_windows.go
负责 process lifecycle / containment

encoding_windows.go
负责 stdio representation
```

两者职责必须保持正交。

---

# 37. 与默认 Shell inference 的关系

默认 Shell 推断和 Stdio normalization 也必须保持独立。

```text
Shell Resolver
回答：
执行哪个 shell？

Encoding Boundary
回答：
输出 bytes 怎么进入 Ingot？
```

因此：

```text
resolveShell()
↓
normalizedConfig.shell
```

与：

```text
newTextDecoder()
```

不应耦合。

这样：

```text
默认 cmd.exe
显式 PowerShell
显式 pwsh
显式其他 shell
```

都可以经过同一个 UTF-8 boundary。

---

# 38. PowerShell 情况

当前 Windows 显式指定 PowerShell 可以实际运行。

本次不应该破坏这一行为。

如果 PowerShell/pwsh 输出本身已经是 UTF-8：

```text
UTF-8 autodetection
↓
直接透传
```

如果输出 native code page：

```text
UTF-8 detection fail
↓
native decode
```

所以此次 encoding layer 实际上也提升了显式 PowerShell 的健壮性。

---

# 39. Command 输入

当前：

```go
args.Command
```

已经检查：

```go
utf8.ValidString(*args.Command)
```



Go 在 Windows 上创建 process 时会经过 Windows Unicode process creation path，因此：

```text
Agent UTF-8 command string
```

和：

```text
stdout native byte encoding
```

是两个不同问题。

本次不要为了输出编码去修改：

```text
command argument
```

contract。

---

# 40. Tests：Decoder 单元测试

必须新增 Encoding 层的纯单元测试。

## UTF-8 ASCII

```text
hello
```

保持不变。

## UTF-8 中文

```text
你好世界
```

保持不变。

## UTF-8 跨 chunk

```text
E4 B8
+
AD
```

最终得到：

```text
中
```

---

# 41. Windows CP936 测试

Windows-only：

输入 CP936 编码：

```text
中文测试
```

经过 decoder：

```text
中文测试
```

并验证：

```go
utf8.ValidString(result) == true
```

---

# 42. Windows native chunk boundary

构造一个双字节字符：

```text
byte 1
↓
Feed()

byte 2
↓
Feed()
```

要求：

第一次：

```text
不输出坏字符
```

第二次：

```text
输出完整 UTF-8 rune
```

---

# 43. Invalid byte 测试

构造：

```text
无法按照 UTF-8/native CP 正确解释的 bytes
```

要求：

```text
不 panic
不返回 invalid UTF-8
不使 Invoke 失败
```

最终：

```text
U+FFFD
```

---

# 44. Output Collector 测试

当前已有：

```go
TestOutputCollectorUsesFixedPerStreamQuotas
```



应改成覆盖：

```text
UTF-8 normalized quota
```

例如 limit 恰好落在：

```text
"世界"
```

的第二个 rune 中间。

最终结果必须：

```go
utf8.ValidString(result) == true
```

并正确包含：

```text
[output truncated]
```

---

# 45. Final Result Windows 集成测试

Windows CI 上真实执行：

```cmd
echo 中文
```

要求：

```text
Invoke error == nil
```

Result：

```text
exit_code: 0
stdout:
中文
stderr:
```

最关键：

```go
utf8.ValidString(resultText(result)) == true
```

这个测试就是当前 bug 的回归测试。

---

# 46. stderr Windows 测试

测试：

```cmd
echo 中文错误 1>&2
```

要求：

```text
stderr:
中文错误
```

且 UTF-8 valid。

stdout 和 stderr 都必须覆盖。

---

# 47. Progress Windows 测试

Observation consumer 捕获：

```text
ToolProgress(stdout)
```

以及：

```text
ToolProgress(stderr)
```

要求 Content 是：

```text
text
```

而不是：

```text
application/octet-stream
```

并验证中文正确。

---

# 48. Timeout + pending 测试

构造：

```text
输出一个多字节字符的一部分
↓
process timeout
```

要求：

```text
timeout result
```

仍然是合法 UTF-8。

不得因为 pending bytes：

```text
再次触发 invalid tool result
```

---

# 49. 大输出测试

输出远超过：

```text
max_output_bytes
```

的 Windows 中文。

要求：

- 不超过 normalized quota；
- truncation marker 正确；
- UTF-8 valid；
- stdout/stderr pipe 继续 drain；
- 不发生 deadlock。

---

# 50. 并发测试

每个 Invoke 必须拥有独立：

```text
stdout decoder
stderr decoder
collector
```

多个 shell invocation 并行时：

```text
decoder mode
pending bytes
code page state
```

不得共享。

继续：

```text
go test -race
```

验证。

---

# 51. 现有测试修改

当前测试 helper 会根据 OS 自动填充 Shell。

如果默认 Shell inference 已经落地，应同步删除这层隐藏注入。

Encoding 相关测试不要依赖 test helper 偷偷改变环境。

另外：

```go
TestOutputCollectorUsesFixedPerStreamQuotas
```

中的 UTF-8 truncation 测试应由：

```text
formatter 修补
```

迁移到：

```text
collector UTF-8 invariant
```

---

# 52. Error Policy

Encoding 错误不新增 exported error：

```go
ErrInvalidEncoding
```

因为外部程序输出 arbitrary bytes 并不是配置错误。

因此：

```text
Shell exit
→ Tool Result

Unknown encoding
→ replacement

Invalid byte
→ replacement

Decoder ambiguity
→ best effort

Process I/O failure
→ Go error
```

只有真正的：

```text
pipe/process/OS failure
```

才应导致 Invoke error。

---

# 53. Observation Policy

Encoding fallback 本身不需要额外 Tool lifecycle event。

如果未来需要 debug，可以在内部日志/telemetry 中记录：

```text
native_code_page
decoder_mode
replacement_count
```

但不要把这些直接写进 Tool Result。

模型不需要看到：

```text
decoder switched to CP936
```

这种实现细节。

---

# 54. 可观测指标建议

如果后续 telemetry 需要，可以增加内部 observation metadata：

```text
shell.output.encoding = utf8
shell.output.encoding = windows-native
shell.output.code_page = 936
shell.output.replacements = 0
```

但这属于后续增强。

本次不是必须项。

---

# 55. 文档更新

更新：

```text
docs/plugin-designs/tool.shell_v0.1.md
```

当前设计文档只要求：

> 截断不能制造非法 UTF-8。



这个约束太弱。

应该升级成：

> `tool.shell` 将 stdout/stderr 视为外部 byte stream，并在进入 SDK Content 边界前统一归一化为 UTF-8。Final Result 和 textual ToolProgress 永远不得携带非法 UTF-8。

同时修改当前关于 Progress：

```text
invalid UTF-8
→ application/octet-stream
```

的描述。

新 contract：

```text
raw shell bytes
→ stream normalization
→ UTF-8 textual ToolProgress
```

真正无法恢复的 sequence：

```text
→ replacement character
```

而不是 binary progress。

---

# 56. Config 是否需要新增 encoding

不增加：

```toml
encoding = "gbk"
```

也不增加：

```toml
encoding = "utf-8"
```

原因：

1. zero-config 应正常工作；
2. 大部分用户不知道 code page；
3. Shell 下还可能运行多个不同 executable；
4. encoding 属于 process boundary policy；
5. 显式固定 encoding 很容易与真实子程序不一致。

因此：

```text
Config ABI
保持不变
```

---

# 57. SDK / ABI 影响

本次不改变：

```go
Config
Dependencies
Exports
Tool Definition
Tool Result envelope
```

Result 仍然：

```text
exit_code: 0
stdout:
...
stderr:
...
```

只是以前 envelope 内可能存在非法 UTF-8，现在正式保证 UTF-8。

因此：

```text
不需要 ABI bump
不需要 SDK bump
不需要 manifest version bump
不需要 config migration
```

---

# 58. 实现顺序

建议按以下顺序开发。

## Step 1：建立 Decoder abstraction

新增：

```text
encoding.go
encoding_other.go
encoding_windows.go
```

完成 unit tests。

---

## Step 2：让 Final Result 使用 UTF-8 pipeline

改造：

```text
outputWriter
outputCollector
```

建立：

```text
raw bytes
→ decoder
→ collector
```

优先让当前：

```text
invalid tool result
```

问题彻底消失。

---

## Step 3：改造 ToolProgress

让 Progress 使用 decoder 产生的同一 UTF-8 chunk。

删除：

```text
invalid UTF-8
→ application/octet-stream
```

这一临时 fallback。

---

## Step 4：重构 truncation

让：

```text
max_output_bytes
```

作用于 normalized UTF-8。

删除：

```text
trimIncompleteUTF8Suffix()
```

---

## Step 5：Windows CI

增加：

```text
cmd 中文 stdout
cmd 中文 stderr
native encoding
UTF-8 executable
chunk boundary
```

集成测试。

---

## Step 6：更新设计文档

明确 UTF-8 boundary contract。

---

# 59. 最终数据流

修改完成后：

```text
                         Windows Process
                               │
                               │ raw bytes
                               ▼
                    ┌─────────────────────┐
                    │ outputWriter        │
                    │                     │
                    │ Stateful Decoder    │
                    │ native/UTF8 → UTF8  │
                    └──────────┬──────────┘
                               │
                        valid UTF-8 only
                               │
                 ┌─────────────┴─────────────┐
                 │                           │
                 ▼                           ▼
          ToolProgress               outputCollector
            UTF-8                     UTF-8 quota
                                             │
                                             ▼
                                      result formatter
                                             │
                                             ▼
                                     content.FromText
                                             │
                                             ▼
                                           Agent
```

---

# 60. 核心 invariant

实现完成后可以把下面四条作为代码评审标准。

### Invariant 1

```text
Process boundary 外：
arbitrary bytes
```

### Invariant 2

```text
进入 collector 后：
UTF-8 only
```

### Invariant 3

```text
任何 Content.KindText：
UTF-8 only
```

### Invariant 4

```text
Encoding ambiguity：
不能导致整个 Tool Call 失败
```

---

# 61. 已确定的设计决策

## 决策 1：是否只针对 GBK 修复？

否。

实现平台级：

```text
Windows native encoding → UTF-8
```

---

## 决策 2：是否使用 `chcp 65001` 强制 Shell？

否。

它可以改善部分程序，但不足以作为可靠 I/O contract。

---

## 决策 3：是否把原始 invalid bytes 继续作为 binary Content？

Final Result 不允许。

stdout/stderr 属于 shell textual output，统一经过 decoder 后作为 UTF-8 Text。

---

## 决策 4：是否只修 Final Result？

完整实现同时处理：

```text
Final Result
+
ToolProgress
```

二者使用同一 decoding pipeline。

---

## 决策 5：是否逐 Write chunk 转码？

不能无状态转码。

必须使用：

```text
stateful incremental decoder
```

处理字符跨 Write 边界。

---

## 决策 6：Windows 是否硬编码 CP936？

否。

优先使用 runtime code page。

---

## 决策 7：是否增加 `encoding` Config？

否。

保持自动行为。

---

## 决策 8：编码错误是否导致 Invoke Error？

否。

编码问题采用 replacement fallback。

---

## 决策 9：`max_output_bytes` 按什么统计？

按最终归一化后的：

```text
UTF-8 bytes
```

统计。

这样才能真正控制 Agent/Model 接收到的数据大小。

---

## 决策 10：是否继续保留 `trimIncompleteUTF8Suffix()`？

不保留。

正确的职责是：

```text
Decoder 保证 encoding
Collector 保证 rune-safe truncation
```

而不是 Formatter 最后补救。

---

# 62. 最终验收标准

该功能完成后至少必须满足：

```text
Windows 中文 cmd 输出
→ 不再出现 text is not valid UTF-8

Windows stdout
→ UTF-8

Windows stderr
→ UTF-8

ToolProgress
→ UTF-8 Text

Final Result
→ UTF-8 Text

UTF-8 native CLI
→ 不被错误转码

Windows native code page CLI
→ 自动转 UTF-8

跨 Write 字符
→ 不乱码

output truncation
→ 不截断字符

timeout
→ 不留下 invalid pending bytes

多 Invocation 并发
→ decoder 状态完全隔离
```

最终可以把这次修改定义为：

> **`tool.shell` 正式建立 UTF-8 textual I/O boundary：外部 Shell 和进程可以继续使用平台原生字节编码，但 Ingot Runtime、Observation、Agent 与 Model 永远只接触稳定、合法的 UTF-8 文本。**

这比针对 `cmd.exe` 增加一次 GBK 转码更符合 Ingot 当前的 capability boundary 设计，也能成为以后 PowerShell、多 Shell dialect 和 stdin 能力继续扩展的基础。