#!/bin/bash

echo "Testing MQTT connection..."

# 检查MQTT broker是否运行
if ! pgrep -x "mosquitto" > /dev/null; then
    echo "MQTT broker (mosquitto) is not running. Starting it..."
    mosquitto &
    sleep 2
fi

# 测试MQTT连接
echo "Testing MQTT connection to localhost:1883..."
mosquitto_pub -h localhost -p 1883 -t "test/connection" -m "Hello MQTT" 2>/dev/null

if [ $? -eq 0 ]; then
    echo "✅ MQTT connection successful!"
else
    echo "❌ MQTT connection failed!"
    echo "Please make sure mosquitto is installed and running:"
    echo "  - Install: brew install mosquitto (macOS) or sudo apt-get install mosquitto (Ubuntu)"
    echo "  - Start: mosquitto"
fi

echo ""
echo "To monitor MQTT messages, run:"
echo "  mosquitto_sub -t 'numind/image/processing/#' -v" 