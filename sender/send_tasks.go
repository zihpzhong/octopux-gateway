package sender

import (
	"log"
	"math/rand"
	"time"

	pfc "github.com/baishancloud/goperfcounter"
	cmodel "github.com/open-falcon/common/model"
	cutils "github.com/open-falcon/common/utils"
	nsema "github.com/toolkits/concurrent/semaphore"
	nlist "github.com/toolkits/container/list"

	"github.com/baishancloud/octopux-gateway/g"
)

func startSendTasks(server *g.ReceiverStatusManager) {
	cfg := g.Config()
	concurrent := cfg.Transfer.MaxConns * int32(len(cfg.Transfer.Cluster))
	go forward2TransferTask(SenderQueue, concurrent, server)
}

func forward2TransferTask(Q *nlist.SafeListLimited, concurrent int32, server *g.ReceiverStatusManager) {
	cfg := g.Config()
	batch := int(cfg.Transfer.Batch)
	maxConns := int64(cfg.Transfer.MaxConns)
	retry := int(cfg.Transfer.Retry)
	if retry < 1 {
		retry = 1
	}

	sema := nsema.NewSemaphore(int(concurrent))
	transNum := len(TransferHostnames)
	server.Add(1)
	defer server.Done()

	for {
		items := Q.PopBackBy(batch)
		count := len(items)
		if count == 0 {
			time.Sleep(time.Millisecond * 50)
			if server.IsRun() == false && Q.Len() == 0 {
				return
			}
			continue
		}

		transItems := make([]*cmodel.MetricValue, count)
		for i := 0; i < count; i++ {
			transItems[i] = convert(items[i].(*cmodel.MetaData))
		}

		sema.Acquire()
		go func(transItems []*cmodel.MetricValue, count int) {
			defer sema.Release()
			var err error
			start := time.Now()

			// 随机遍历transfer列表，直到数据发送成功 或者 遍历完;随机遍历，可以缓解慢transfer
			resp := &g.TransferResp{}
			sendOk := false

			for j := 0; j < retry && !sendOk; j++ {
				rint := rand.Int()
				for i := 0; i < transNum && !sendOk; i++ {
					idx := (i + rint) % transNum
					host := TransferHostnames[idx]
					addr := TransferMap[host]

					// 过滤掉建连缓慢的host, 否则会严重影响发送速率
					cc := pfc.GetCounterCount(host)
					if cc >= maxConns {
						continue
					}

					pfc.Counter(host, 1)
					err = SenderConnPools.Call(addr, "Transfer.Update", transItems, resp)
					pfc.Counter(host, -1)

					if err == nil {
						sendOk = true
						// statistics
						pfc.Meter("SWGWSendCnt"+host, int64(count))
					} else {
						// statistics
						pfc.Meter("SWGWSendFailCnt"+host, int64(count))
					}
				}
			}

			// statistics
			if !sendOk {
				if cfg.Debug {
					log.Printf("send to transfer fail, connpool:%v", SenderConnPools.Proc())
				}
				pfc.Meter("SWGWSendFail", int64(count))
			} else {
				pfc.Meter("SWGWSend", int64(count))
			}
			pfc.Histogram("SWGWSendTime", int64(time.Since(start)/time.Millisecond))
		}(transItems, count)
	}
}

func convert(v *cmodel.MetaData) *cmodel.MetricValue {
	return &cmodel.MetricValue{
		Metric:    v.Metric,
		Endpoint:  v.Endpoint,
		Timestamp: v.Timestamp,
		Step:      v.Step,
		Type:      v.CounterType,
		Tags:      cutils.SortedTags(v.Tags),
		Value:     v.Value,
	}
}
