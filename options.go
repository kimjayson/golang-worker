package worker

import "time"

// Options 描述工作池的全部可配置项。
type Options struct {
	// Workers 是并发执行任务的 goroutine 数量。
	// 小于等于 0 时按 1 处理。
	Workers int

	// QueueSize 是待执行任务的队列容量。
	// 0 表示无缓冲：提交方会一直阻塞到有空闲 worker 接手任务。
	QueueSize int

	// TaskTimeout 是单个任务（单次尝试）的执行超时时间。
	// 0 表示不限制。
	// 超时只会取消任务收到的 ctx，不会强制杀死 goroutine。
	TaskTimeout time.Duration

	// MaxRetries 是任务失败后的最大重试次数，不包含首次执行。
	MaxRetries int

	// RetryBackoff 是两次尝试之间的等待时间，0 表示立即重试。
	RetryBackoff time.Duration

	// OnPanic 在任务发生 panic 并被 recover 之后被调用，可以为 nil。
	// 回调同样是同步执行的，不要在里面做耗时操作。
	OnPanic func(recovered any)
}

// DefaultOptions 返回默认配置：单协程、无缓冲队列、不重试、不超时。
func DefaultOptions() Options {
	return Options{
		Workers:      1,
		QueueSize:    0,
		MaxRetries:   0,
		RetryBackoff: 0,
	}
}

// Option 以函数式选项的方式覆盖默认配置。
type Option func(*Options)

// WithWorkers 设置并发协程数。
func WithWorkers(n int) Option {
	return func(o *Options) {
		if n > 0 {
			o.Workers = n
		}
	}
}

// WithQueueSize 设置任务队列容量。
func WithQueueSize(n int) Option {
	return func(o *Options) {
		if n >= 0 {
			o.QueueSize = n
		}
	}
}

// WithTaskTimeout 设置单任务执行超时。
func WithTaskTimeout(d time.Duration) Option {
	return func(o *Options) {
		o.TaskTimeout = d
	}
}

// WithMaxRetries 设置失败后的最大重试次数。
func WithMaxRetries(n int) Option {
	return func(o *Options) {
		if n > 0 {
			o.MaxRetries = n
		}
	}
}

// WithRetryBackoff 设置两次尝试之间的等待时间。
func WithRetryBackoff(d time.Duration) Option {
	return func(o *Options) {
		o.RetryBackoff = d
	}
}

// WithPanicHandler 注册 panic 回调。
func WithPanicHandler(fn func(recovered any)) Option {
	return func(o *Options) {
		o.OnPanic = fn
	}
}
