package backend

import (
	"time"

	rd "github.com/gomodule/redigo/redis"
	"github.com/jmuyuyang/queue_proxy/config"
)

const (
	REDIS_CONN_READ_TIMEOUT = 30 //读超时10s,用于brpop操作
)

type redisQueue struct {
	pool   *rd.Pool
	config config.QueueAttrConfig
	topic  string
	active bool
}

type RedisQueueProducer struct {
	redisQueue
}

type RedisPipelineProducer struct {
	conn          rd.Conn
	topic         string
	curBufferSize int32
}

/*
type RedisQueueConsumer struct {
	redisQueue
	clientID         [ClientIDLength]byte
	options          *Options
	exitChan         chan int
	closeChan        chan int
	MessageChan      chan *Message
	inFlightPQ       inFlightPqueue
	inFlightMutex    sync.Mutex
	inFlightMessages map[MessageID]*Message
	waitGroup        util.WaitGroupWrapper
	workerNum        int
}
*/

func createRedisQueuePool(host string, connTimeout, idleTimeout time.Duration, maxConn int) *rd.Pool {
	pool := &rd.Pool{
		MaxIdle:     maxConn,
		IdleTimeout: idleTimeout,
		Dial: func() (rd.Conn, error) {
			c, err := rd.Dial("tcp", host,
				rd.DialConnectTimeout(connTimeout),
				rd.DialReadTimeout(connTimeout),
				rd.DialWriteTimeout(connTimeout))
			if err != nil {
				return nil, err
			}
			return c, err
		},
	}
	return pool
}

func newRedisQueue(config config.QueueAttrConfig) redisQueue {
	timeout := time.Duration(config.Timeout) * time.Second
	return redisQueue{
		pool:   createRedisQueuePool(config.Bind, timeout, DEFAULT_QUEUE_IDLE_TIMEOUT*time.Second, config.PoolSize),
		config: config,
		active: true,
	}
}

func (q *redisQueue) SetTopic(topic string) {
	q.topic = topic
}

func (q *redisQueue) GetTopic() string {
	return q.topic
}

func (q *redisQueue) IsActive() bool {
	return q.active
}

/**
* 检查队列是否正常启用
 */
func (q *redisQueue) CheckActive() bool {
	if !q.checkConnActive() {
		//链接非活动
		q.active = false
		return false
	}
	if !q.checkQueueLen() {
		//队列长度超过限制
		q.active = false
		return false
	}
	q.active = true
	return true
}

/**
* 检查队列链接是否正常
 */
func (q *redisQueue) checkConnActive() bool {
	conn := q.pool.Get()
	defer conn.Close()
	_, err := rd.DoWithTimeout(conn, time.Duration(q.config.Timeout)*time.Second, "PING")
	if err != nil {
		return false
	} else {
		return true
	}
}

/**
* 检查队列长度
 */
func (q *redisQueue) checkQueueLen() bool {
	maxQueueLen, ok := q.config.Attr["max_queue_len"]
	if ok {
		conn := q.pool.Get()
		defer conn.Close()
		queueLen, err := rd.Int(conn.Do("LLEN", q.topic))
		if err != nil {
			return false
		} else {
			if queueLen > maxQueueLen.(int) {
				return false
			} else {
				return true
			}
		}
	}
	//未设置max_queue_len,则不作队列超载检测
	return true
}

func NewRedisQueueProducer(config config.QueueAttrConfig) *RedisQueueProducer {
	return &RedisQueueProducer{
		redisQueue: newRedisQueue(config),
	}
}

func (q *RedisQueueProducer) SendMessage(data []byte) error {
	conn := q.pool.Get()
	defer conn.Close()
	_, err := conn.Do("LPUSH", q.topic, string(data))
	return err
}

/**
* 停止redis queue producer
 */
func (q *RedisQueueProducer) Stop() error {
	return q.pool.Close()
}

/**
* 开启pipeline
 */
func (q *RedisQueueProducer) StartBatchProducer() (BatchQueueProducer, error) {
	timeout := time.Duration(q.config.Timeout) * time.Second
	conn, err := rd.Dial("tcp", q.config.Bind,
		rd.DialConnectTimeout(timeout),
		rd.DialReadTimeout(timeout),
		rd.DialWriteTimeout(timeout))
	if err != nil {
		return nil, err
	}
	pipelineQueue := &RedisPipelineProducer{
		conn:  conn,
		topic: q.topic,
	}
	return pipelineQueue, nil
}

func (q *RedisPipelineProducer) Topic() string {
	return q.topic
}

/**
* 启用redis pipeline 批量发送消息
 */
func (q *RedisPipelineProducer) SendMessages(items [][]byte) error {
	err := q.conn.Send("MULTI")
	if err != nil {
		return err
	}
	for _, item := range items {
		q.conn.Send("LPUSH", q.topic, string(item))
	}
	return q.conn.Send("EXEC")
}

func (q *RedisPipelineProducer) Stop() error {
	return q.conn.Close()
}

/*
func NewRedisQueueConsumer(config config.QueueAttrConfig, options *Options) *RedisQueueConsumer {
	pqSize := int(math.Max(1, float64(options.MemQueueSize)/2))
	clientId := uuid.NewV4()
	return &RedisQueueConsumer{
		redisQueue:       newRedisQueue(config),
		clientID:         clientId,
		options:          options,
		exitChan:         make(chan int),
		closeChan:        make(chan int),
		MessageChan:      make(chan *Message, options.MemQueueSize),
		inFlightPQ:       newInFlightPqueue(pqSize),
		inFlightMessages: make(map[MessageID]*Message),
		workerNum:        0,
	}
}

func (c *RedisQueueConsumer) GetOpts() *Options {
	return c.options
}

func (c *RedisQueueConsumer) Start() {
	c.waitGroup.Wrap(func() {
		go c.queueScanWorker()
	})
	c.startConsumerWorker(c.options.MinWorkerNum)
}

func (c *RedisQueueConsumer) startConsumerWorker(num int) {
	if c.workerNum < num {
		adjustNum := num - c.workerNum
		for i := 0; i < adjustNum; i++ {
			c.waitGroup.Wrap(func() {
				go c.queueConsumerWorker()
			})
			c.workerNum++
		}
	} else {
		c.closeChan <- 1
		c.workerNum--
	}
}

func (c *RedisQueueConsumer) resizeConsumerWorker() {
	conn := c.pool.Get()
	defer conn.Close()
	queueLen, err := rd.Int(conn.Do("LLEN", c.topic))
	if err != nil {
		return
	}
	if queueLen > int(float64(c.workerNum)*c.options.QueueDelayRatio*2.0) {
		//如果delay size超过最大delay倍数两倍
		c.startConsumerWorker(c.workerNum * c.options.WorkerAdjustRatio)
	}
}

func (c *RedisQueueConsumer) AckMessage(msgId MessageID) error {
	c.removeFromInFlightPQ(msgId)
	return nil
}

func (c *RedisQueueConsumer) GetMessageChan() chan *Message {
	return c.MessageChan
}

func (c *RedisQueueConsumer) Stop() {
	close(c.closeChan)
	close(c.exitChan)
	c.waitGroup.Wait()
}

func (c *RedisQueueConsumer) getMessage(wait bool) (*Message, error) {
	conn := c.pool.Get()
	defer conn.Close()
	var data []byte
	var err error
	if wait {
		s, err := rd.ByteSlices(rd.DoWithTimeout(conn, time.Duration(REDIS_CONN_READ_TIMEOUT)*time.Second, "BRPOP", c.topic, REDIS_CONN_READ_TIMEOUT))
		if err == nil {
			data = s[1]
		}
	} else {
		data, err = rd.Bytes(conn.Do("RPOP", c.topic))
	}
	if len(data) > 0 {
		msgId := uuid.NewV4()
		if err != nil {
			return nil, err
		}
		message := &Message{
			ClientID: c.clientID,
			ID:       MessageID(msgId.String()),
			Body:     data,
		}
		return message, err
	}
	return nil, err
}

func (c *RedisQueueConsumer) addToInFlightPQ(msg *Message, timeout time.Duration) {
	c.inFlightMutex.Lock()
	now := time.Now()
	msg.pri = now.Add(timeout).UnixNano()
	msg.index = 0
	c.inFlightPQ.Push(msg)
	c.inFlightMessages[msg.ID] = msg
	c.inFlightMutex.Unlock()
	return
}

func (c *RedisQueueConsumer) removeFromInFlightPQ(msgId MessageID) {
	c.inFlightMutex.Lock()
	item, ok := c.inFlightMessages[msgId]
	if !ok {
		c.inFlightMutex.Unlock()
		return
	}
	delete(c.inFlightMessages, msgId)
	if item.index == -1 {
		c.inFlightMutex.Unlock()
		return
	}
	c.inFlightPQ.Remove(item.index)
	c.inFlightMutex.Unlock()
	return
}

func (c *RedisQueueConsumer) requeueMessage(msg *Message) error {
	conn := c.pool.Get()
	defer conn.Close()
	_, err := conn.Do("LPUSH", c.topic, msg.Body)
	if err != nil {
		return err
	}
	return nil
}

func (c *RedisQueueConsumer) queueConsumerWorker() {
	for {
		message, err := c.getMessage(true)
		if err != nil {
			continue
		}
		if message == nil {
			//未取到消息
			continue
		}
		select {
		case c.MessageChan <- message:
			c.addToInFlightPQ(message, time.Duration(c.options.QueueInActiveTimeout)*time.Second)
		case <-c.closeChan:
			goto exit
		}
	}
exit:
}

func (c *RedisQueueConsumer) queueScanWorker() {
	workerTicker := time.NewTicker(time.Second)
	for {
		select {
		case <-workerTicker.C:
			c.processInFlightQueue(time.Now().UnixNano())
		case <-c.exitChan:
			goto exit
		}
	}
exit:
}

func (c *RedisQueueConsumer) processInFlightQueue(t int64) error {
	var err error
	for {
		c.inFlightMutex.Lock()
		msg, _ := c.inFlightPQ.PeekAndShift(t)
		c.inFlightMutex.Unlock()

		if msg == nil {
			goto exit
		}
		c.removeFromInFlightPQ(msg.ID)
		err = c.requeueMessage(msg)
		if err != nil {
			goto exit
		}
	}

exit:
	return err
}
*/
