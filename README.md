## queue_proxy

- 支持redis/kafka多种队列配置,统一发送方式
- 支持disk queue做队列消息发送灾备,避免消息丢失
- 支持队列级别限速,本地队列支持数据压缩

### 安装使用
```
  go get github.com/jmuyuyang/queue_proxy
```

### 配置说明
```
  type: kafka
  redis:
    bind: 127.0.0.1:6379
    timeout: 3
    size: 5 //连接池长度
  kafka:
    bind: 172.16.2.216:9092
    timeout: 20
    size: 5 //连接池长度
  disk:
    path: "./data"
    prefix: "logcenter-proxy"
    flush_timeout: 2
    compress_type: "gzip" //文件压缩方式
```

### 使用说明
```
    import queue "github.com/jmuyuyang/queue_proxy"
    val config queue.QueueConfig
    yaml.Unmarshal([]byte(config), &config)
    queue.NewQueueProducer(topicName, config)
    queue.SetQueueType("kafka")
    queue.StartBackend()

    queue.SendMessage(dateByte)
    queue.SetRateLimit(ratePerSecond) //限制限流(每秒流速)
    queue.Stop()
```
