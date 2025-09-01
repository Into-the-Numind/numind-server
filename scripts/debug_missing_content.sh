#!/bin/bash

# 调试丢失内容问题的专用脚本
# 专门测试第7条内容丢失的问题

set -e

echo "🔍 开始调试卡片内容丢失问题..."
echo "=========================================="

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 测试数据：模拟用户的7条内容
echo -e "${BLUE}📋 创建测试JSON数据...${NC}"

TEST_JSON_FILE="test_missing_content_$(date +%s).json"
cat > "$TEST_JSON_FILE" << 'EOF'
{
  "content": [
    "第1条：早上别一醒就摸手机，先拉开窗帘会儿。醒了到急着刷消息，推开窗让风进来，看看楼下的树影晃了晃，听几声早起的鸟叫。这几分钟的空白，比刷十条推送更让人清醒，线下的清晨，其实藏着最软的温柔。",
    "第2条：把运动挪到户外去，哪怕只是慢走。不用非得跑几公里，下班后绕着小区走两圈，踩踩停晚的夕阳敲子，闻闻路过人家厨房飘来的饭菜香。运动时的汗珠子，比手机里的步数排行榜实在多了。",
    "第3条：周末约朋友坐下来\"浪费时间\"。别总说\"线上聊\"，找家街角的咖啡馆，点两杯喝的，面对面坐着发呆、扯闲篇。阳光透过窗户落在杯沿上，比视频通话里的像素更暖。",
    "第4条：读本纸质书，感受翻页的重量。睡前别刷手机了，摆开一本纸质书，指尖划过纸页的纹路，闻闻淡淡的油墨味。看到喜欢的句子折个角，这种实实在在的痕迹，比电子书签让人踏实。",
    "第5条：跟小区里的流浪猫打个招呼。下楼丢垃圾时，留意下花坛边的流浪猫，带袋猫粮放在角落，看它警惕地凑过来吃，尾巴轻轻晃的样子。这些具体的小生命，比手机里的萌宠视频更让人心里软。",
    "第6条：走条没走过的回家路，发现新风景。下班别总按老路线走，绕进旁边的小巷子。看看墙头上爬的牵牛花，听老房子里传来的收音机声，说不定会遇见一家藏在巷尾的小面馆。这些意外的发现，比算法推荐的\"附近好去处\"惊喜多了。",
    "第7条：睡前花5分钟复盘今天的线下小事……会让你觉得'今天真的活过'"
  ],
  "type": "list"
}
EOF

echo -e "${GREEN}✅ 测试数据创建完成: $TEST_JSON_FILE${NC}"
echo -e "${BLUE}📊 数据统计:${NC}"
echo "  - 总条数: 7"
echo "  - 类型: list"
echo "  - 最后一条内容: 睡前花5分钟复盘今天的线下小事..."

echo ""

# 检查JSON格式是否正确
echo -e "${BLUE}🔍 验证JSON格式...${NC}"
if jq empty "$TEST_JSON_FILE" 2>/dev/null; then
    echo -e "${GREEN}✅ JSON格式正确${NC}"
    
    # 解析内容数组
    CONTENT_COUNT=$(jq '.content | length' "$TEST_JSON_FILE")
    echo -e "${GREEN}📋 内容数组长度: $CONTENT_COUNT${NC}"
    
    # 显示每条内容的前50个字符
    echo -e "${BLUE}📝 内容预览:${NC}"
    for i in $(seq 0 $((CONTENT_COUNT-1))); do
        CONTENT=$(jq -r ".content[$i] | .[0:50]" "$TEST_JSON_FILE")
        echo "  [$((i+1))] $CONTENT..."
    done
    
    # 特别检查第7条
    LAST_CONTENT=$(jq -r '.content[-1]' "$TEST_JSON_FILE")
    echo -e "${YELLOW}🎯 第7条(最后一条)完整内容:${NC}"
    echo "  $LAST_CONTENT"
    
else
    echo -e "${RED}❌ JSON格式错误${NC}"
    exit 1
fi

echo ""

# 现在测试Go代码的处理
echo -e "${BLUE}🔬 测试Go代码处理过程...${NC}"

# 创建Go测试文件
TEST_GO_FILE="debug_missing_content_main.go"
cat > "$TEST_GO_FILE" << 'EOF'
package main

import (
    "encoding/json"
    "fmt"
    "os"
)

// QianwenResponse 模拟AI响应结构
type QianwenResponse struct {
    StructuredTextArray []struct {
        Type    string      `json:"type"`
        Content interface{} `json:"content"`
    } `json:"structured_text_array"`
}

// Element 分页元素
type Element struct {
    Type    string
    Content interface{}
}

func main() {
    if len(os.Args) < 2 {
        fmt.Println("用法: go run debug_missing_content_test.go <json_file>")
        os.Exit(1)
    }
    
    filename := os.Args[1]
    
    // 读取JSON文件
    data, err := os.ReadFile(filename)
    if err != nil {
        fmt.Printf("读取文件失败: %v\n", err)
        os.Exit(1)
    }
    
    // 解析JSON
    var testData struct {
        Content []string `json:"content"`
        Type    string   `json:"type"`
    }
    
    if err := json.Unmarshal(data, &testData); err != nil {
        fmt.Printf("JSON解析失败: %v\n", err)
        os.Exit(1)
    }
    
    fmt.Printf("🔍 原始数据解析结果:\n")
    fmt.Printf("  类型: %s\n", testData.Type)
    fmt.Printf("  内容数组长度: %d\n", len(testData.Content))
    
    // 模拟AI响应结构
    response := QianwenResponse{
        StructuredTextArray: []struct {
            Type    string      `json:"type"`
            Content interface{} `json:"content"`
        }{
            {
                Type:    testData.Type,
                Content: testData.Content,
            },
        },
    }
    
    fmt.Printf("\n🔍 模拟AI响应处理:\n")
    
    // 处理结构化内容
    var elements []Element
    
    for i, item := range response.StructuredTextArray {
        fmt.Printf("🔍 处理元素 %d，类型: %s\n", i, item.Type)
        
        if item.Type == "title" {
            fmt.Printf("🔍 跳过title类型\n")
            continue
        }
        
        var content interface{}
        switch v := item.Content.(type) {
        case string:
            content = v
            fmt.Printf("🔍 字符串内容，长度: %d\n", len(v))
        case []interface{}:
            var listItems []string
            fmt.Printf("🔍 列表内容，原始长度: %d\n", len(v))
            for j, listItem := range v {
                if str, ok := listItem.(string); ok {
                    listItems = append(listItems, str)
                    fmt.Printf("🔍 列表项 %d: %s\n", j, str[:min(len(str), 50)]+"...")
                }
            }
            content = listItems
            fmt.Printf("🔍 列表转换完成，最终长度: %d\n", len(listItems))
        case []string:
            fmt.Printf("🔍 字符串列表，长度: %d\n", len(v))
            for j, str := range v {
                fmt.Printf("🔍 列表项 %d: %s\n", j, str[:min(len(str), 50)]+"...")
            }
            content = v
        default:
            content = fmt.Sprintf("%v", v)
            fmt.Printf("🔍 默认处理，类型: %T\n", v)
        }
        
        elements = append(elements, Element{
            Type:    item.Type,
            Content: content,
        })
        fmt.Printf("🔍 元素添加完成，总数: %d\n", len(elements))
    }
    
    fmt.Printf("\n🎯 最终结果:\n")
    fmt.Printf("  处理后元素数量: %d\n", len(elements))
    
    // 检查最后的list内容
    for _, element := range elements {
        if element.Type == "list" {
            if items, ok := element.Content.([]string); ok {
                fmt.Printf("  List元素包含项目数: %d\n", len(items))
                fmt.Printf("  最后一项内容: %s\n", items[len(items)-1])
            }
        }
    }
}

func min(a, b int) int {
    if a < b {
        return a
    }
    return b
}
EOF

echo -e "${GREEN}✅ Go测试文件创建完成: $TEST_GO_FILE${NC}"

# 运行Go测试
echo -e "${BLUE}🚀 运行Go测试...${NC}"
go run "$TEST_GO_FILE" "$TEST_JSON_FILE"

echo ""

# 清理文件
echo -e "${BLUE}🧹 清理测试文件...${NC}"
read -p "是否删除测试文件? (y/N): " -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
    rm -f "$TEST_JSON_FILE" "$TEST_GO_FILE"
    echo -e "${GREEN}✅ 测试文件已删除${NC}"
else
    echo -e "${YELLOW}⚠️ 测试文件保留: $TEST_JSON_FILE, $TEST_GO_FILE${NC}"
fi

echo ""
echo -e "${GREEN}🎯 调试完成！${NC}"
echo "如果Go测试显示所有7条内容都正确处理，问题可能在:"
echo "1. 分页算法计算"
echo "2. HTML模板渲染"
echo "3. 高度测量截断"
echo ""
echo "请查看实际的应用日志来进一步确定问题所在。"
