package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

type PerformanceMetrics struct {
	TotalMessages    int64
	TimeoutMessages  int64
	ErrorMessages    int64
	ConnectionErrors int64
	SendErrors       int64
	ReceiveErrors    int64
	ResponseTimes    []time.Duration
	mu               sync.RWMutex
}

func (pm *PerformanceMetrics) AddResponseTime(duration time.Duration) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.ResponseTimes = append(pm.ResponseTimes, duration)
}

func (pm *PerformanceMetrics) GetPercentile(percentile float64) time.Duration {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	if len(pm.ResponseTimes) == 0 {
		return 0
	}

	// 复制并排序
	times := make([]time.Duration, len(pm.ResponseTimes))
	copy(times, pm.ResponseTimes)
	sort.Slice(times, func(i, j int) bool {
		return times[i] < times[j]
	})

	index := int(float64(len(times)-1) * percentile / 100)
	return times[index]
}

func main() {
	// 定义命令行参数
	numClients := flag.Int("clients", 1000, "客户端连接数量")
	messagesPerSecond := flag.Int("mps", 1, "每秒发送消息数量")
	testDuration := flag.Duration("duration", 1*time.Minute, "测试持续时间")
	connectionTimeout := flag.Duration("conn-timeout", 10*time.Second, "连接超时时间")
	responseTimeout := flag.Duration("resp-timeout", 1*time.Second, "响应超时时间")
	rampUpTime := flag.Duration("ramp-up", 10*time.Second, "连接建立阶段时间")
	messageSize := flag.Int("msg-size", 100, "消息大小(字节)")
	serverURL := flag.String("server", "172.17.0.12:50051", "服务器地址")
	flag.Parse()

	if *numClients <= 0 {
		log.Fatalf("无效的客户端数量: %d (必须大于0)", *numClients)
	}
	if *messagesPerSecond <= 0 {
		log.Fatalf("无效的每秒消息数量: %d (必须大于0)", *messagesPerSecond)
	}
	if *testDuration <= 0 {
		log.Fatalf("无效的测试持续时间: %v", *testDuration)
	}

	var wg sync.WaitGroup
	metrics := &PerformanceMetrics{}
	u := url.URL{Scheme: "ws", Host: *serverURL, Path: "/ws"}

	// 创建上下文，设置测试持续时间
	ctx, cancel := context.WithTimeout(context.Background(), *testDuration)
	defer cancel()

	// 创建通道用于优雅关闭
	done := make(chan struct{})

	// 实时监控goroutine
	monitorCtx, monitorCancel := context.WithCancel(context.Background())
	defer monitorCancel()

	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-monitorCtx.Done():
				return
			case <-ticker.C:
				total := atomic.LoadInt64(&metrics.TotalMessages)
				timeouts := atomic.LoadInt64(&metrics.TimeoutMessages)
				errors := atomic.LoadInt64(&metrics.ErrorMessages)
				connErrors := atomic.LoadInt64(&metrics.ConnectionErrors)

				if total > 0 {
					log.Printf("实时统计 - 总消息: %d, 超时: %d (%.2f%%), 错误: %d (%.2f%%), 连接错误: %d",
						total, timeouts, float64(timeouts)/float64(total)*100,
						errors, float64(errors)/float64(total)*100, connErrors)
				}
			}
		}
	}()

	// 连接建立阶段
	log.Printf("开始建立 %d 个连接，建立时间: %v", *numClients, *rampUpTime)
	connectionInterval := *rampUpTime / time.Duration(*numClients)

	for i := 0; i < *numClients; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// 延迟建立连接，实现连接建立阶段
			time.Sleep(connectionInterval * time.Duration(id))

			// 建立 WebSocket 连接
			connCtx, connCancel := context.WithTimeout(context.Background(), *connectionTimeout)
			conn, _, _, err := ws.Dial(connCtx, u.String())
			connCancel()

			if err != nil {
				atomic.AddInt64(&metrics.ConnectionErrors, 1)
				log.Printf("[%d] 连接失败: %v", id, err)
				return
			}
			defer conn.Close()

			// 计算消息间隔时间
			interval := time.Duration(1000 / *messagesPerSecond) * time.Millisecond
			ticker := time.NewTicker(interval)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					// 测试时间结束，优雅退出
					return
				case <-ticker.C:
					// 创建指定大小的消息
					baseMsg := fmt.Sprintf("客户端 %d: %v", id, time.Now().UnixNano())
					// 计算需要填充的字符数以达到指定大小
					remainingBytes := *messageSize - len(baseMsg)
					if remainingBytes > 0 {
						// 用空格填充到指定大小
						for j := 0; j < remainingBytes; j++ {
							baseMsg += " "
						}
					} else if remainingBytes < 0 {
						// 如果超过指定大小，截断
						baseMsg = baseMsg[:*messageSize]
					}

					// 记录发送开始时间
					startTime := time.Now()

					// 发送消息
					if err := wsutil.WriteClientText(conn, []byte(baseMsg)); err != nil {
						atomic.AddInt64(&metrics.SendErrors, 1)
						atomic.AddInt64(&metrics.ErrorMessages, 1)
						log.Printf("[%d] 发送失败: %v", id, err)
						return
					}

					// 读取服务端响应
					_, err := wsutil.ReadServerText(conn)
					if err != nil {
						atomic.AddInt64(&metrics.ReceiveErrors, 1)
						atomic.AddInt64(&metrics.ErrorMessages, 1)
						log.Printf("[%d] 接收失败: %v", id, err)
						return
					}

					// 计算响应时间
					responseTime := time.Since(startTime)
					atomic.AddInt64(&metrics.TotalMessages, 1)
					metrics.AddResponseTime(responseTime)

					// 检查是否超过响应超时时间
					if responseTime > *responseTimeout {
						atomic.AddInt64(&metrics.TimeoutMessages, 1)
					}
				}
			}
		}(i)
	}

	// 等待所有goroutine完成
	go func() {
		wg.Wait()
		close(done)
	}()

	// 等待测试完成或超时
	select {
	case <-ctx.Done():
		log.Println("测试时间结束，开始优雅关闭...")
	case <-done:
		log.Println("所有客户端完成")
	}

	// 等待所有goroutine完全停止
	<-done

	// 生成详细报告
	generateReport(metrics, *numClients, *messagesPerSecond, *testDuration, *messageSize, *serverURL)
}

func generateReport(metrics *PerformanceMetrics, numClients, mps int, duration time.Duration, msgSize int, serverURL string) {
	total := atomic.LoadInt64(&metrics.TotalMessages)
	timeouts := atomic.LoadInt64(&metrics.TimeoutMessages)
	errors := atomic.LoadInt64(&metrics.ErrorMessages)
	connErrors := atomic.LoadInt64(&metrics.ConnectionErrors)
	sendErrors := atomic.LoadInt64(&metrics.SendErrors)
	receiveErrors := atomic.LoadInt64(&metrics.ReceiveErrors)

	// 计算吞吐量
	throughput := float64(total) / duration.Seconds()

	// 计算响应时间统计
	p50 := metrics.GetPercentile(50)
	p90 := metrics.GetPercentile(90)
	p95 := metrics.GetPercentile(95)
	p99 := metrics.GetPercentile(99)

	// 生成报告
	report := fmt.Sprintf(`WebSocket 压测报告
==================
测试配置:
- 服务器地址: %s
- 客户端数量: %d
- 每秒消息数: %d
- 测试持续时间: %v
- 消息大小: %d 字节

测试结果:
- 总消息数: %d
- 成功消息数: %d
- 超时消息数: %d (%.2f%%)
- 错误消息数: %d (%.2f%%)
- 连接错误数: %d
- 发送错误数: %d
- 接收错误数: %d

性能指标:
- 吞吐量: %.2f 消息/秒
- P50 响应时间: %v
- P90 响应时间: %v
- P95 响应时间: %v
- P99 响应时间: %v

测试时间: %s
`,
		serverURL, numClients, mps, duration, msgSize,
		total, total-errors, timeouts, float64(timeouts)/float64(total)*100,
		errors, float64(errors)/float64(total)*100, connErrors, sendErrors, receiveErrors,
		throughput, p50, p90, p95, p99,
		time.Now().Format("2006-01-02 15:04:05"))

	// 写入文件
	filename := fmt.Sprintf("websocket_loadtest_%s.txt", time.Now().Format("20060102_150405"))
	err := os.WriteFile(filename, []byte(report), 0644)
	if err != nil {
		log.Printf("写入报告文件失败: %v", err)
	} else {
		log.Printf("测试报告已写入文件: %s", filename)
	}

	fmt.Println(report)
}
