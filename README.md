# golang-worker

一个用 Go 实现的轻量 goroutine 工作池：**任务队列 + 失败重试 + 执行超时 + panic 兜底 + 优雅关闭 + 运行统计**。

无第三方依赖，只依赖标准库。适合作为学习「并发控制」的样板，也足够撑起中小型服务的异步任务处理。

```
go get github.com/kimjayson/golang-worker
```

## 特性

| 能力 | 说明 |
| --- | --- |
| 并发限流 | 固定协程数消费队列，控制同时执行的任务数量 |
| 队列缓冲 | 可配置队列容量，`0` 表示无缓冲（提交方与执行方同步交接） |
| 失败重试 | `MaxRetries` + `RetryBackoff`，可配置重试次数与退避时间 |
| 执行超时 | `TaskTimeout` 为每次尝试派生带超时的 ctx |
| panic 兜底 | worker 内 `recover`，异常转为错误，协程不退出 |
| 优雅关闭 | 先停止接收、再排空队列与执行中任务，支持超时兜底 |
| 运行统计 | 提交数 / 完成数 / 失败数 / 重试数 / 并发数 / 排队数 |
| 错误出口 | 只读错误通道，非阻塞投递，不影响执行 |

## 快速开始

```go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	worker "github.com/kimjayson/golang-worker"
)

func main() {
	pool := worker.New(
		worker.WithWorkers(4),
		worker.WithQueueSize(8),
		worker.WithTaskTimeout(2*time.Second),
		worker.WithMaxRetries(3),
		worker.WithRetryBackoff(100*time.Millisecond),
	)

	// 后台消费错误通道
	go func() {
		for err := range pool.Errors() {
			log.Printf("任务失败: %v", err)
		}
	}()

	for i := 0; i < 100; i++ {
		id := i
		err := pool.Submit(context.Background(), worker.TaskFunc(func(ctx context.Context) error {
			return handle(ctx, id)
		}))
		if err != nil {
			log.Printf("提交任务 %d 失败: %v", id, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Shutdown(ctx); err != nil {
		log.Printf("关闭工作池: %v", err)
	}

	fmt.Printf("%+v\n", pool.Stats())
}

func handle(ctx context.Context, id int) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(100 * time.Millisecond):
		return nil
	}
}
```

运行内置示例（模拟 20% 失败率）：

```bash
go run ./cmd/demo -n 100 -w 5 -fail 20
```

## 核心概念

### Task

```go
type Task interface {
	Run(ctx context.Context) error
}
```

返回 `error` 让工作池知道任务成败，这是重试与统计的前提。普通函数用 `TaskFunc` 适配：

```go
pool.Submit(ctx, worker.TaskFunc(func(ctx context.Context) error {
	return doSomething(ctx)
}))
```

任务实现**必须**在 `ctx` 被取消时尽快返回，工作池只会传递取消信号，不会强制终止 goroutine —— 这是 Go 的天然限制，不是实现缺陷。

### 配置项

| 选项 | 默认值 | 说明 |
| --- | --- | --- |
| `WithWorkers(n)` | `1` | 并发协程数 |
| `WithQueueSize(n)` | `0` | 队列容量，`0` 为无缓冲 |
| `WithTaskTimeout(d)` | `0` | 单次尝试超时，`0` 不限制 |
| `WithMaxRetries(n)` | `0` | 失败后的重试次数（不含首次） |
| `WithRetryBackoff(d)` | `0` | 两次尝试之间的等待 |
| `WithPanicHandler(fn)` | `nil` | panic 恢复后的回调 |

### 提交语义

- `Submit` 在队列满时**阻塞**，直到有空位、`ctx` 取消或工作池关闭，因此天然具备背压能力；
- `ctx` 取消返回 `ctx.Err()`，且不计入 `Submitted` 统计；
- 工作池关闭后返回 `ErrPoolClosed`；
- 提交 `nil` 返回 `ErrNilTask`。

### 关闭语义

`Shutdown(ctx)` 分三段：

1. 置位 `closed`，拒绝新任务（已经在提交途中的任务仍会被受理并执行完）；
2. 等待所有提交方结束，然后关闭队列；
3. worker 排空队列后退出，全部退出即返回 `nil`。

若 `ctx` 先到期，返回包装了 `ErrShutdownTimeout` 的错误，同时取消所有任务的 ctx 促其退出 —— 但**不会**等待它们真正结束。这一步配合 `context.WithTimeout` 使用是标准做法：

```go
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()
if err := pool.Shutdown(ctx); err != nil && errors.Is(err, worker.ErrShutdownTimeout) {
	log.Printf("仍有任务未结束，已放弃等待")
}
```

## 设计说明

三个容易踩坑的地方，这里都做了处理：

**1. 关闭队列与并发提交的竞态。** 直接 `close(queue)` 而另一个 goroutine 正在 `queue <- task`，会 panic。实现里 `Submit` 在 `RLock` 内对 `senders` 计数，`Shutdown` 先置 `closed=true`（此后不会再有新的计数者），再 `senders.Wait()`，最后才关闭队列，从而杜绝这一 panic。

**2. 关闭不等于中断。** 关闭只用「停止接收 + 排空」的方式，不通过关闭队列来中止任务，因此执行中的任务拿到的 ctx 是完整的，不会出现「日志写一半就被掐断」的情况。

**3. 错误通道非阻塞投递。** 缓冲满时错误被丢弃，但 `Stats().Failed` 照常累加。这是刻意的取舍：宁可丢日志，也不让监控通道阻塞业务执行。若不能接受丢弃，请调大 `WithQueueSize` 或持续消费 `Errors()`。

## 统计

```go
st := pool.Stats()
// st.Submitted 成功入队的任务数
// st.Completed 执行成功的任务数（含重试后成功）
// st.Failed    重试耗尽仍失败的任务数
// st.Retried   实际重试次数
// st.Active    当前执行中的任务数
// st.Queued    当前排队中的任务数
```

一次任务的重试不会重复计入 `Submitted`，提交一次记一次。

## 从 v1 迁移

v1 是 `NewWorker(ctx, n)` + `Run(w)` + `Shutdown()` 的三件套，v2 有破坏性变更：

| v1 | v2 |
| --- | --- |
| `task.NewWorker(ctx, 5)` | `worker.New(worker.WithWorkers(5))` |
| 接口 `Worker.Task(ctx)` | 接口 `Task.Run(ctx) error` |
| `pool.Run(w)` | `pool.Submit(ctx, t)` / `pool.SubmitFunc(ctx, fn)` |
| `pool.Shutdown()` | `pool.Shutdown(ctx)`，返回 `error` |
| 无 | `Errors()` / `Stats()` / 重试 / 超时 / panic 兜底 |

v1 里 `ctx` 传入后并未真正传给任务，v2 修正了这一点；同时删除了无法编译的 `demo.go`（其中 `p.Run(&lc)` 引用了不存在的变量），示例改到 `cmd/demo`。

## 目录结构

```
.
├── task.go              # Task 接口、TaskFunc、哨兵错误
├── options.go           # Options 与函数式选项
├── worker.go            # Pool 主体：提交、执行、重试、关闭
├── stats.go             # 统计快照
├── worker_test.go       # 14 个用例，覆盖并发/重试/超时/关闭/panic
├── cmd/demo/            # 可运行示例
├── Makefile
└── .github/workflows/   # CI: gofmt + vet + build + race test
```

## 开发

```bash
make test    # go test -count=1 ./...
make race    # go test -race -count=1 ./...
make vet     # go vet ./...
make demo    # go run ./cmd/demo -n 100 -w 5 -fail 10
```

CI 会在每次 push 与 PR 时执行 gofmt 检查、`go vet`、`go build` 与 `go test -race`。

## Roadmap

- [ ] 泛型结果通道 `SubmitResult[T]`
- [ ] 动态调整协程数
- [ ] 优先级队列 / 延时任务
- [ ] `Stats()` 到 Prometheus 指标的转换示例

## License

[MIT](./LICENSE)
