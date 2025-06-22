package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"log"
	"net/url"
	"sync"
	"time"
)

func main() {
	// 定义命令行参数
	numClients := flag.Int("clients", 1000, "客户端连接数量")
	flag.Parse()
	if *numClients <= 0 {
		log.Fatalf("无效的客户端数量: %d (必须大于0)", *numClients)
	}
	var wg sync.WaitGroup
	u := url.URL{Scheme: "ws", Host: ":50051", Path: "/ws"}

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

			ticker := time.NewTicker(1 * time.Second)
			defer ticker.Stop()

			for range ticker.C {
				msg := fmt.Sprintf("客户端 %d: %v", id, time.Now().Unix())

				// 发送消息
				if err := wsutil.WriteClientText(conn, []byte(msg)); err != nil {
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
