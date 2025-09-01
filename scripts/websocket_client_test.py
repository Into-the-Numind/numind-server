#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
WebSocket客户端测试脚本
用于测试分词和卡册调取功能在WebSocket API中的集成
"""

import asyncio
import websockets
import json
import time
from datetime import datetime

# WebSocket服务器地址
WS_URL = "ws://localhost:9091/v1/chat/ws"

async def test_websocket_search():
    """测试WebSocket搜索功能"""
    print("=== WebSocket搜索功能测试 ===")
    print(f"连接地址: {WS_URL}")
    print()
    
    try:
        # 连接WebSocket服务器
        print("1. 正在连接WebSocket服务器...")
        async with websockets.connect(WS_URL) as websocket:
            print("✅ WebSocket连接成功")
            print()
            
            # 测试搜索消息
            test_queries = [
                "旅行照片卡册",
                "美食烹饪菜谱", 
                "人工智能技术",
                "摄影技巧大全"
            ]
            
            for i, query in enumerate(test_queries, 1):
                print(f"2.{i} 测试搜索查询: '{query}'")
                
                # 构建搜索请求
                search_request = {
                    "type": "search_books",
                    "content": query,
                    "timestamp": datetime.now().isoformat()
                }
                
                print(f"   发送请求: {json.dumps(search_request, ensure_ascii=False)}")
                
                # 发送搜索请求
                await websocket.send(json.dumps(search_request, ensure_ascii=False))
                
                # 等待响应
                try:
                    response = await asyncio.wait_for(websocket.recv(), timeout=10.0)
                    response_data = json.loads(response)
                    
                    print(f"   收到响应: {json.dumps(response_data, ensure_ascii=False, indent=2)}")
                    
                    # 验证响应格式
                    if response_data.get("type") == "search_books_result":
                        print(f"   ✅ 搜索成功，找到 {len(response_data.get('data', []))} 本相关书籍")
                    else:
                        print(f"   ❌ 响应类型不正确: {response_data.get('type')}")
                        
                except asyncio.TimeoutError:
                    print("   ❌ 响应超时")
                except json.JSONDecodeError:
                    print("   ❌ 响应格式错误")
                
                print()
                time.sleep(1)  # 避免请求过于频繁
            
            # 测试心跳
            print("3. 测试心跳消息...")
            ping_request = {
                "type": "ping",
                "timestamp": datetime.now().isoformat()
            }
            
            await websocket.send(json.dumps(ping_request, ensure_ascii=False))
            
            try:
                response = await asyncio.wait_for(websocket.recv(), timeout=5.0)
                response_data = json.loads(response)
                
                if response_data.get("type") == "pong":
                    print("✅ 心跳测试成功")
                else:
                    print(f"❌ 心跳响应类型不正确: {response_data.get('type')}")
                    
            except asyncio.TimeoutError:
                print("❌ 心跳响应超时")
            
            print()
            print("=== 测试完成 ===")
            
    except websockets.exceptions.ConnectionRefused:
        print("❌ 连接被拒绝，请确保服务正在运行")
        print("启动命令: go run cmd/numind/main.go")
    except Exception as e:
        print(f"❌ 连接失败: {e}")

async def test_websocket_chat():
    """测试WebSocket聊天功能"""
    print("=== WebSocket聊天功能测试 ===")
    print(f"连接地址: {WS_URL}")
    print()
    
    try:
        # 连接WebSocket服务器
        print("1. 正在连接WebSocket服务器...")
        async with websockets.connect(WS_URL) as websocket:
            print("✅ WebSocket连接成功")
            print()
            
            # 测试聊天消息
            print("2. 测试聊天消息...")
            chat_request = {
                "type": "message",
                "content": "你好，我想搜索一些关于旅行的卡册",
                "timestamp": datetime.now().isoformat()
            }
            
            print(f"   发送聊天消息: {json.dumps(chat_request, ensure_ascii=False)}")
            
            # 发送聊天消息
            await websocket.send(json.dumps(chat_request, ensure_ascii=False))
            
            # 等待响应
            try:
                response = await asyncio.wait_for(websocket.recv(), timeout=10.0)
                response_data = json.loads(response)
                
                print(f"   收到回复: {json.dumps(response_data, ensure_ascii=False, indent=2)}")
                
                if response_data.get("type") == "message":
                    print("✅ 聊天功能正常")
                else:
                    print(f"❌ 聊天响应类型不正确: {response_data.get('type')}")
                    
            except asyncio.TimeoutError:
                print("❌ 聊天响应超时")
            
            print()
            print("=== 聊天测试完成 ===")
            
    except websockets.exceptions.ConnectionRefused:
        print("❌ 连接被拒绝，请确保服务正在运行")
    except Exception as e:
        print(f"❌ 连接失败: {e}")

def main():
    """主函数"""
    print("选择测试类型:")
    print("1. 搜索功能测试")
    print("2. 聊天功能测试")
    print("3. 全部测试")
    
    choice = input("请输入选择 (1/2/3): ").strip()
    
    if choice == "1":
        asyncio.run(test_websocket_search())
    elif choice == "2":
        asyncio.run(test_websocket_chat())
    elif choice == "3":
        print("\n" + "="*50)
        asyncio.run(test_websocket_search())
        print("\n" + "="*50)
        asyncio.run(test_websocket_chat())
    else:
        print("无效选择，默认执行搜索功能测试")
        asyncio.run(test_websocket_search())

if __name__ == "__main__":
    main()
