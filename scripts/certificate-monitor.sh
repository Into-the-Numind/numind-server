#!/bin/bash

# 微信支付证书监控脚本
# 用于定期检查证书状态并发送告警

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 配置
CERT_DIR="/Users/neozhang/go/src/github.com/Into-the-Numind/numind-server/configs/cert"
CONFIG_FILE="config_local.yaml"
LOG_FILE="certificate-monitor.log"
ALERT_EMAIL="admin@example.com"  # 替换为实际的告警邮箱

# 获取配置
get_config() {
    if [ -f "$CONFIG_FILE" ]; then
        CONFIG_SERIAL_NO=$(grep "mch_cert_serial_no:" "$CONFIG_FILE" | awk '{print $2}' | tr -d '"')
        CERT_PATH=$(grep "wechatpay_cert_path:" "$CONFIG_FILE" | awk '{print $2}' | tr -d '"')
        KEY_PATH=$(grep "mch_private_key_path:" "$CONFIG_FILE" | awk '{print $2}' | tr -d '"')
    else
        echo -e "${RED}配置文件不存在: $CONFIG_FILE${NC}"
        exit 1
    fi
}

# 检查证书状态
check_certificate() {
    echo -e "\n${YELLOW}检查证书状态...${NC}"
    
    if [ ! -f "$CERT_PATH" ]; then
        echo -e "${RED}✗ 证书文件不存在: $CERT_PATH${NC}"
        send_alert "证书文件不存在" "证书文件 $CERT_PATH 不存在，请检查配置"
        return 1
    fi
    
    if [ ! -f "$KEY_PATH" ]; then
        echo -e "${RED}✗ 私钥文件不存在: $KEY_PATH${NC}"
        send_alert "私钥文件不存在" "私钥文件 $KEY_PATH 不存在，请检查配置"
        return 1
    fi
    
    # 获取证书序列号
    CERT_SERIAL_NO=$(openssl x509 -in "$CERT_PATH" -noout -serial 2>/dev/null | cut -d'=' -f2)
    if [ -z "$CERT_SERIAL_NO" ]; then
        echo -e "${RED}✗ 无法获取证书序列号${NC}"
        send_alert "证书序列号获取失败" "无法从证书文件获取序列号"
        return 1
    fi
    
    # 检查序列号是否匹配
    if [ "$CONFIG_SERIAL_NO" != "$CERT_SERIAL_NO" ]; then
        echo -e "${RED}✗ 证书序列号不匹配${NC}"
        echo -e "${YELLOW}配置文件中的序列号: $CONFIG_SERIAL_NO${NC}"
        echo -e "${YELLOW}证书中的序列号: $CERT_SERIAL_NO${NC}"
        send_alert "证书序列号不匹配" "配置文件中的序列号与证书序列号不匹配"
        return 1
    fi
    
    # 获取证书有效期
    VALID_FROM=$(openssl x509 -in "$CERT_PATH" -noout -startdate 2>/dev/null | cut -d'=' -f2)
    VALID_TO=$(openssl x509 -in "$CERT_PATH" -noout -enddate 2>/dev/null | cut -d'=' -f2)
    
    # 计算剩余天数
    CURRENT_DATE=$(date +%s)
    EXPIRE_DATE=$(date -d "$VALID_TO" +%s 2>/dev/null || echo "0")
    DAYS_TO_EXPIRE=$(( (EXPIRE_DATE - CURRENT_DATE) / 86400 ))
    
    echo -e "${GREEN}✓ 证书序列号匹配${NC}"
    echo -e "${BLUE}证书有效期: $VALID_FROM 至 $VALID_TO${NC}"
    echo -e "${BLUE}剩余天数: $DAYS_TO_EXPIRE${NC}"
    
    # 检查是否即将过期
    if [ $DAYS_TO_EXPIRE -le 0 ]; then
        echo -e "${RED}✗ 证书已过期${NC}"
        send_alert "证书已过期" "微信支付证书已过期，请立即更新"
        return 1
    elif [ $DAYS_TO_EXPIRE -le 30 ]; then
        echo -e "${RED}✗ 证书将在 $DAYS_TO_EXPIRE 天后过期（紧急）${NC}"
        send_alert "证书即将过期（紧急）" "微信支付证书将在 $DAYS_TO_EXPIRE 天后过期，请立即更新"
        return 1
    elif [ $DAYS_TO_EXPIRE -le 90 ]; then
        echo -e "${YELLOW}⚠ 证书将在 $DAYS_TO_EXPIRE 天后过期（警告）${NC}"
        send_alert "证书即将过期（警告）" "微信支付证书将在 $DAYS_TO_EXPIRE 天后过期，建议及时更新"
    elif [ $DAYS_TO_EXPIRE -le 180 ]; then
        echo -e "${YELLOW}⚠ 证书将在 $DAYS_TO_EXPIRE 天后过期（提醒）${NC}"
        send_alert "证书即将过期（提醒）" "微信支付证书将在 $DAYS_TO_EXPIRE 天后过期，建议在到期前6个月进行更新"
    else
        echo -e "${GREEN}✓ 证书状态正常${NC}"
    fi
    
    return 0
}

# 发送告警
send_alert() {
    local subject="$1"
    local message="$2"
    local timestamp=$(date '+%Y-%m-%d %H:%M:%S')
    
    echo -e "\n${RED}告警: $subject${NC}"
    echo -e "${YELLOW}时间: $timestamp${NC}"
    echo -e "${YELLOW}消息: $message${NC}"
    
    # 记录到日志文件
    echo "[$timestamp] ALERT: $subject - $message" >> "$LOG_FILE"
    
    # 这里可以添加发送邮件、短信或其他告警方式的代码
    # 例如：mail -s "$subject" "$ALERT_EMAIL" <<< "$message"
    
    echo -e "${BLUE}告警已记录到日志文件: $LOG_FILE${NC}"
}

# 主函数
main() {
    echo -e "${YELLOW}微信支付证书监控脚本${NC}"
    echo "=========================="
    echo -e "${BLUE}时间: $(date)${NC}"
    
    # 获取配置
    get_config
    
    # 检查证书状态
    if check_certificate; then
        echo -e "\n${GREEN}证书检查完成，状态正常${NC}"
    else
        echo -e "\n${RED}证书检查完成，发现问题${NC}"
        exit 1
    fi
}

# 运行主函数
main "$@" 