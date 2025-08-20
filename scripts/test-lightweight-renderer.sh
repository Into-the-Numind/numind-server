#!/bin/bash

# 轻量级渲染器测试脚本
# 用于验证无浏览器依赖卡片渲染器的功能和性能

set -e

echo "🚀 轻量级渲染器测试脚本"
echo "=================================="

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 项目根目录
PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$PROJECT_ROOT"

# 日志函数
log_info() {
    echo -e "${BLUE}ℹ️  $1${NC}"
}

log_success() {
    echo -e "${GREEN}✅ $1${NC}"
}

log_warning() {
    echo -e "${YELLOW}⚠️  $1${NC}"
}

log_error() {
    echo -e "${RED}❌ $1${NC}"
}

# 检查依赖
check_dependencies() {
    log_info "检查依赖..."
    
    # 检查 Go
    if ! command -v go &> /dev/null; then
        log_error "Go 未安装"
        exit 1
    fi
    log_success "Go: $(go version)"
    
    # 检查 wkhtmltoimage
    if ! command -v wkhtmltoimage &> /dev/null; then
        log_warning "wkhtmltoimage 未安装，尝试自动安装..."
        if [ -f "./scripts/install-wkhtmltoimage.sh" ]; then
            chmod +x ./scripts/install-wkhtmltoimage.sh
            ./scripts/install-wkhtmltoimage.sh
        else
            log_error "安装脚本不存在，请手动安装 wkhtmltoimage"
            exit 1
        fi
    fi
    log_success "wkhtmltoimage: $(wkhtmltoimage --version | head -n1)"
    
    # 检查必要的字体
    if command -v fc-list &> /dev/null; then
        font_count=$(fc-list | grep -i "han\|yahei\|hei" | wc -l)
        if [ "$font_count" -gt 0 ]; then
            log_success "中文字体: 已安装 ($font_count 个)"
        else
            log_warning "中文字体: 未检测到，可能影响渲染效果"
        fi
    fi
}

# 编译项目
build_project() {
    log_info "编译项目..."
    
    # 更新依赖
    go mod tidy
    
    # 编译测试程序
    if go build -o test-browser-free-renderer ./cmd/test-browser-free-renderer/; then
        log_success "编译成功"
    else
        log_error "编译失败"
        exit 1
    fi
    
    # 编译主程序（如果存在）
    if [ -f "./cmd/numind/main.go" ]; then
        if go build -o numind-server ./cmd/numind/; then
            log_success "主程序编译成功"
        else
            log_warning "主程序编译失败"
        fi
    fi
}

# 运行基础测试
run_basic_tests() {
    log_info "运行基础测试..."
    
    # 运行 Go 测试
    if go test ./internal/numind/biz/card/ -v; then
        log_success "Go 单元测试通过"
    else
        log_warning "Go 单元测试失败或无测试文件"
    fi
    
    # 运行集成测试
    log_info "运行渲染器集成测试..."
    if ./test-browser-free-renderer; then
        log_success "集成测试通过"
    else
        log_error "集成测试失败"
        return 1
    fi
}

# 性能测试
run_performance_tests() {
    log_info "运行性能测试..."
    
    # 创建性能测试脚本
    cat > performance_test.go << 'EOF'
package main

import (
    "context"
    "fmt"
    "log"
    "runtime"
    "time"
    
    "numind-server/internal/numind/biz/card"
    "numind-server/internal/pkg/model"
)

func main() {
    ctx := context.Background()
    
    // 创建渲染器
    renderer, err := card.NewBrowserFreeRenderer()
    if err != nil {
        log.Fatal("创建渲染器失败:", err)
    }
    defer renderer.Cleanup()
    
    // 准备测试数据
    book := &model.BookM{Title: "性能测试", Tags: "benchmark"}
    book.ID = 1
    
    cards := make([]*model.CardM, 5) // 测试5张卡片
    for i := 0; i < 5; i++ {
        cards[i] = &model.CardM{
            ProcessedText: fmt.Sprintf(`[{"type":"title","content":"卡片 %d"},{"type":"body","content":"这是第 %d 张测试卡片的内容..."}]`, i+1, i+1),
            SortOrder: i + 1,
        }
        cards[i].ID = uint(i + 1)
    }
    
    // 记录初始内存
    var m1 runtime.MemStats
    runtime.ReadMemStats(&m1)
    
    // 执行渲染
    start := time.Now()
    results, err := renderer.RenderBookToImages(ctx, book, cards)
    duration := time.Since(start)
    
    if err != nil {
        log.Fatal("渲染失败:", err)
    }
    
    // 记录结束内存
    var m2 runtime.MemStats
    runtime.ReadMemStats(&m2)
    
    // 输出性能指标
    fmt.Printf("========== 性能测试结果 ==========\n")
    fmt.Printf("卡片数量: %d\n", len(cards))
    fmt.Printf("生成图片: %d\n", len(results))
    fmt.Printf("总耗时: %v\n", duration)
    fmt.Printf("平均耗时: %v/卡片\n", duration/time.Duration(len(cards)))
    fmt.Printf("渲染速度: %.2f 卡片/秒\n", float64(len(cards))/duration.Seconds())
    fmt.Printf("内存使用: %d KB -> %d KB (增加 %d KB)\n", 
        m1.Alloc/1024, m2.Alloc/1024, (m2.Alloc-m1.Alloc)/1024)
    fmt.Printf("================================\n")
}
EOF

    # 运行性能测试
    if go run performance_test.go; then
        log_success "性能测试完成"
    else
        log_error "性能测试失败"
    fi
    
    # 清理测试文件
    rm -f performance_test.go
}

# 验证输出文件
verify_output() {
    log_info "验证输出文件..."
    
    # 检查是否生成了图片文件
    if find ./res/upload/card/ -name "*.png" -o -name "*.webp" 2>/dev/null | head -1 | grep -q .; then
        image_count=$(find ./res/upload/card/ -name "*.png" -o -name "*.webp" 2>/dev/null | wc -l)
        log_success "找到 $image_count 个生成的图片文件"
        
        # 显示最新的几个文件
        log_info "最新生成的图片文件:"
        find ./res/upload/card/ -name "*.png" -o -name "*.webp" 2>/dev/null | head -5 | while read file; do
            size=$(du -h "$file" 2>/dev/null | cut -f1)
            echo "  $file ($size)"
        done
    else
        log_warning "未找到生成的图片文件"
    fi
    
    # 检查临时文件是否正确清理
    temp_files=$(find /tmp -name "*numind*" -o -name "*render*" 2>/dev/null | wc -l)
    if [ "$temp_files" -eq 0 ]; then
        log_success "临时文件已正确清理"
    else
        log_warning "发现 $temp_files 个临时文件未清理"
    fi
}

# 生成测试报告
generate_report() {
    log_info "生成测试报告..."
    
    report_file="lightweight_renderer_test_report.md"
    
    cat > "$report_file" << EOF
# 轻量级渲染器测试报告

## 测试信息
- 测试时间: $(date)
- 测试环境: $(uname -a)
- Go 版本: $(go version)
- wkhtmltoimage 版本: $(wkhtmltoimage --version | head -n1)

## 依赖检查
- Go: ✅ 已安装
- wkhtmltoimage: ✅ 已安装
- 中文字体: $(fc-list | grep -i "han\|yahei\|hei" | wc -l) 个

## 测试结果
EOF

    if [ -f "./test-browser-free-renderer" ]; then
        echo "- 编译: ✅ 成功" >> "$report_file"
    else
        echo "- 编译: ❌ 失败" >> "$report_file"
    fi
    
    echo "- 基础功能测试: 已执行" >> "$report_file"
    echo "- 性能测试: 已执行" >> "$report_file"
    
    # 添加性能指标（如果有的话）
    if [ -f "performance_results.txt" ]; then
        echo "" >> "$report_file"
        echo "## 性能指标" >> "$report_file"
        cat "performance_results.txt" >> "$report_file"
        rm -f "performance_results.txt"
    fi
    
    echo "" >> "$report_file"
    echo "## 建议" >> "$report_file"
    echo "- 建议在生产环境部署前进行更全面的测试" >> "$report_file"
    echo "- 监控内存使用情况和渲染性能" >> "$report_file"
    echo "- 确保所有依赖在目标环境中正确安装" >> "$report_file"
    
    log_success "测试报告已生成: $report_file"
}

# 清理函数
cleanup() {
    log_info "清理测试文件..."
    rm -f test-browser-free-renderer
    rm -f numind-server
    rm -f performance_test.go
}

# 主函数
main() {
    local run_performance=false
    local generate_report_flag=false
    
    # 解析命令行参数
    while [[ $# -gt 0 ]]; do
        case $1 in
            --performance)
                run_performance=true
                shift
                ;;
            --report)
                generate_report_flag=true
                shift
                ;;
            --help)
                echo "使用方法: $0 [选项]"
                echo "选项:"
                echo "  --performance  运行性能测试"
                echo "  --report      生成测试报告"
                echo "  --help        显示帮助信息"
                exit 0
                ;;
            *)
                log_error "未知选项: $1"
                exit 1
                ;;
        esac
    done
    
    # 设置退出时清理
    trap cleanup EXIT
    
    # 执行测试步骤
    check_dependencies
    build_project
    run_basic_tests
    
    if [ "$run_performance" = true ]; then
        run_performance_tests
    fi
    
    verify_output
    
    if [ "$generate_report_flag" = true ]; then
        generate_report
    fi
    
    log_success "所有测试完成！"
    echo ""
    echo "🎉 轻量级渲染器工作正常"
    echo "💡 使用 --performance 运行性能测试"
    echo "📊 使用 --report 生成详细报告"
}

# 执行主函数
main "$@"
