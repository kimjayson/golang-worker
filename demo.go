package task

import (
	"context"
	"sync"
	"fmt"
)

type LoadDemo struct{
	channelID int
}

func main (ctx context.Context) {

	p := NewWorker(ctx,5) //工作池协程数

	var wg sync.WaitGroup
	wg.Add(100) //任务数可以按各个维度

	channelIDs := []int{}
	for i:=0;i<100;i++{
		channelIDs = append(channelIDs,i)
	}
	for _, id := range channelIDs { //渠道ID

		ld := LoadDemo{
			channelID: id,
		}

		go func(){
			p.Run(&lc)
			wg.Done()
		}()
	}
	wg.Wait()

	p.Shutdown()
}

//任务逻辑
func (l *LoadDemo) Task(ctx context.Context) {
	fmt.Println(l.channelID)
}
