package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/url"
	"sync"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

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
	u := url.URL{Scheme: "ws", Host: "172.17.0.12:50051", Path: "/ws"}

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

			for range ticker.C {
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
				log.Printf("[%d] 收到响应: %s", id, res)
			}
		}(i)
	}
	wg.Wait()
	fmt.Println("所有客户端完成")
}
