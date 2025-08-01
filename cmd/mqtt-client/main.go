package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

func main() {
	fmt.Println("Numind MQTT Client - Image Processing Result Listener")
	fmt.Println("====================================================")

	// MQTT 配置
	broker := "localhost"
	port := 1883
	clientID := "numind-client"
	username := ""
	password := ""

	// 创建 MQTT 客户端选项
	opts := mqtt.NewClientOptions()
	opts.AddBroker(fmt.Sprintf("tcp://%s:%d", broker, port))
	opts.SetClientID(clientID)

	if username != "" {
		opts.SetUsername(username)
		opts.SetPassword(password)
	}

	// 设置连接回调
	opts.SetOnConnectHandler(func(client mqtt.Client) {
		fmt.Printf("Connected to MQTT broker: %s:%d\n", broker, port)
	})

	// 设置连接丢失回调
	opts.SetConnectionLostHandler(func(client mqtt.Client, err error) {
		fmt.Printf("Connection to MQTT broker lost: %v\n", err)
	})

	// 创建客户端
	client := mqtt.NewClient(opts)

	// 连接到 MQTT broker
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		log.Fatalf("Failed to connect to MQTT broker: %v", token.Error())
	}

	// 订阅状态主题
	statusTopic := "numind/image/processing/status/#"
	if token := client.Subscribe(statusTopic, 0, func(client mqtt.Client, msg mqtt.Message) {
		fmt.Printf("\n[STATUS] Topic: %s\n", msg.Topic())
		fmt.Printf("Payload: %s\n", string(msg.Payload()))

		// 尝试解析 JSON
		var status map[string]interface{}
		if err := json.Unmarshal(msg.Payload(), &status); err == nil {
			if taskID, ok := status["task_id"].(string); ok {
				fmt.Printf("Task ID: %s\n", taskID)
			}
			if statusMsg, ok := status["status"].(string); ok {
				fmt.Printf("Status: %s\n", statusMsg)
			}
			if message, ok := status["message"].(string); ok {
				fmt.Printf("Message: %s\n", message)
			}
		}
		fmt.Println("---")
	}); token.Wait() && token.Error() != nil {
		log.Fatalf("Failed to subscribe to status topic: %v", token.Error())
	}

	// 订阅结果主题
	resultTopic := "numind/image/processing/result/#"
	if token := client.Subscribe(resultTopic, 0, func(client mqtt.Client, msg mqtt.Message) {
		fmt.Printf("\n[RESULT] Topic: %s\n", msg.Topic())
		fmt.Printf("Payload: %s\n", string(msg.Payload()))

		// 尝试解析 JSON
		var result map[string]interface{}
		if err := json.Unmarshal(msg.Payload(), &result); err == nil {
			if taskID, ok := result["task_id"].(string); ok {
				fmt.Printf("Task ID: %s\n", taskID)
			}
			if status, ok := result["status"].(string); ok {
				fmt.Printf("Status: %s\n", status)
			}
			if processingTime, ok := result["processing_time"].(string); ok {
				fmt.Printf("Processing Time: %s\n", processingTime)
			}
			if processedImages, ok := result["processed_images"].([]interface{}); ok {
				fmt.Printf("Processed Images Count: %d\n", len(processedImages))
			}
			if finalResult, ok := result["final_result"].(map[string]interface{}); ok {
				if bookID, ok := finalResult["book_id"].(float64); ok {
					fmt.Printf("Generated Book ID: %.0f\n", bookID)
				}
				if wanxiangResult, ok := finalResult["wanxiang_result"].(string); ok {
					fmt.Printf("Wanxiang Result: %s\n", wanxiangResult)
				}
			}
		}
		fmt.Println("---")
	}); token.Wait() && token.Error() != nil {
		log.Fatalf("Failed to subscribe to result topic: %v", token.Error())
	}

	fmt.Printf("Subscribed to topics:\n")
	fmt.Printf("- Status: %s\n", statusTopic)
	fmt.Printf("- Result: %s\n", resultTopic)
	fmt.Println("\nWaiting for messages... (Press Ctrl+C to exit)")

	// 等待中断信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	fmt.Println("\nDisconnecting from MQTT broker...")
	client.Disconnect(250)
	fmt.Println("Disconnected")
}
