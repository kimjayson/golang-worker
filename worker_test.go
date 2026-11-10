package worker

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// receiveErr 在限定时间内从错误通道取一条错误。
func receiveErr(t *testing.T, p *Pool, d time.Duration) error {
	t.Helper()
	select {
	case err, ok := <-p.Errors():
		if !ok {
			t.Fatal("错误通道已被关闭")
		}
		return err
	case <-time.After(d):
		t.Fatal("等待任务错误超时")
		return nil
	}
}

func TestPoolRunsAllTasks(t *testing.T) {
	p := New(WithWorkers(5), WithQueueSize(10))

	var done atomic.Int64
	for i := 0; i < 100; i++ {
		err := p.Submit(context.Background(), TaskFunc(func(ctx context.Context) error {
			done.Add(1)
			return nil
		}))
		if err != nil {
			t.Fatalf("提交任务失败: %v", err)
		}
	}

	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("关闭工作池失败: %v", err)
	}

	if got := done.Load(); got != 100 {
		t.Fatalf("期望执行 100 个任务, 实际 %d", got)
	}

	st := p.Stats()
	if st.Submitted != 100 || st.Completed != 100 || st.Failed != 0 || st.Active != 0 || st.Queued != 0 {
		t.Fatalf("统计不符合预期: %+v", st)
	}
}

func TestConcurrencyLimit(t *testing.T) {
	const workers = 3
	p := New(WithWorkers(workers), WithQueueSize(50))

	var current, peak atomic.Int64
	for i := 0; i < 30; i++ {
		err := p.Submit(context.Background(), TaskFunc(func(ctx context.Context) error {
			n := current.Add(1)
			for {
				m := peak.Load()
				if n <= m || peak.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			current.Add(-1)
			return nil
		}))
		if err != nil {
			t.Fatalf("提交任务失败: %v", err)
		}
	}

	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("关闭工作池失败: %v", err)
	}

	if got := peak.Load(); got > workers {
		t.Fatalf("并发数超过上限: 峰值 %d, 上限 %d", got, workers)
	}
	if got := peak.Load(); got == 0 {
		t.Fatal("没有任何任务被执行")
	}
}

func TestShutdownWaitsForQueuedTasks(t *testing.T) {
	p := New(WithWorkers(2), WithQueueSize(20))

	var done atomic.Int64
	for i := 0; i < 20; i++ {
		err := p.Submit(context.Background(), TaskFunc(func(ctx context.Context) error {
			time.Sleep(5 * time.Millisecond)
			done.Add(1)
			return nil
		}))
		if err != nil {
			t.Fatalf("提交任务失败: %v", err)
		}
	}

	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("关闭工作池失败: %v", err)
	}

	if got := done.Load(); got != 20 {
		t.Fatalf("关闭时应排空队列, 期望 20 个任务完成, 实际 %d", got)
	}
}

func TestSubmitAfterShutdown(t *testing.T) {
	p := New(WithWorkers(1))
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("关闭工作池失败: %v", err)
	}

	err := p.Submit(context.Background(), TaskFunc(func(ctx context.Context) error {
		return nil
	}))
	if !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("期望 ErrPoolClosed, 实际 %v", err)
	}
}

func TestSubmitNilTask(t *testing.T) {
	p := New(WithWorkers(1))
	defer p.Shutdown(context.Background())

	if err := p.Submit(context.Background(), nil); !errors.Is(err, ErrNilTask) {
		t.Fatalf("期望 ErrNilTask, 实际 %v", err)
	}
	if err := p.SubmitFunc(context.Background(), nil); !errors.Is(err, ErrNilTask) {
		t.Fatalf("期望 ErrNilTask, 实际 %v", err)
	}
}

func TestSubmitCanceledWhileQueueFull(t *testing.T) {
	// 无缓冲队列 + 单 worker: 第二个任务必然阻塞在提交阶段。
	p := New(WithWorkers(1))

	release := make(chan struct{})
	err := p.Submit(context.Background(), TaskFunc(func(ctx context.Context) error {
		<-release
		return nil
	}))
	if err != nil {
		t.Fatalf("提交首个任务失败: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	err = p.Submit(ctx, TaskFunc(func(ctx context.Context) error {
		return nil
	}))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("期望 context.DeadlineExceeded, 实际 %v", err)
	}

	close(release)
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("关闭工作池失败: %v", err)
	}

	st := p.Stats()
	if st.Submitted != 1 || st.Completed != 1 {
		t.Fatalf("被取消的提交不应计入统计: %+v", st)
	}
}

func TestRetrySucceeds(t *testing.T) {
	p := New(WithWorkers(1), WithMaxRetries(2), WithRetryBackoff(time.Millisecond))

	var calls atomic.Int64
	err := p.Submit(context.Background(), TaskFunc(func(ctx context.Context) error {
		if calls.Add(1) < 3 {
			return errors.New("暂时不可用")
		}
		return nil
	}))
	if err != nil {
		t.Fatalf("提交任务失败: %v", err)
	}

	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("关闭工作池失败: %v", err)
	}

	if got := calls.Load(); got != 3 {
		t.Fatalf("期望执行 3 次(1 次 + 2 次重试), 实际 %d", got)
	}

	st := p.Stats()
	if st.Completed != 1 || st.Failed != 0 || st.Retried != 2 {
		t.Fatalf("统计不符合预期: %+v", st)
	}
}

func TestRetryExhausted(t *testing.T) {
	p := New(WithWorkers(1), WithMaxRetries(1), WithRetryBackoff(time.Millisecond))

	var calls atomic.Int64
	err := p.Submit(context.Background(), TaskFunc(func(ctx context.Context) error {
		calls.Add(1)
		return errors.New("永远失败")
	}))
	if err != nil {
		t.Fatalf("提交任务失败: %v", err)
	}

	gotErr := receiveErr(t, p, time.Second)
	if gotErr == nil || !strings.Contains(gotErr.Error(), "永远失败") {
		t.Fatalf("错误通道内容不符合预期: %v", gotErr)
	}

	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("关闭工作池失败: %v", err)
	}

	if got := calls.Load(); got != 2 {
		t.Fatalf("期望执行 2 次(1 次 + 1 次重试), 实际 %d", got)
	}

	st := p.Stats()
	if st.Failed != 1 || st.Completed != 0 || st.Retried != 1 {
		t.Fatalf("统计不符合预期: %+v", st)
	}
}

func TestPanicRecovered(t *testing.T) {
	var panicked atomic.Bool
	p := New(WithWorkers(1), WithPanicHandler(func(recovered any) {
		panicked.Store(true)
	}))

	err := p.Submit(context.Background(), TaskFunc(func(ctx context.Context) error {
		panic("任务炸了")
	}))
	if err != nil {
		t.Fatalf("提交任务失败: %v", err)
	}

	gotErr := receiveErr(t, p, time.Second)
	if !strings.Contains(gotErr.Error(), "panic") {
		t.Fatalf("期望 panic 被转换成错误, 实际 %v", gotErr)
	}

	// worker 不应被 panic 打死，后续任务仍能执行。
	var after atomic.Int64
	if err := p.Submit(context.Background(), TaskFunc(func(ctx context.Context) error {
		after.Add(1)
		return nil
	})); err != nil {
		t.Fatalf("panic 之后提交任务失败: %v", err)
	}

	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("关闭工作池失败: %v", err)
	}

	if !panicked.Load() {
		t.Fatal("OnPanic 回调未被触发")
	}
	if after.Load() != 1 {
		t.Fatal("panic 之后 worker 未能继续执行任务")
	}
	if st := p.Stats(); st.Failed != 1 || st.Completed != 1 {
		t.Fatalf("统计不符合预期: %+v", st)
	}
}

func TestTaskTimeout(t *testing.T) {
	p := New(WithWorkers(1), WithTaskTimeout(20*time.Millisecond))

	var deadline atomic.Bool
	err := p.Submit(context.Background(), TaskFunc(func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			deadline.Store(errors.Is(ctx.Err(), context.DeadlineExceeded))
			return ctx.Err()
		case <-time.After(2 * time.Second):
			return nil
		}
	}))
	if err != nil {
		t.Fatalf("提交任务失败: %v", err)
	}

	gotErr := receiveErr(t, p, time.Second)
	if !errors.Is(gotErr, context.DeadlineExceeded) {
		t.Fatalf("期望 DeadlineExceeded, 实际 %v", gotErr)
	}

	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("关闭工作池失败: %v", err)
	}

	if !deadline.Load() {
		t.Fatal("任务 ctx 未收到超时信号")
	}
	if st := p.Stats(); st.Failed != 1 {
		t.Fatalf("统计不符合预期: %+v", st)
	}
}

func TestShutdownTimeout(t *testing.T) {
	p := New(WithWorkers(1))

	release := make(chan struct{})
	err := p.Submit(context.Background(), TaskFunc(func(ctx context.Context) error {
		<-release
		return nil
	}))
	if err != nil {
		t.Fatalf("提交任务失败: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	err = p.Shutdown(ctx)
	if !errors.Is(err, ErrShutdownTimeout) {
		t.Fatalf("期望 ErrShutdownTimeout, 实际 %v", err)
	}

	// 放行任务后再次关闭应当成功。
	close(release)
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("二次关闭失败: %v", err)
	}
}

func TestErrorsChannelClosedAfterShutdown(t *testing.T) {
	p := New(WithWorkers(1))
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("关闭工作池失败: %v", err)
	}

	select {
	case _, ok := <-p.Errors():
		if ok {
			t.Fatal("关闭后错误通道应当是关闭状态")
		}
	case <-time.After(time.Second):
		t.Fatal("关闭后错误通道未关闭")
	}
}

func TestShutdownIsIdempotent(t *testing.T) {
	p := New(WithWorkers(2), WithQueueSize(4))

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.Shutdown(context.Background()); err != nil {
				t.Errorf("并发关闭失败: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestErrorsAggregatedFromMultipleTasks(t *testing.T) {
	p := New(WithWorkers(4), WithQueueSize(8))

	const failures = 5
	for i := 0; i < failures; i++ {
		err := p.Submit(context.Background(), TaskFunc(func(ctx context.Context) error {
			return errors.New("批量失败")
		}))
		if err != nil {
			t.Fatalf("提交任务失败: %v", err)
		}
	}

	for i := 0; i < failures; i++ {
		receiveErr(t, p, time.Second)
	}

	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("关闭工作池失败: %v", err)
	}

	if st := p.Stats(); st.Failed != failures {
		t.Fatalf("统计不符合预期: %+v", st)
	}
}
