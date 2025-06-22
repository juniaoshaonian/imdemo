package main

import (
	"log"
	"net"
	"sync/atomic"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

var (
	activeConnections int64
	totalMessages     int64
	startTime         = time.Now()
)

func main() {
	ln, err := net.Listen("tcp", ":50051")
	if err != nil {
		log.Fatalf("监听端口失败: %v", err)
	}
	defer ln.Close()
	log.Println("WebSocket服务端启动 :50051")

	// 启动统计监控
	go monitorStats()

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("接受连接失败: %v", err)
			continue
		}

		go handleConnection(conn)
	}
}

func handleConnection(conn net.Conn) {
	atomic.AddInt64(&activeConnections, 1)
	defer func() {
		atomic.AddInt64(&activeConnections, -1)
		conn.Close()
	}()

	// 升级为WebSocket连接
	_, err := ws.Upgrade(conn)
	if err != nil {
		log.Printf("升级WebSocket失败: %v", err)
		return
	}

	for {
		// 读取客户端消息
		_, op, err := wsutil.ReadClientData(conn)
		if err != nil {
			// 只在非正常关闭时打印错误
			if err.Error() != "EOF" {
				log.Printf("读取失败: %v", err)
			}
			return
		}

		// 增加消息计数
		atomic.AddInt64(&totalMessages, 1)

		// 返回响应（移除日志打印以提高性能）
		if err := wsutil.WriteServerMessage(conn, op, []byte("OK")); err != nil {
			log.Printf("发送失败: %v", err)
			return
		}
	}
}

func monitorStats() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		active := atomic.LoadInt64(&activeConnections)
		total := atomic.LoadInt64(&totalMessages)
		duration := time.Since(startTime).Seconds()
		throughput := float64(total) / duration

		log.Printf("统计 - 活跃连接: %d, 总消息: %d, 吞吐量: %.2f 消息/秒",
			active, total, throughput)
	}
}
