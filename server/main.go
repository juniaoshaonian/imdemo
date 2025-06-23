package main

import (
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

const (
	// 缓冲区大小配置
	readBufferSize  = 4096
	writeBufferSize = 4096
	// 连接池配置
	maxConnections = 10000
	// 工作池配置
	workerPoolSize = 3000
)

var (
	activeConnections int64
	totalMessages     int64
	startTime         = time.Now()

	// 连接池
	connPool = &ConnectionPool{
		connections: make(map[net.Conn]*Connection),
		mutex:       &sync.RWMutex{},
	}

	// 缓冲区对象池
	bufferPool = sync.Pool{
		New: func() interface{} {
			return make([]byte, readBufferSize)
		},
	}

	// 消息对象池
	messagePool = sync.Pool{
		New: func() interface{} {
			return &Message{
				Data: make([]byte, 0, 1024),
			}
		},
	}
)

// Connection 表示一个WebSocket连接
type Connection struct {
	conn        net.Conn
	readBuffer  []byte
	writeBuffer []byte
	lastActive  time.Time
}

// Message 表示一个消息
type Message struct {
	Data []byte
	Op   ws.OpCode
}

// ConnectionPool 连接池
type ConnectionPool struct {
	connections map[net.Conn]*Connection
	mutex       *sync.RWMutex
}

func (cp *ConnectionPool) Add(conn net.Conn, wsConn *Connection) {
	cp.mutex.Lock()
	defer cp.mutex.Unlock()
	cp.connections[conn] = wsConn
}

func (cp *ConnectionPool) Remove(conn net.Conn) {
	cp.mutex.Lock()
	defer cp.mutex.Unlock()
	delete(cp.connections, conn)
}

func (cp *ConnectionPool) Get(conn net.Conn) (*Connection, bool) {
	cp.mutex.RLock()
	defer cp.mutex.RUnlock()
	wsConn, exists := cp.connections[conn]
	return wsConn, exists
}

func (cp *ConnectionPool) Count() int {
	cp.mutex.RLock()
	defer cp.mutex.RUnlock()
	return len(cp.connections)
}

// 工作池
type WorkerPool struct {
	workers    int
	jobQueue   chan func()
	workerPool chan chan func()
	quit       chan bool
}

func NewWorkerPool(workers int) *WorkerPool {
	return &WorkerPool{
		workers:    workers,
		jobQueue:   make(chan func(), workers*2),
		workerPool: make(chan chan func(), workers),
		quit:       make(chan bool),
	}
}

func (wp *WorkerPool) Start() {
	for i := 0; i < wp.workers; i++ {
		worker := NewWorker(wp.workerPool, wp.quit)
		worker.Start()
	}

	go wp.dispatch()
}

func (wp *WorkerPool) Stop() {
	close(wp.quit)
}

func (wp *WorkerPool) Submit(job func()) {
	wp.jobQueue <- job
}

func (wp *WorkerPool) dispatch() {
	for {
		select {
		case job := <-wp.jobQueue:
			worker := <-wp.workerPool
			worker <- job
		case <-wp.quit:
			return
		}
	}
}

type Worker struct {
	workerPool chan chan func()
	jobChannel chan func()
	quit       chan bool
}

func NewWorker(workerPool chan chan func(), quit chan bool) Worker {
	return Worker{
		workerPool: workerPool,
		jobChannel: make(chan func()),
		quit:       quit,
	}
}

func (w Worker) Start() {
	go func() {
		for {
			w.workerPool <- w.jobChannel
			select {
			case job := <-w.jobChannel:
				job()
			case <-w.quit:
				return
			}
		}
	}()
}

var workerPool *WorkerPool

func main() {
	// 初始化工作池
	workerPool = NewWorkerPool(workerPoolSize)
	workerPool.Start()
	defer workerPool.Stop()

	ln, err := net.Listen("tcp", ":50051")
	if err != nil {
		log.Fatalf("监听端口失败: %v", err)
	}
	defer ln.Close()
	log.Println("WebSocket服务端启动 :50051")

	// 启动统计监控
	go monitorStats()

	// 启动连接清理协程
	go cleanupConnections()

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("接受连接失败: %v", err)
			continue
		}

		// 检查连接池是否已满
		if connPool.Count() >= maxConnections {
			conn.Close()
			log.Printf("连接池已满，拒绝新连接")
			continue
		}

		go handleConnection(conn)
	}
}

func handleConnection(conn net.Conn) {
	atomic.AddInt64(&activeConnections, 1)
	defer func() {
		atomic.AddInt64(&activeConnections, -1)
		connPool.Remove(conn)
		conn.Close()
	}()

	// 升级为WebSocket连接
	_, err := ws.Upgrade(conn)
	if err != nil {
		log.Printf("升级WebSocket失败: %v", err)
		return
	}

	// 创建连接对象并添加到连接池
	wsConn := &Connection{
		conn:        conn,
		readBuffer:  bufferPool.Get().([]byte),
		writeBuffer: bufferPool.Get().([]byte),
		lastActive:  time.Now(),
	}
	connPool.Add(conn, wsConn)

	// 归还缓冲区到池中
	defer func() {
		bufferPool.Put(wsConn.readBuffer)
		bufferPool.Put(wsConn.writeBuffer)
	}()

	for {
		// 更新最后活跃时间
		wsConn.lastActive = time.Now()

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

		// 使用工作池处理消息
		workerPool.Submit(func() {
			processMessage(conn, op)
		})
	}
}

func processMessage(conn net.Conn, op ws.OpCode) {
	// 从对象池获取消息对象
	msg := messagePool.Get().(*Message)
	defer messagePool.Put(msg)

	// 重置消息对象
	msg.Data = msg.Data[:0]
	msg.Op = op

	// 构造响应数据
	msg.Data = append(msg.Data, []byte("OK")...)

	// 返回响应
	if err := wsutil.WriteServerMessage(conn, op, msg.Data); err != nil {
		log.Printf("发送失败: %v", err)
		return
	}
}

func cleanupConnections() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		connPool.mutex.Lock()
		for conn, wsConn := range connPool.connections {
			// 清理超过5分钟未活跃的连接
			if time.Since(wsConn.lastActive) > 5*time.Minute {
				conn.Close()
				delete(connPool.connections, conn)
			}
		}
		connPool.mutex.Unlock()
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
		poolCount := connPool.Count()

		log.Printf("统计 - 活跃连接: %d, 连接池大小: %d, 总消息: %d, 吞吐量: %.2f 消息/秒",
			active, poolCount, total, throughput)
	}
}
