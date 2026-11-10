// Command demo 演示 golang-worker 的常见用法。
//
// 运行示例：
//
//	go run ./cmd/demo -n 100 -w 5 -fail 20
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	worker "github.com/kimjayson/golang-worker"
)

func main() {
	total := flag.Int("n", 100, "任务总数")
	workers := flag.Int("w", 5, "并发协程数")
	failRate := flag.Int("fail", 10, "模拟失败率, 取值 0-100")
	flag.Parse()

	if *total <= 0 {
		log.Fatal("任务总数必须大于 0")
	}

	// Ctrl+C 时取消 ctx，工作池会停止接收新任务。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool := worker.New(
		worker.WithWorkers(*workers),
		worker.WithQueueSize(*workers*2),
		worker.WithTaskTimeout(3*time.Second),
		worker.WithMaxRetries(2),
		worker.WithRetryBackoff(50*time.Millisecond),
		worker.WithPanicHandler(func(recovered any) {
			log.Printf("[panic] 任务发生 panic: %v", recovered)
		}),
	)

	// 持续消费错误通道，避免错误被丢弃。
	var drain sync.WaitGroup
	drain.Add(1)
	go func() {
		defer drain.Done()
		for err := range pool.Errors() {
			log.Printf("[error] %v", err)
		}
	}()

	// 限制同时提交的协程数，避免一次性拉起过多 goroutine。
	var submit sync.WaitGroup
	sem := make(chan struct{}, *workers)
	for i := 1; i <= *total; i++ {
		id := i
		submit.Add(1)
		go func() {
			defer submit.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			err := pool.Submit(ctx, worker.TaskFunc(func(ctx context.Context) error {
				return handle(ctx, id, *failRate)
			}))
			if err != nil {
				log.Printf("[submit] 任务 %d 提交失败: %v", id, err)
			}
		}()
	}
	submit.Wait()

	// 优雅关闭：等待队列与执行中的任务完成，最多等待 10s。
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := pool.Shutdown(shutdownCtx); err != nil {
		if errors.Is(err, worker.ErrShutdownTimeout) {
			log.Printf("[shutdown] 等待超时，已放弃剩余任务: %v", err)
		} else {
			log.Printf("[shutdown] 关闭异常: %v", err)
		}
	}
	drain.Wait()

	st := pool.Stats()
	fmt.Println("---------- 统计 ----------")
	fmt.Printf("协程数   : %d\n", st.Workers)
	fmt.Printf("已提交   : %d\n", st.Submitted)
	fmt.Printf("已完成   : %d\n", st.Completed)
	fmt.Printf("已失败   : %d\n", st.Failed)
	fmt.Printf("重试次数 : %d\n", st.Retried)
	fmt.Printf("成功率   : %.1f%%\n", successRate(st))
}

// handle 模拟一个会随机失败的业务任务。
func handle(ctx context.Context, id, failRate int) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Duration(rand.Intn(50)) * time.Millisecond):
	}

	if failRate > 0 && rand.Intn(100) < failRate {
		return fmt.Errorf("任务 %d 处理失败", id)
	}
	return nil
}

// successRate 计算成功率，避免除零。
func successRate(st worker.Stats) float64 {
	if st.Submitted == 0 {
		return 0
	}
	return float64(st.Completed) / float64(st.Submitted) * 100
}
