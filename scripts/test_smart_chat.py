#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
智能聊天功能测试脚本
测试分词和卡册调取在聊天中的集成
"""

import asyncio
import websockets
import json
import time
from datetime import datetime

# WebSocket服务器地址
WS_URL = "ws://localhost:9091/v1/chat/ws"

async def test_smart_chat():
    """测试智能聊天功能"""
    print("🧠 智能聊天功能测试")
    print("=" * 50)
    print(f"连接地址: {WS_URL}")
    print()
    
    try:
        # 连接WebSocket服务器
        print("1. 正在连接WebSocket服务器...")
        async with websockets.connect(WS_URL) as websocket:
            print("✅ WebSocket连接成功")
            print()
            
            # 测试不同类型的消息
            test_messages = [
                # 搜索相关消息（会触发卡册搜索）
                {
                    "type": "message",
                    "content": "我想找一些旅行照片卡册",
                    "description": "搜索意图消息 - 应该触发卡册搜索"
                },
                {
                    "type": "message", 
                    "content": "推荐一些美食相关的卡册",
                    "description": "推荐意图消息 - 应该触发卡册搜索"
                },
                {
                    "type": "message",
                    "content": "有什么摄影技巧的卡册吗？",
                    "description": "询问意图消息 - 应该触发卡册搜索"
                },
                {
                    "type": "message",
                    "content": "我对艺术设计类卡册感兴趣",
                    "description": "兴趣表达消息 - 应该触发卡册搜索"
                },
                
                # 日常对话消息（不会触发搜索）
                {
                    "type": "message",
                    "content": "你好",
                    "description": "问候消息 - 应该获得默认回复"
                },
                {
                    "type": "message",
                    "content": "谢谢你的帮助",
                    "description": "感谢消息 - 应该获得默认回复"
                },
                {
                    "type": "message",
                    "content": "这个功能怎么用？",
                    "description": "帮助消息 - 应该获得默认回复"
                }
            ]
            
            for i, test_msg in enumerate(test_messages, 1):
                print(f"2.{i} 测试: {test_msg['description']}")
                print(f"   发送消息: {test_msg['content']}")
                
                # 发送聊天消息
                await websocket.send(json.dumps(test_msg, ensure_ascii=False))
                
                # 等待响应
                try:
                    response = await asyncio.wait_for(websocket.recv(), timeout=15.0)
                    response_data = json.loads(response)
                    
                    print(f"   收到回复: {response_data.get('content', '')[:100]}...")
                    
                    # 分析回复类型
                    if "找到了" in response_data.get('content', '') or "相关卡册" in response_data.get('content', ''):
                        print("   ✅ 触发了卡册搜索")
                    elif "你好" in response_data.get('content', '') or "帮助" in response_data.get('content', ''):
                        print("   ✅ 获得了默认回复")
                    else:
                        print("   ⚠️  回复类型不明确")
                        
                except asyncio.TimeoutError:
                    print("   ❌ 响应超时")
                except json.JSONDecodeError:
                    print("   ❌ 响应格式错误")
                
                print()
                time.sleep(2)  # 避免请求过于频繁
            
            # 测试搜索功能
            print("3. 测试专门的搜索功能...")
            search_request = {
                "type": "search_books",
                "content": "旅行照片卡册",
                "timestamp": datetime.now().isoformat()
            }
            
            print(f"   发送搜索请求: {json.dumps(search_request, ensure_ascii=False)}")
            
            await websocket.send(json.dumps(search_request, ensure_ascii=False))
            
            try:
                response = await asyncio.wait_for(websocket.recv(), timeout=10.0)
                response_data = json.loads(response)
                
                if response_data.get("type") == "search_books_result":
                    print(f"   ✅ 搜索功能正常，找到 {len(response_data.get('data', []))} 本相关书籍")
                else:
                    print(f"   ❌ 搜索响应类型不正确: {response_data.get('type')}")
                    
            except asyncio.TimeoutError:
                print("   ❌ 搜索响应超时")
            
            print()
            print("=== 智能聊天测试完成 ===")
            
    except websockets.exceptions.ConnectionRefused:
        print("❌ 连接被拒绝，请确保服务正在运行")
        print("启动命令: go run cmd/numind/main.go")
    except Exception as e:
        print(f"❌ 连接失败: {e}")

async def test_chat_flow():
    """测试完整的聊天流程"""
    print("🔄 完整聊天流程测试")
    print("=" * 50)
    print(f"连接地址: {WS_URL}")
    print()
    
    try:
        async with websockets.connect(WS_URL) as websocket:
            print("✅ WebSocket连接成功")
            print()
            
            # 模拟真实的聊天对话
            conversation = [
                "你好",
                "我想找一些旅行照片卡册",
                "这些卡册都有什么内容？",
                "能推荐一些美食相关的吗？",
                "谢谢你的帮助",
                "再见"
            ]
            
            print("开始模拟聊天对话...")
            print()
            
            for i, message in enumerate(conversation, 1):
                print(f"用户 ({i}): {message}")
                
                # 发送消息
                chat_request = {
                    "type": "message",
                    "content": message,
                    "timestamp": datetime.now().isoformat()
                }
                
                await websocket.send(json.dumps(chat_request, ensure_ascii=False))
                
                # 等待回复
                try:
                    response = await asyncio.wait_for(websocket.recv(), timeout=10.0)
                    response_data = json.loads(response)
                    
                    assistant_reply = response_data.get('content', '')
                    print(f"助手 ({i}): {assistant_reply[:100]}...")
                    
                    # 分析回复类型
                    if any(keyword in message for keyword in ["找", "推荐", "什么", "相关"]):
                        if "找到了" in assistant_reply or "相关卡册" in assistant_reply:
                            print("   ✅ 正确触发了卡册搜索")
                        else:
                            print("   ⚠️  应该触发搜索但未触发")
                    else:
                        if "你好" in assistant_reply or "帮助" in assistant_reply or "谢谢" in assistant_reply:
                            print("   ✅ 正确获得了默认回复")
                        else:
                            print("   ⚠️  回复类型不明确")
                    
                except asyncio.TimeoutError:
                    print("   ❌ 响应超时")
                
                print()
                time.sleep(1)
            
            print("=== 聊天流程测试完成 ===")
            
    except Exception as e:
        print(f"❌ 测试失败: {e}")

def main():
    """主函数"""
    print("选择测试类型:")
    print("1. 智能聊天功能测试")
    print("2. 完整聊天流程测试")
    print("3. 全部测试")
    
    choice = input("请输入选择 (1/2/3): ").strip()
    
    if choice == "1":
        asyncio.run(test_smart_chat())
    elif choice == "2":
        asyncio.run(test_chat_flow())
    elif choice == "3":
        print("\n" + "="*50)
        asyncio.run(test_smart_chat())
        print("\n" + "="*50)
        asyncio.run(test_chat_flow())
    else:
        print("无效选择，默认执行智能聊天功能测试")
        asyncio.run(test_smart_chat())

if __name__ == "__main__":
    main()
