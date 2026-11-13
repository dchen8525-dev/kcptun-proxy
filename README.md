# kcptun-proxy

用 Go 复刻 kcptun 的架构（`kcp-go/v5` + `smux v2`），交付**两个独立的命令行程序**：`kcptun-server` 和 `kcptun-client`。参数沿用 FCP 的白名单风格，目的是和 `D:\work\fcp`（C++）、`D:\work\fcp-node`（Node.js）做同条件对比。

不是官方 kcptun 的封装。官方二进制的下载源在本机不通（GitHub Releases API 返回空、asset 404），而且封装别人的 exe 也回答不了「我们自己这套协议栈到底差在哪」。这版是 readable、可测、可交叉编译的源码。

## 架构

```
本地应用 --(SOCKS5 / HTTP CONNECT / absolute-form HTTP)--> kcptun-client
                                                             |
                                                      KCP over UDP
                                                   AES-128-GCM + RS FEC
                                                      smux v2 (多流复用)
                                                             |
                                                        kcptun-server --(TCP)--> 目标站点
```

- 一条 KCP 会话承载多条 smux stream，每条 stream 对应一个本地 TCP 连接。
- stream 的头几个字节是 OPEN 帧（magic `KCPX` + ATYP + 地址 + 端口），服务端据此 `net.Dial` 目标地址。
- 两端共用 `tune()` 的固定 fast3 配置，不存在「一端 fast3 一端 normal」这种错配。

## 环境

| 项 | 值 |
| --- | --- |
| Go | 1.26+（本机 `C:\Program Files\Go\bin\go.exe`，1.27.4，MSI 安装） |
| 模块代理 | `GOPROXY=https://goproxy.cn,direct`（`go.dev` 在本机 TLS 握手失败） |
| 依赖 | `github.com/xtaci/kcp-go/v5 v5.7.5`、`github.com/xtaci/smux v1.5.59`，其余为间接依赖 |
| 外部运行时 | 无。Windows 与 Linux 都是单文件静态二进制，`CGO_ENABLED=0` |

```bash
go env -w GOPROXY=https://goproxy.cn,direct
```

## 构建

```bash
bash scripts/build.sh          # Git Bash / Linux
powershell -File scripts/build.ps1   # PowerShell
```

四个产物，命名统一为 `kcptun-<role>-<os>-<arch>[.exe]`：

```
dist/kcptun-server-windows-amd64.exe
dist/kcptun-client-windows-amd64.exe
dist/kcptun-server-linux-amd64
dist/kcptun-client-linux-amd64
```

`VERSION=0.2.0 bash scripts/build.sh` 会把版本注入 `-h` 首行。

## 运行

**服务端**（VPS）：

```bash
kcptun-server --key=00112233445566778899aabbccddeeff -b=0.0.0.0:4462 --fec=10:3
```

**客户端**（本机）：

```bash
kcptun-client --key=00112233445566778899aabbccddeeff \
              --peer=vps.example.com:4462 -b=127.0.0.1:1080 --fec=10:3
curl -x socks5h://127.0.0.1:1080 https://example.com
```

默认 UDP 端口是 **4462**，不是 FCP 的 4460 / FCP-Node 的 4461 —— 三个隧道可以在同一台 VPS 上并存。目标 VPS 上没有 `ufw`、`nft` 无规则、`iptables INPUT` 为 ACCEPT，主机层无需放通；云控制台安全组仍须允许 UDP 4462 入站（已实测放行）。

**部署 Linux 服务端到 VPS**（2026-11-13 已实装，全过程与验证见 `docs/部署记录.md`）：

```bash
scp dist/kcptun-server-linux-amd64 your-vps:/tmp/kcptun-server.new
scp deploy/kcptun-server.service your-vps:/tmp/kcptun-server.service
ssh your-vps 'install -m 0755 /tmp/kcptun-server.new /usr/local/bin/kcptun-server &&
              install -m 0600 /tmp/kcptun-server.service /etc/systemd/system/kcptun-server.service &&
              systemctl daemon-reload && systemctl enable --now kcptun-server'
```

`deploy/kcptun-server.service` 是实际在跑的单元：`Restart=always`、日志进 journal、开机自启；因为内含 PSK，文件权限设为 `0600`。运维用 `systemctl status kcptun-server` / `journalctl -u kcptun-server -f`；升级先 `install -m 0755` 覆盖二进制再 `systemctl restart kcptun-server`。

`chmod +x` 不是可选项：Windows NTFS 保留不了 Linux 的执行位，`build.sh` 里的 `chmod` 在 NTFS 上是空操作，scp 之后必须补一次（上面的 `install -m 0755` 已包含）。产物是 `CGO_ENABLED=0` 的静态二进制，无外部依赖。

`--key` 两端必须逐字相同，`--fec` 和 `--mtu` 也必须一致 —— 这一版没有参数协商，写错的表现是连不上或静默丢包，不会报错告诉你「两端配置不同」。

一个本地端口同时说三种协议，和 FCP 一致：SOCKS5 CONNECT（无认证）、HTTP CONNECT、absolute-form HTTP（自动重写成 origin-form 并去掉 hop-by-hop 头）。SOCKS5 UDP ASSOCIATE 不支持。

**自测**（不需要服务端、不需要 key）：

```bash
kcptun-client --speedtest --seconds=8
```

## 参数白名单

值参数**必须**写 `--name=VALUE`；开关（`--help`、`--speedtest`）不带值。未知参数、FCP 的旧拼写、kcptun 的官方参数全部是**硬错误**并给出替代写法 —— 静默忽略才是事故。

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `--key=HEX32` | 必需 | 16 字节 PSK，32 个十六进制字符。和 FCP 的 `--key` 是同一个字符串，同一个 key 文件可直接复用。AES-128-GCM。 |
| `-b, --bind=IP[:P]` | server `0.0.0.0:4462`；client `127.0.0.1:1080` | 两端都表示「本地绑定」；缺端口自动补默认值。 |
| `--peer=IP[:P]` | client 必需 | 服务端地址；缺端口补 4462。 |
| `--fec=D:P` | `10:3` | Reed-Solomon 数据/校验分片；`P=0` 关闭 FEC。`D+P<=256`。**P 是固定值**，kcp-go 没有 FCP 的自适应奇偶控制器。 |
| `--mtu=N` | `1400` | UDP 数据报字节数，**含** 20B IPv4 + 8B UDP 头；内部 `SetMtu(N-28)`。范围 576..9000。kcp-go 没有 PMTUD，两端必须一致。 |
| `--log-level=LEVEL` | `info` | `debug\|info\|warn\|error\|off`，日志走 stderr。 |
| `--log-file=PATH` | 无 | 追加写日志，自动建父目录；打开失败直接报错退出。 |
| `--trace=PREFIX` | 无 | 每进程一个 `<PREFIX>-<role>-<pid>.jsonl` 连接事件日志。**不是** FCP 的 FEC 控制器 trace。 |
| `--speedtest` | 关 | client 专用，进程内环回测速，不需要 `--key` / `--peer`。 |
| `--seconds=N` | `8` | `--speedtest` 时长，3..30。 |
| `-h, --help` | — | 首行带版本号。没有 `--version`。 |

stdout 每 500ms 输出一行 `stats 12.4s live=1 up=1 tunnels=1 tx=..B rx=..B tx_rate=..B/s rx_rate=..B/s`，与 FCP 同格式，**不受 `--log-level` 影响**，日志走 stderr。

详见 `docs/参数对照.md`（逐项与 FCP 对照 + 完整 fail-loud 表）和 `docs/InternalProfile.md`（固定不变的内部参数）。

## 与 FCP 的边界

这一版**没有**：PMTUD、自适应 FEC、SACK v2 / ACK v2 TLV、epoch key roll、stateless reset、BBR-lite、丢包控制器。这些都是 FCP 的东西，kcptun 的架构里本来就没有对应物。为它们保留 flag 只会制造「以为开了其实没开」的假象，所以直接不存在，传了就报错。

反过来，FCP 也没有 smux 多流复用（每条本地连接一条会话），这一版有。

## 测试与门禁

```bash
bash scripts/check.sh         # gofmt + vet + go test -count=1 + build + CLI smoke + speedtest
bash scripts/smoke.sh         # 真实两个 .exe + curl，验证三种本地协议
bash scripts/bench.sh         # 32 MiB 直连 vs 隧道吞吐
bash scripts/bench-compare.sh # 再加 fcp-node 同条件对比（默认 ../fcp-node）
```

`go test ./...` 覆盖：参数白名单与全部 **52** 个 fail-loud 拼写、地址归一化（含 IPv6）、OPEN 帧与三种本地协议解析、stats 行格式与计数、以及进程内端到端（SOCKS5 / CONNECT / absolute-form / 2 MiB 校验和 / FEC 开关 / 打开失败传播）。

## 实测（2026-11-13，本机 Windows，环回）

| 项 | 结果 |
| --- | --- |
| `go vet ./...` / `go test -count=1 ./...` | 0 issue / 全绿 |
| `scripts/check.sh` | `ALL GATES PASSED` |
| `scripts/smoke.sh` | SOCKS5 + absolute-form HTTP + HTTP CONNECT 全部 200 |
| `--speedtest --seconds=3` | tx=rx=109,477,888 B，双向 285.42 Mbit/s |
| `bench.sh` 32 MiB | 直连 495.1 MB/s；kcptun-proxy 57.0 / 59.0 MB/s |
| `bench-compare.sh` 32 MiB，MTU 1400B，FEC 10:3 | 直连 477.8 MB/s；**kcptun-proxy 59.6 MB/s**；**fcp-node 12.1 MB/s** |
| 真实进程 stats 行（32 MiB 下载） | client `up=1 tunnels=1 rx=33,554,561B`，server `tx=33,554,561B`，方向对称、无漏计 |

**别过度解读最后一行。** 这是 RTT≈0、零丢包的环回：FEC、拥塞控制、重传策略的价值完全体现不出来，测的其实是纯加密/编解码/FEC 的 CPU 开销。fcp-node 是纯 JS 的 AES-GCM + Cauchy RS，Go 这边是编译过的 kcp-go + AES 硬件加速，4.9× 差距主要来自实现语言，不是协议优劣。真实 VPS 链路已重测，结论确实反转，见下一节 —— 也参见 FCP 仓库 `docs/决策记录.md` #94 记录的环回 BBR 退化，同一类环境效应。

## 实测（2026-11-13，真实 VPS 链路）

服务端已按上文部署到 `vps.example.com`：Debian 13 / systemd 257，UDP 4462，`--fec=10:3 --mtu=1400`，与既有 `fcp-node.service`（UDP 4461）并存。测速源是 VPS 上 `127.0.0.1:18080` 提供的 32 MiB 内存文件，排除公网侧波动；`socks5h` 让目标地址在 VPS 上解析。

| 项 | 结果 |
| --- | --- |
| 服务状态 | `enabled` + `active (running)`，`ss -lunp` 显示 `*:4462`；`kill -9` 主进程后 4 s 内自动换 PID 拉起 |
| 云防火墙 | 隧道出口 IP `vps.example.com`，`https://example.com` 经隧道 200（1.13 s）→ UDP 4462 入站通 |
| IP 分片 | `/proc/net/snmp` 的 `ReasmReqds / ReasmFails / FragCreates` 测前测后均为 0 → MTU 1400 未触发 IP 分片 |
| 32 MiB 下载 ×3 | kcptun-proxy 2.65 / 1.26 / 2.45 MB/s；随后交替复测 1.72 / 1.69 / 1.51 MB/s |
| 同链路 fcp-node ×3（交替） | 2.75 / 3.32 / 2.62 MB/s |
| 共存检查 | `fcp-node.service` 仍 active、UDP 4461 仍监听，未受影响 |

**环回的 4.9× 优势在真实链路上变成 1.8× 劣势。** 这是同一时间窗口交替测量，不是两次独立采样。fcp-node 有 PMTUD（日志实测探测到 1500B）、自适应 FEC（10:6）、BBR-lite；本实现是固定 MTU 1400、固定 FEC 10:3、kcp-go fast3 且 `nc=1`（关闭拥塞控制）。这些差异都可能影响 RTT 229–271 ms 的跨洋链路，但**本次没有做消融实验，不能把 1.8× 归因到其中任何一项**。分片计数为 0 只说明没有 IP 层分片，不排除 UDP 层丢包重传。定位需专门测 FEC / MTU / 拥塞控制的组合，过程与原始数字见 `docs/部署记录.md`。

## 已知限制

- 无 PMTUD，`--mtu` 两端必须手工一致；跨路径 MTU 小于该值时会分片/丢包。
- 无 SOCKS5 UDP ASSOCIATE，DNS 走 `socks5h` 由服务端解析。
- 无认证：只知道 PSK 的人都能连。PSK 即能力凭证。
- `--trace` 只记连接事件，不是控制器轨迹。
- Linux 产物只做了 amd64；其他架构改 `scripts/build.sh` 的 `build_one` 调用即可。
