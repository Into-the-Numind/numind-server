package main

import (
	"fmt"
	"log"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/spf13/viper"
)

func main() {
	// 加载配置文件
	viper.SetConfigName("config_local")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")

	if err := viper.ReadInConfig(); err != nil {
		log.Fatalf("Error reading config file: %v", err)
	}

	// 获取MQTT配置
	broker := viper.GetString("mqtt.broker")
	port := viper.GetInt("mqtt.port")
	clientID := viper.GetString("mqtt.client_id")
	username := viper.GetString("mqtt.username")
	password := viper.GetString("mqtt.password")

	fmt.Printf("MQTT Configuration:\n")
	fmt.Printf("  Broker: %s\n", broker)
	fmt.Printf("  Port: %d\n", port)
	fmt.Printf("  Client ID: %s\n", clientID)
	fmt.Printf("  Username: %s\n", username)
	fmt.Printf("  Password: %s\n", password)

	// 创建MQTT客户端选项
	opts := mqtt.NewClientOptions()
	opts.AddBroker(fmt.Sprintf("tcp://%s:%d", broker, port))
	opts.SetClientID(clientID)

	if username != "" {
		opts.SetUsername(username)
		opts.SetPassword(password)
	}

	// 设置连接回调
	opts.SetOnConnectHandler(func(client mqtt.Client) {
		fmt.Println("✅ Successfully connected to MQTT broker!")
	})

	opts.SetConnectionLostHandler(func(client mqtt.Client, err error) {
		fmt.Printf("❌ Connection lost: %v\n", err)
	})

	// 创建客户端
	client := mqtt.NewClient(opts)

	// 连接
	fmt.Println("Connecting to MQTT broker...")
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		log.Fatalf("Failed to connect: %v", token.Error())
	}

	// 等待连接建立
	time.Sleep(2 * time.Second)

	// 检查连接状态
	if client.IsConnected() {
		fmt.Println("✅ MQTT client is connected!")

		// 测试发布消息
		testTopic := "test/connection"
		testMessage := "Hello from numind-server test client"

		fmt.Printf("Publishing test message to topic: %s\n", testTopic)
		token := client.Publish(testTopic, 0, false, testMessage)
		if token.Wait() && token.Error() != nil {
			fmt.Printf("❌ Failed to publish: %v\n", token.Error())
		} else {
			fmt.Println("✅ Test message published successfully!")
		}

		// 测试订阅
		fmt.Printf("Subscribing to topic: %s\n", testTopic)
		token = client.Subscribe(testTopic, 0, func(client mqtt.Client, msg mqtt.Message) {
			fmt.Printf("📨 Received message: %s\n", string(msg.Payload()))
		})
		if token.Wait() && token.Error() != nil {
			fmt.Printf("❌ Failed to subscribe: %v\n", token.Error())
		} else {
			fmt.Println("✅ Successfully subscribed!")
		}

		// 等待一段时间接收消息
		fmt.Println("Waiting for messages... (press Ctrl+C to exit)")
		time.Sleep(10 * time.Second)

	} else {
		fmt.Println("❌ MQTT client is not connected!")
	}

	// 断开连接
	client.Disconnect(250)
	fmt.Println("Disconnected from MQTT broker")
}
