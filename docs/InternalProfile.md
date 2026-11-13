# 固定的内部参数（InternalProfile）

kcptun 官方有 15+ 个调优 flag。这一版把它们**全部固定**，理由不是「少即是多」这种空话，而是：

1. 两个 exe 由同一份源码编译，固定值保证两端永远一致。可配置的调优参数最常见的故障就是「一端改了、另一端没改」，而且症状是吞吐慢慢劣化，不是立刻报错，极难定位。
2. 对比脚本需要可复现的基线。每次跑都能从 `-h` 和这份文档还原出完整的运行配置，不需要翻命令行历史。
3. 这些值本来就是 kcptun `fast3` 的推荐组合，对目标场景（VPS 代理，中等丢包、~150ms RTT）没有需要临时改的余地。

要改，就改 `internal/tunnel/tunnel.go` 的常量后重新编译 —— 改动会进 git、会过门禁，不会从某个忘记传的 flag 里悄悄溜进来。

## 常量表

| 常量 | 值 | 对应 kcptun flag | 作用 |
| --- | --- | --- | --- |
| `KCPNoDelay` | `1` | `--nodelay 1` | 开启无延迟模式 |
| `KCPInterval` | `10` ms | `--interval 10` | KCP 内部 update 周期 |
| `KCPResend` | `2` | `--resend 2` | 快速重传触发次数 |
| `KCPNC` | `1` | `--nc 1` | 关闭拥塞控制的流控退让 |
| `KCPSndWnd` / `KCPRcvWnd` | `1024` | `--sndwnd 1024 --rcvwnd 1024` | 发送/接收窗口（包数） |
| `KCPDSCP` | `46` | `--dscp 46` | EF，让中间设备优先转发 |
| `SocketBuf` | `4 MiB` | `--sockbuf 4194304` | UDP 读写缓冲 |
| `SmuxFrame` | `8192` | `--smuxbuf` 的帧粒度 | 单个 smux 帧最大载荷 |
| `SmuxSession` | `16 MiB` | `--smuxbuf` | 会话级接收缓冲 |
| `SmuxStream` | `4 MiB` | `--streambuf` | 单流接收窗口 |
| smux `Version` | `2` | `--smuxver 2` | 带流控的 v2 协议 |
| keepalive | `10s` / `30s` | `--keepalive` | smux NOP 间隔 / 超时 |
| 加密 | AES-128-GCM | `--crypt aes` + `--key` | 唯一模式，无法关闭 |
| 压缩 | 关 | `--nocomp` | kcp-go v5 没有内置压缩 |

这套组合等价于 kcptun 的 `--mode fast3`（`nodelay=1 interval=10 resend=2 nc=1`），窗口与缓冲区取官方常用值。

## 调用点

```go
// internal/tunnel/tunnel.go
func tune(s *kcp.UDPSession, mtu int) {
    s.SetStreamMode(true)
    s.SetWriteDelay(false)
    s.SetACKNoDelay(true)
    s.SetNoDelay(KCPNoDelay, KCPInterval, KCPResend, KCPNC)
    s.SetWindowSize(KCPSndWnd, KCPRcvWnd)
    s.SetMtu(mtu)
    _ = s.SetDSCP(KCPDSCP)          // accepted session 会返回 errInvalidOperation
    _ = s.SetReadBuffer(SocketBuf)
    _ = s.SetWriteBuffer(SocketBuf)
}
```

`kcp.UDPSession.SetDSCP` 对从 `Listener.AcceptKCP()` 拿到的会话返回 `errInvalidOperation`（它不能改共享的监听 socket）。所以服务端在 `Listen()` 里对 `kcp.Listener` 再调一次 `SetDSCP(46)`；客户端的 dial 会话则由 `tune()` 设置。这里刻意忽略错误，不是漏了检查。

`SetStreamMode(true)` 是 deprecated API，但 smux 需要字节流语义；kcp-go v5 默认的消息模式会把每次 Write 当独立消息，smux 帧会被错误切分。

## 唯一可调的传输参数

`--mtu=N`（默认 1400 数据报字节，内部 `SetMtu(N-28)`）和 `--fec=D:P`（默认 10:3）。这两个是**路径相关**的，必须允许两端一起指定；其余都是**实现相关**的，固定。

`SetMtu` 内部还会再扣掉 KCP 头 `headerSize` 和 AEAD 的 16B overhead，所以传入的是 UDP 载荷预算，不是最终 KCP 分片大小。`--mtu=1400` → `SetMtu(1372)`。

## 超时

| 常量 | 值 | 说明 |
| --- | --- | --- |
| `OpenTimeout` | 15s | 等 OPEN 帧 / 等服务端 OPEN 回复 |
| `DialTimeout` | 10s | 服务端连目标站点的 TCP 超时 |
| 本地请求头 | 30s | client 对本地连接的握手 deadline，建流后清除 |
| 本地请求头上限 | 64 KiB | 超过返回 431/断开，防止无边界缓冲 |

## 关于环回性能

`RTT≈0` 时 KCP 的快速重传与窗口机制会表现出与真实链路不同的行为（FCP 仓库 `docs/决策记录.md` #94 记录过 C++ 客户端在环回上的 BBR pacing 自锁，吞吐只有 Node 客户端的 29%，注入 60ms RTT 后差距从 70% 缩到 6%）。本版同样**不要用环回数字推断 VPS 表现**。`scripts/bench-compare.sh` 的用途是验证实现没退化，不是给协议排名。
