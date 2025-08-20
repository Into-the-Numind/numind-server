#!/bin/bash

# wkhtmltoimage 替代方案安装脚本
# 由于原版本在某些系统中不再维护，提供多种替代方案

set -e

echo "🚀 wkhtmltoimage 替代方案安装"
echo "====================================="

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

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

# 检测操作系统
detect_os() {
    if [[ "$OSTYPE" == "linux-gnu"* ]]; then
        if [ -f /etc/debian_version ]; then
            echo "ubuntu"
        elif [ -f /etc/redhat-release ]; then
            echo "centos"
        else
            echo "linux"
        fi
    elif [[ "$OSTYPE" == "darwin"* ]]; then
        echo "macos"
    else
        echo "unknown"
    fi
}

# 方案1: 尝试从官方GitHub下载二进制文件
install_from_github() {
    log_info "方案1: 从GitHub下载官方二进制文件"
    
    OS=$(detect_os)
    case $OS in
        macos)
            DOWNLOAD_URL="https://github.com/wkhtmltopdf/packaging/releases/download/0.12.6.1-2/wkhtmltox-0.12.6.1-2.macos-cocoa.pkg"
            FILENAME="wkhtmltox.pkg"
            ;;
        ubuntu)
            DOWNLOAD_URL="https://github.com/wkhtmltopdf/packaging/releases/download/0.12.6.1-2/wkhtmltox_0.12.6.1-2.jammy_amd64.deb"
            FILENAME="wkhtmltox.deb"
            ;;
        *)
            log_error "不支持的操作系统: $OS"
            return 1
            ;;
    esac
    
    log_info "下载 $FILENAME..."
    if curl -L -o "/tmp/$FILENAME" "$DOWNLOAD_URL"; then
        case $OS in
            macos)
                log_info "安装 .pkg 文件..."
                sudo installer -pkg "/tmp/$FILENAME" -target /
                ;;
            ubuntu)
                log_info "安装 .deb 文件..."
                sudo dpkg -i "/tmp/$FILENAME" || sudo apt-get install -f -y
                ;;
        esac
        
        rm -f "/tmp/$FILENAME"
        
        if command -v wkhtmltoimage &> /dev/null; then
            log_success "GitHub方案安装成功"
            return 0
        fi
    fi
    
    log_error "GitHub方案安装失败"
    return 1
}

# 方案2: 使用Docker容器方式
install_docker_solution() {
    log_info "方案2: Docker容器方案"
    
    if ! command -v docker &> /dev/null; then
        log_error "Docker 未安装，无法使用此方案"
        return 1
    fi
    
    # 创建Docker包装脚本
    cat > /usr/local/bin/wkhtmltoimage << 'EOF'
#!/bin/bash
docker run --rm -v "$(pwd)":/workspace surnet/alpine-wkhtmltopdf:3.20.2-0.12.6-small wkhtmltoimage "$@"
EOF
    
    chmod +x /usr/local/bin/wkhtmltoimage
    
    # 预拉取Docker镜像
    log_info "拉取Docker镜像..."
    docker pull surnet/alpine-wkhtmltopdf:3.20.2-0.12.6-small
    
    if /usr/local/bin/wkhtmltoimage --version &> /dev/null; then
        log_success "Docker方案配置成功"
        return 0
    fi
    
    log_error "Docker方案配置失败"
    return 1
}

# 方案3: 推荐替代技术栈
recommend_alternatives() {
    log_warning "推荐替代技术栈"
    echo ""
    echo "由于 wkhtmltopdf 不再维护，建议考虑以下现代化替代方案："
    echo ""
    echo "🔶 Puppeteer (Node.js)"
    echo "   - 使用无头Chrome"
    echo "   - 活跃维护，功能强大"
    echo "   - 可通过 Go 调用"
    echo ""
    echo "🔶 Playwright (多语言)"
    echo "   - 支持多种浏览器"
    echo "   - 现代化API"
    echo "   - 有Go绑定"
    echo ""
    echo "🔶 Chrome DevTools Protocol"
    echo "   - 直接使用CDP协议"
    echo "   - 保持现有chromedp集成"
    echo "   - 但优化配置和使用方式"
    echo ""
    echo "🔶 Server-side渲染方案"
    echo "   - 使用Cairo/Skia等图形库"
    echo "   - 纯Go实现，无外部依赖"
    echo "   - 需要重新实现布局引擎"
    echo ""
}

# 方案4: 创建Go原生渲染器
create_go_native_renderer() {
    log_info "方案4: 创建Go原生简单渲染器"
    
    cat > "go_native_renderer.go" << 'EOF'
package main

import (
    "fmt"
    "image"
    "image/color"
    "image/draw"
    "image/png"
    "os"
    
    "golang.org/x/image/font"
    "golang.org/x/image/font/basicfont"
    "golang.org/x/image/math/fixed"
)

// SimpleGoRenderer 简单的Go原生渲染器
// 作为wkhtmltoimage不可用时的备用方案
type SimpleGoRenderer struct {
    width  int
    height int
}

func NewSimpleGoRenderer(width, height int) *SimpleGoRenderer {
    return &SimpleGoRenderer{
        width:  width,
        height: height,
    }
}

func (r *SimpleGoRenderer) RenderTextToImage(text string, filename string) error {
    // 创建图片
    img := image.NewRGBA(image.Rect(0, 0, r.width, r.height))
    
    // 设置白色背景
    draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{255, 255, 255, 255}}, image.Point{}, draw.Src)
    
    // 设置字体和颜色
    face := basicfont.Face7x13
    col := color.RGBA{0, 0, 0, 255}
    
    // 绘制文本
    point := fixed.Point26_6{
        X: fixed.I(20),
        Y: fixed.I(50),
    }
    
    d := &font.Drawer{
        Dst:  img,
        Src:  image.NewUniform(col),
        Face: face,
        Dot:  point,
    }
    
    d.DrawString(text)
    
    // 保存图片
    file, err := os.Create(filename)
    if err != nil {
        return err
    }
    defer file.Close()
    
    return png.Encode(file, img)
}

func main() {
    renderer := NewSimpleGoRenderer(1080, 1440)
    err := renderer.RenderTextToImage("Hello, World!\nThis is a simple Go native renderer.", "test_output.png")
    if err != nil {
        fmt.Printf("Error: %v\n", err)
    } else {
        fmt.Println("✅ Go native renderer test successful")
        fmt.Println("Output saved to: test_output.png")
    }
}
EOF

    log_info "创建Go原生渲染器示例..."
    if go run go_native_renderer.go; then
        log_success "Go原生渲染器测试成功"
        log_info "这个方案可以作为基础，扩展支持更复杂的布局"
    else
        log_error "Go原生渲染器测试失败"
    fi
    
    # 清理测试文件
    rm -f go_native_renderer.go test_output.png
}

# 主函数
main() {
    log_info "尝试多种安装方案..."
    
    # 首先检查是否已安装
    if command -v wkhtmltoimage &> /dev/null; then
        log_success "wkhtmltoimage 已安装: $(wkhtmltoimage --version | head -n1)"
        return 0
    fi
    
    # 尝试方案1: GitHub下载
    if install_from_github; then
        return 0
    fi
    
    log_warning "GitHub方案失败，尝试其他方案..."
    
    # 尝试方案2: Docker
    if install_docker_solution; then
        return 0
    fi
    
    log_warning "Docker方案失败，展示替代方案..."
    
    # 方案3: 推荐替代方案
    recommend_alternatives
    
    # 方案4: Go原生示例
    create_go_native_renderer
    
    echo ""
    log_warning "wkhtmltoimage 安装失败，但已提供多种替代方案"
    log_info "建议根据项目需求选择最适合的技术栈"
    
    return 1
}

main "$@"
