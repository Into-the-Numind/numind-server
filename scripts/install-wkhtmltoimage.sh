#!/bin/bash

# 安装wkhtmltoimage工具脚本
# 支持Ubuntu/Debian、CentOS/RHEL、macOS等主流操作系统

set -e

echo "🚀 开始安装 wkhtmltoimage..."

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

# 安装Ubuntu/Debian版本
install_ubuntu() {
    echo "📦 检测到Ubuntu/Debian系统，开始安装..."
    
    # 更新包列表
    sudo apt-get update
    
    # 安装依赖包
    sudo apt-get install -y wget fontconfig libfreetype6 libjpeg-turbo8 libpng16-16 libx11-6 libxcb1 libxext6 libxrender1 xfonts-75dpi xfonts-base
    
    # 下载并安装wkhtmltopdf包（包含wkhtmltoimage）
    WKHTML_VERSION="0.12.6.1-2"
    WKHTML_PACKAGE="wkhtmltox_${WKHTML_VERSION}.jammy_amd64.deb"
    WKHTML_URL="https://github.com/wkhtmltopdf/packaging/releases/download/0.12.6.1-2/${WKHTML_PACKAGE}"
    
    echo "⬇️  下载 wkhtmltopdf 包..."
    wget -O "/tmp/${WKHTML_PACKAGE}" "${WKHTML_URL}"
    
    echo "📦 安装 wkhtmltopdf 包..."
    sudo dpkg -i "/tmp/${WKHTML_PACKAGE}" || sudo apt-get install -f -y
    
    # 清理临时文件
    rm -f "/tmp/${WKHTML_PACKAGE}"
}

# 安装CentOS/RHEL版本
install_centos() {
    echo "📦 检测到CentOS/RHEL系统，开始安装..."
    
    # 安装依赖包
    sudo yum install -y wget fontconfig freetype libjpeg-turbo libpng libX11 libXext libXrender xorg-x11-fonts-75dpi xorg-x11-fonts-Type1
    
    # 下载并安装wkhtmltopdf包
    WKHTML_VERSION="0.12.6.1-2"
    WKHTML_PACKAGE="wkhtmltox-${WKHTML_VERSION}.centos8.x86_64.rpm"
    WKHTML_URL="https://github.com/wkhtmltopdf/packaging/releases/download/0.12.6.1-2/${WKHTML_PACKAGE}"
    
    echo "⬇️  下载 wkhtmltopdf 包..."
    wget -O "/tmp/${WKHTML_PACKAGE}" "${WKHTML_URL}"
    
    echo "📦 安装 wkhtmltopdf 包..."
    sudo rpm -ivh "/tmp/${WKHTML_PACKAGE}"
    
    # 清理临时文件
    rm -f "/tmp/${WKHTML_PACKAGE}"
}

# 安装macOS版本
install_macos() {
    echo "📦 检测到macOS系统，开始安装..."
    
    # 检查是否有Homebrew
    if command -v brew &> /dev/null; then
        echo "🍺 使用Homebrew安装..."
        brew install wkhtmltopdf
    else
        echo "❌ 请先安装Homebrew或手动安装wkhtmltopdf"
        echo "Homebrew安装命令: /bin/bash -c \"\$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)\""
        exit 1
    fi
}

# 验证安装
verify_installation() {
    echo "🔍 验证安装..."
    
    if command -v wkhtmltoimage &> /dev/null; then
        echo "✅ wkhtmltoimage 安装成功！"
        echo "版本信息："
        wkhtmltoimage --version
        
        # 测试基本功能
        echo "🧪 测试基本功能..."
        echo '<html><body><h1>测试页面</h1><p>如果你看到这个图片，说明wkhtmltoimage工作正常！</p></body></html>' > /tmp/test.html
        wkhtmltoimage --width 1080 --quality 90 /tmp/test.html /tmp/test.png
        
        if [ -f /tmp/test.png ]; then
            echo "✅ 功能测试通过！"
            rm -f /tmp/test.html /tmp/test.png
        else
            echo "❌ 功能测试失败"
            exit 1
        fi
    else
        echo "❌ wkhtmltoimage 安装失败"
        exit 1
    fi
}

# 主程序
main() {
    OS=$(detect_os)
    
    case $OS in
        ubuntu)
            install_ubuntu
            ;;
        centos)
            install_centos
            ;;
        macos)
            install_macos
            ;;
        *)
            echo "❌ 不支持的操作系统: $OSTYPE"
            echo "请手动安装 wkhtmltoimage"
            echo "官方下载地址: https://wkhtmltopdf.org/downloads.html"
            exit 1
            ;;
    esac
    
    verify_installation
    
    echo ""
    echo "🎉 wkhtmltoimage 安装完成！"
    echo "现在可以使用轻量级渲染器替代无头浏览器了。"
}

# 执行主程序
main "$@"
