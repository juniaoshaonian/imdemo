package main

import (
	"log"
	"net"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

func main() {
	ln, err := net.Listen("tcp", ":50051")
	if err != nil {
		log.Fatalf("监听端口失败: %v", err)
	}
	defer ln.Close()
	log.Println("WebSocket服务端启动 :50051")

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
	defer conn.Close()

	// 升级为WebSocket连接
	_, err := ws.Upgrade(conn)
	if err != nil {
		log.Printf("升级WebSocket失败: %v", err)
		return
	}

	for {
		// 读取客户端消息
		msg, op, err := wsutil.ReadClientData(conn)
		if err != nil {
			log.Printf("读取失败: %v", err)
			return
		}
		log.Printf("收到消息: %s", msg)

		// 返回 "OK" 响应
		if err := wsutil.WriteServerMessage(conn, op, []byte("OK")); err != nil {
			log.Printf("发送失败: %v", err)
			return
		}
	}
}
