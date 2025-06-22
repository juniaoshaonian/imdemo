package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

type ClientStats struct {
	ID           int
	TimeoutCount int64
	TotalCount   int64
}

func main() {
	// 定义命令行参数
	numClients := flag.Int("clients", 1000, "客户端连接数量")
	messagesPerSecond := flag.Int("mps", 1, "每秒发送消息数量")
	flag.Parse()
	if *numClients <= 0 {
		log.Fatalf("无效的客户端数量: %d (必须大于0)", *numClients)
	}
	if *messagesPerSecond <= 0 {
		log.Fatalf("无效的每秒消息数量: %d (必须大于0)", *messagesPerSecond)
	}

	var wg sync.WaitGroup
	var globalTimeoutCount int64
	var globalTotalCount int64
	u := url.URL{Scheme: "ws", Host: "172.17.0.12:50051", Path: "/ws"}

	// 创建上下文，1分钟后自动取消
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	// 创建通道用于优雅关闭
	done := make(chan struct{})

	for i := 0; i < *numClients; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// 建立 WebSocket 连接
			conn, _, _, err := ws.Dial(context.Background(), u.String())
			if err != nil {
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
					// 创建100字节的消息
					baseMsg := fmt.Sprintf("客户端 %d: %v", id, time.Now().UnixNano())
					// 计算需要填充的字符数以达到100字节
					remainingBytes := 100 - len(baseMsg)
					if remainingBytes > 0 {
						// 用空格填充到100字节
						for j := 0; j < remainingBytes; j++ {
							baseMsg += " "
						}
					} else if remainingBytes < 0 {
						// 如果超过100字节，截断到100字节
						baseMsg = baseMsg[:100]
					}

					// 记录发送开始时间
					startTime := time.Now()

					// 发送消息
					if err := wsutil.WriteClientText(conn, []byte(baseMsg)); err != nil {
						log.Printf("[%d] 发送失败: %v", id, err)
						return
					}

					// 读取服务端响应
					res, err := wsutil.ReadServerText(conn)
					if err != nil {
						log.Printf("[%d] 接收失败: %v", id, err)
						return
					}

					// 计算响应时间
					responseTime := time.Since(startTime)
					atomic.AddInt64(&globalTotalCount, 1)

					// 检查是否超过500ms
					if responseTime > 500*time.Millisecond {
						atomic.AddInt64(&globalTimeoutCount, 1)
						log.Printf("[%d] 响应超时: %v, 响应: %s", id, responseTime, res)
					} else {
						log.Printf("[%d] 响应时间: %v, 响应: %s", id, responseTime, res)
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

	// 将结果写入文件
	result := fmt.Sprintf("测试结果:\n总消息数: %d\n超时消息数: %d\n超时率: %.2f%%\n",
		globalTotalCount, globalTimeoutCount,
		float64(globalTimeoutCount)/float64(globalTotalCount)*100)

	filename := fmt.Sprintf("test_result_%s.txt", time.Now().Format("20060102_150405"))
	err := os.WriteFile(filename, []byte(result), 0644)
	if err != nil {
		log.Printf("写入结果文件失败: %v", err)
	} else {
		log.Printf("测试结果已写入文件: %s", filename)
	}

	fmt.Println(result)
}
