// Package worker 提供一个带任务队列、失败重试、执行超时和优雅关闭的 goroutine 工作池。
//
// 最小用法：
//
//	pool := worker.New(worker.WithWorkers(4), worker.WithQueueSize(16))
//
//	if err := pool.Submit(ctx, worker.TaskFunc(func(ctx context.Context) error {
//		return handle(ctx)
//	})); err != nil {
//		log.Printf("提交任务失败: %v", err)
//	}
//
//	// 停止接收新任务，并等待队列与执行中的任务全部完成
//	if err := pool.Shutdown(ctx); err != nil {
//		log.Printf("关闭工作池: %v", err)
//	}
package worker

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Pool 是一个并发执行 Task 的工作池。
//
// 请使用 New 创建，零值 Pool 不可用。
type Pool struct {
	opts Options

	// ctx 是工作池的根上下文，Shutdown 超时时由 cancel 关闭。
	ctx    context.Context
	cancel context.CancelFunc

	// queue 存放待执行任务，容量由 Options.QueueSize 决定。
	queue chan Task

	// senders 记录正在尝试入队的提交方。
	// Shutdown 先等待它们全部结束，再关闭 queue，
	// 避免出现 panic: send on closed channel。
	senders sync.WaitGroup

	// workers 记录 worker goroutine，workersDone 在它们全部退出后关闭。
	workers     sync.WaitGroup
	workersDone chan struct{}

	// errs 是失败任务的错误出口，缓冲满时丢弃，保证 worker 不被阻塞。
	errs     chan error
	errsOnce sync.Once

	// mu 保护 closed；关闭之后不再接收新任务。
	mu       sync.RWMutex
	closed   bool
	stopOnce sync.Once

	submitted atomic.Int64
	completed atomic.Int64
	failed    atomic.Int64
	retried   atomic.Int64
	active    atomic.Int64
}

// New 按给定选项创建并启动工作池。
func New(opts ...Option) *Pool {
	o := DefaultOptions()
	for _, fn := range opts {
		if fn == nil {
			continue
		}
		fn(&o)
	}
	if o.Workers <= 0 {
		o.Workers = 1
	}
	if o.QueueSize < 0 {
		o.QueueSize = 0
	}
	if o.MaxRetries < 0 {
		o.MaxRetries = 0
	}

	ctx, cancel := context.WithCancel(context.Background())
	p := &Pool{
		opts:        o,
		ctx:         ctx,
		cancel:      cancel,
		queue:       make(chan Task, o.QueueSize),
		workersDone: make(chan struct{}),
		errs:        make(chan error, errorBufferSize(o)),
	}

	p.workers.Add(o.Workers)
	for i := 0; i < o.Workers; i++ {
		go p.run()
	}
	go func() {
		p.workers.Wait()
		close(p.workersDone)
	}()

	return p
}

// errorBufferSize 决定错误通道的缓冲长度。
func errorBufferSize(o Options) int {
	size := o.Workers * 4
	if o.QueueSize > size {
		size = o.QueueSize
	}
	if size < 16 {
		size = 16
	}
	return size
}

// run 是一个 worker 的主循环：从队列取任务，直到队列关闭。
func (p *Pool) run() {
	defer p.workers.Done()
	for t := range p.queue {
		p.execute(t)
	}
}

// Submit 提交一个任务到工作池。
//
// 队列已满时会阻塞，直到有空闲位置、ctx 被取消或工作池关闭。
// 工作池关闭后返回 ErrPoolClosed。
func (p *Pool) Submit(ctx context.Context, t Task) error {
	if t == nil {
		return ErrNilTask
	}

	p.mu.RLock()
	if p.closed {
		p.mu.RUnlock()
		return ErrPoolClosed
	}
	p.senders.Add(1)
	p.mu.RUnlock()
	defer p.senders.Done()

	p.submitted.Add(1)
	select {
	case p.queue <- t:
		return nil
	case <-ctx.Done():
		p.submitted.Add(-1)
		return ctx.Err()
	}
}

// SubmitFunc 是 Submit 的便捷写法，直接提交一个函数。
func (p *Pool) SubmitFunc(ctx context.Context, fn func(ctx context.Context) error) error {
	if fn == nil {
		return ErrNilTask
	}
	return p.Submit(ctx, TaskFunc(fn))
}

// Running 报告工作池是否仍在接收任务。
//
// 新建的工作池返回 true；Shutdown 一旦被调用就立即返回 false，
// 即使此时队列里仍有待执行的任务、执行中的任务也尚未结束。
// 该方法并发安全，可与 Submit、Stats、Shutdown 同时调用。
func (p *Pool) Running() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return !p.closed
}

// Errors 返回只读的错误通道，用于收集重试耗尽后仍然失败的任务错误。
//
// 投递是非阻塞的：通道缓冲满时错误会被丢弃，但 Stats().Failed 仍会累加，
// 因此调用方应持续消费，否则会丢失细节。
// 正常关闭完成后该通道会被关闭；Shutdown 超时的情况下不会关闭。
func (p *Pool) Errors() <-chan error {
	return p.errs
}

// Shutdown 优雅关闭工作池。
//
// 流程是：停止接收新任务 -> 等待在途提交结束 -> 关闭队列 -> 等待 worker 排空退出。
// 执行中的任务不会被打断；如果 ctx 先到期，会返回包装了 ErrShutdownTimeout 的错误，
// 同时取消所有任务的 ctx 以促使它们尽快退出。
//
// Shutdown 可以重复调用，从第二次起只等待 worker 退出。
func (p *Pool) Shutdown(ctx context.Context) error {
	p.stopOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()

		// 等待所有在途的提交操作结束，此后不会再有 send，可以安全关闭队列。
		p.senders.Wait()
		close(p.queue)
	})

	select {
	case <-p.workersDone:
		p.cancel()
		p.closeErrors()
		return nil
	case <-ctx.Done():
		p.cancel()
		return fmt.Errorf("%w: %v", ErrShutdownTimeout, ctx.Err())
	}
}

// execute 执行单个任务，包含重试与结果统计。
func (p *Pool) execute(t Task) {
	p.active.Add(1)
	defer p.active.Add(-1)

	attempts := p.opts.MaxRetries + 1
	var err error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			p.retried.Add(1)
			if !p.waitBackoff() {
				// 工作池正在关闭，放弃剩余重试。
				break
			}
		}
		err = p.safeRun(t)
		if err == nil {
			p.completed.Add(1)
			return
		}
	}

	p.failed.Add(1)
	p.report(err)
}

// safeRun 在受控环境中执行一次任务尝试：派生超时 ctx、捕获 panic。
func (p *Pool) safeRun(t Task) (err error) {
	ctx := p.ctx
	if p.opts.TaskTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.opts.TaskTimeout)
		defer cancel()
	}

	defer func() {
		r := recover()
		if r == nil {
			return
		}
		if p.opts.OnPanic != nil {
			p.opts.OnPanic(r)
		}
		err = fmt.Errorf("worker: task panic: %v", r)
	}()

	return t.Run(ctx)
}

// waitBackoff 在两次尝试之间等待，返回 false 表示工作池正在关闭。
func (p *Pool) waitBackoff() bool {
	if p.opts.RetryBackoff <= 0 {
		return p.ctx.Err() == nil
	}

	timer := time.NewTimer(p.opts.RetryBackoff)
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case <-p.ctx.Done():
		return false
	}
}

// report 以非阻塞方式投递错误。
func (p *Pool) report(err error) {
	if err == nil {
		return
	}
	select {
	case p.errs <- err:
	default:
	}
}

// closeErrors 关闭错误通道，重复调用无副作用。
func (p *Pool) closeErrors() {
	p.errsOnce.Do(func() {
		close(p.errs)
	})
}
