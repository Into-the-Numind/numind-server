package mqtt

import (
	"encoding/json"
	"fmt"
	"time"

	"numind-server/internal/pkg/log"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/spf13/viper"
)

type MqttBiz interface {
	Connect() error
	Disconnect()
	Publish(topic string, payload interface{}) error
	Subscribe(topic string, callback func(topic string, payload []byte)) error
	IsConnected() bool
	HealthCheck() error
}

type mqttBiz struct {
	client mqtt.Client
	config *MqttConfig
}

type MqttConfig struct {
	Broker   string
	ClientID string
	Username string
	Password string
	Port     int
}

type ImageProcessingResult struct {
	TaskID          string                 `json:"task_id"`
	UserID          uint                   `json:"user_id"`
	Status          string                 `json:"status"`
	ProcessedImages []ProcessedImage       `json:"processed_images"`
	FinalResult     *FinalProcessingResult `json:"final_result,omitempty"`
	ErrorMessage    string                 `json:"error_message,omitempty"`
	ProcessingTime  time.Duration          `json:"processing_time"`
	CreatedAt       time.Time              `json:"created_at"`
}

type ProcessedImage struct {
	Filename      string `json:"filename"`
	URL           string `json:"url"`
	OriginalText  string `json:"original_text"`
	QianwenResult string `json:"qianwen_result"`
}

type FinalProcessingResult struct {
	WanxiangResult string `json:"wanxiang_result"`
	BookID         uint   `json:"book_id,omitempty"`
	TotalTexts     int    `json:"total_texts"`
}

func NewMqttBiz() MqttBiz {
	config := &MqttConfig{
		Broker:   viper.GetString("mqtt.broker"),
		ClientID: viper.GetString("mqtt.client_id"),
		Username: viper.GetString("mqtt.username"),
		Password: viper.GetString("mqtt.password"),
		Port:     viper.GetInt("mqtt.port"),
	}

	if config.Broker == "" {
		config.Broker = "localhost"
	}
	if config.ClientID == "" {
		config.ClientID = "numind-server"
	}
	if config.Port == 0 {
		config.Port = 1883
	}

	return &mqttBiz{
		config: config,
	}
}

func (m *mqttBiz) Connect() error {
	log.Infow("Attempting to connect to MQTT broker",
		"broker", m.config.Broker,
		"port", m.config.Port,
		"client_id", m.config.ClientID,
		"username", m.config.Username)

	opts := mqtt.NewClientOptions()
	opts.AddBroker(fmt.Sprintf("tcp://%s:%d", m.config.Broker, m.config.Port))
	opts.SetClientID(m.config.ClientID)

	if m.config.Username != "" {
		opts.SetUsername(m.config.Username)
		opts.SetPassword(m.config.Password)
	}

	// 连接稳定性设置
	opts.SetConnectTimeout(60 * time.Second)       // 增加连接超时时间
	opts.SetKeepAlive(60 * time.Second)            // 增加心跳间隔到60秒
	opts.SetPingTimeout(30 * time.Second)          // 增加Ping超时时间
	opts.SetAutoReconnect(true)                    // 自动重连
	opts.SetMaxReconnectInterval(60 * time.Second) // 增加最大重连间隔
	opts.SetConnectRetry(true)                     // 连接重试
	opts.SetConnectRetryInterval(10 * time.Second) // 增加重连间隔
	opts.SetCleanSession(false)                    // 保持会话状态
	opts.SetOrderMatters(false)                    // 消息顺序不重要，提高性能
	opts.SetResumeSubs(true)                       // 重连时恢复订阅
	opts.SetWriteTimeout(30 * time.Second)         // 设置写超时

	// 设置消息处理器
	opts.SetDefaultPublishHandler(func(client mqtt.Client, msg mqtt.Message) {
		log.Infow("Received message", "topic", msg.Topic(), "payload", string(msg.Payload()))
	})

	// 连接成功回调
	opts.SetOnConnectHandler(func(client mqtt.Client) {
		log.Infow("Successfully connected to MQTT broker",
			"broker", m.config.Broker,
			"client_id", m.config.ClientID,
			"keepalive", "60s",
			"timestamp", time.Now().Format("2006-01-02 15:04:05"))
	})

	// 连接丢失回调
	opts.SetConnectionLostHandler(func(client mqtt.Client, err error) {
		log.Errorw("Connection to MQTT broker lost",
			"broker", m.config.Broker,
			"client_id", m.config.ClientID,
			"error", err.Error())

		// 记录连接丢失的统计信息
		log.Infow("MQTT connection lost, auto-reconnect will be attempted",
			"broker", m.config.Broker,
			"client_id", m.config.ClientID)
	})

	m.client = mqtt.NewClient(opts)

	log.Infow("Connecting to MQTT broker...")
	if token := m.client.Connect(); token.Wait() && token.Error() != nil {
		log.Errorw("Failed to connect to MQTT broker", "error", token.Error())
		return fmt.Errorf("failed to connect to MQTT broker: %w", token.Error())
	}

	// 等待连接稳定
	time.Sleep(2 * time.Second)

	if m.client.IsConnected() {
		log.Infow("MQTT connection established successfully")
	} else {
		log.Errorw("MQTT connection failed to establish")
		return fmt.Errorf("MQTT connection failed to establish")
	}

	return nil
}

func (m *mqttBiz) Disconnect() {
	if m.client != nil && m.client.IsConnected() {
		m.client.Disconnect(250)
	}
}

func (m *mqttBiz) Publish(topic string, payload interface{}) error {
	if m.client == nil {
		log.Errorw("MQTT client is nil")
		return fmt.Errorf("MQTT client not initialized")
	}

	// 检查连接状态，如果未连接则尝试重连
	if !m.client.IsConnected() {
		log.Warnw("MQTT client not connected, attempting to reconnect", "broker", m.config.Broker, "client_id", m.config.ClientID)
		if err := m.Connect(); err != nil {
			log.Errorw("Failed to reconnect to MQTT broker", "error", err)
			return fmt.Errorf("MQTT client not connected and reconnection failed: %w", err)
		}
	}

	var payloadBytes []byte
	var err error

	switch p := payload.(type) {
	case []byte:
		payloadBytes = p
	case string:
		payloadBytes = []byte(p)
	default:
		payloadBytes, err = json.Marshal(payload)
		if err != nil {
			log.Errorw("Failed to marshal payload", "error", err)
			return fmt.Errorf("failed to marshal payload: %w", err)
		}
	}

	log.Infow("Publishing message to MQTT", "topic", topic, "payload_size", len(payloadBytes))

	// 添加重试机制
	maxRetries := 3
	for i := 0; i < maxRetries; i++ {
		token := m.client.Publish(topic, 0, false, payloadBytes)
		if token.Wait() && token.Error() != nil {
			log.Warnw("Failed to publish message, retrying", "attempt", i+1, "max_retries", maxRetries, "error", token.Error())
			if i == maxRetries-1 {
				log.Errorw("Failed to publish message after all retries", "topic", topic, "error", token.Error())
				return fmt.Errorf("failed to publish message after %d retries: %w", maxRetries, token.Error())
			}
			time.Sleep(time.Duration(i+1) * time.Second) // 递增延迟
			continue
		}
		log.Infow("Successfully published message to MQTT", "topic", topic, "payload_size", len(payloadBytes))
		return nil
	}

	return fmt.Errorf("failed to publish message after %d retries", maxRetries)
}

func (m *mqttBiz) Subscribe(topic string, callback func(topic string, payload []byte)) error {
	if m.client == nil || !m.client.IsConnected() {
		return fmt.Errorf("MQTT client not connected")
	}

	token := m.client.Subscribe(topic, 0, func(client mqtt.Client, msg mqtt.Message) {
		callback(msg.Topic(), msg.Payload())
	})

	if token.Wait() && token.Error() != nil {
		return fmt.Errorf("failed to subscribe to topic: %w", token.Error())
	}

	log.Infow("Subscribed to MQTT topic", "topic", topic)
	return nil
}

func (m *mqttBiz) IsConnected() bool {
	return m.client != nil && m.client.IsConnected()
}

// HealthCheck 检查MQTT连接健康状态
func (m *mqttBiz) HealthCheck() error {
	if m.client == nil {
		return fmt.Errorf("MQTT client is nil")
	}

	if !m.client.IsConnected() {
		return fmt.Errorf("MQTT client is not connected")
	}

	// 尝试发布一个测试消息来验证连接
	testTopic := fmt.Sprintf("numind/health/check/%s", m.config.ClientID)
	testPayload := map[string]interface{}{
		"timestamp": time.Now().Unix(),
		"client_id": m.config.ClientID,
		"status":    "health_check",
	}

	if err := m.Publish(testTopic, testPayload); err != nil {
		return fmt.Errorf("MQTT health check failed: %w", err)
	}

	return nil
}

// PublishImageProcessingResult 发布图片处理结果到MQTT
func (m *mqttBiz) PublishImageProcessingResult(result *ImageProcessingResult) error {
	topic := fmt.Sprintf("numind/image/processing/result/%d", result.UserID)
	return m.Publish(topic, result)
}

// PublishImageProcessingStatus 发布图片处理状态到MQTT
func (m *mqttBiz) PublishImageProcessingStatus(taskID string, userID uint, status string, message string) error {
	statusMsg := map[string]interface{}{
		"task_id":   taskID,
		"user_id":   userID,
		"status":    status,
		"message":   message,
		"timestamp": time.Now().Unix(),
	}

	topic := fmt.Sprintf("numind/image/processing/status/%d", userID)
	return m.Publish(topic, statusMsg)
}
