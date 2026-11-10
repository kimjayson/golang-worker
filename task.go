package worker

import (
	"context"
	"errors"
)

// Task 是一个可以被工作池执行的单元。
//
// 实现者需要保证 Run 在 ctx 被取消时尽快返回：
// 工作池只会把取消信号传递进来，不会强制终止 goroutine。
type Task interface {
	Run(ctx context.Context) error
}

// TaskFunc 把普通函数适配成 Task，便于直接提交匿名函数。
type TaskFunc func(ctx context.Context) error

// Run 实现 Task 接口。
func (f TaskFunc) Run(ctx context.Context) error {
	if f == nil {
		return ErrNilTask
	}
	return f(ctx)
}

var (
	// ErrNilTask 表示提交了空任务。
	ErrNilTask = errors.New("worker: nil task")

	// ErrPoolClosed 表示工作池已经关闭，不再接收新任务。
	ErrPoolClosed = errors.New("worker: pool is closed")

	// ErrShutdownTimeout 表示等待任务完成超时。
	// 超时后执行中的任务会收到 ctx 取消信号，工作池不再等待它们。
	ErrShutdownTimeout = errors.New("worker: shutdown timeout")
)
