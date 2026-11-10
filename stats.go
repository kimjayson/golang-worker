package worker

// Stats 是工作池在某一时刻的统计快照。
//
// 一次任务的重试不会重复计入 Submitted：
// 提交一次记一次，重试只累加 Retried。
type Stats struct {
	// Submitted 是成功进入队列的任务数。
	Submitted int64

	// Completed 是执行成功（含重试后成功）的任务数。
	Completed int64

	// Failed 是重试耗尽后仍然失败的任务数。
	Failed int64

	// Retried 是实际发生的重试次数。
	Retried int64

	// Active 是当前正在执行的任务数。
	Active int64

	// Queued 是当前仍在队列中等待的任务数。
	Queued int64

	// Workers 是配置的并发协程数。
	Workers int
}

// Stats 返回当前统计快照，可安全并发调用。
func (p *Pool) Stats() Stats {
	return Stats{
		Submitted: p.submitted.Load(),
		Completed: p.completed.Load(),
		Failed:    p.failed.Load(),
		Retried:   p.retried.Load(),
		Active:    p.active.Load(),
		Queued:    int64(len(p.queue)),
		Workers:   p.opts.Workers,
	}
}
