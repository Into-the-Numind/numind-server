#!/bin/bash

# 验证所有配置文件中的超时设置
# 检查qianwen、wanxiang、volc的超时配置是否同步

echo "🔍 验证配置文件超时设置..."
echo "=================================="

# 检查所有配置文件
config_files=("config_local.yaml" "config_dev.yaml" "config_qa.yaml" "config_prod.yaml")

for config_file in "${config_files[@]}"; do
    echo ""
    echo "📁 $config_file:"
    
    if [ -f "$config_file" ]; then
        echo "✅ 文件存在"
        
        # 检查volc超时
        volc_timeout=$(grep "timeout:" "$config_file" | grep volc | head -1)
        if [ -n "$volc_timeout" ]; then
            echo "   🚀 Volc: $volc_timeout"
        else
            echo "   ❌ Volc: 未找到超时配置"
        fi
        
        # 检查ali.text超时
        ali_text_timeout=$(grep "timeout:" "$config_file" | grep -A1 "ali:" | grep "text:" | head -1)
        if [ -n "$ali_text_timeout" ]; then
            echo "   💬 Qianwen: $ali_text_timeout"
        else
            echo "   ❌ Qianwen: 未找到超时配置"
        fi
        
        # 检查ali.image超时
        ali_image_timeout=$(grep "timeout:" "$config_file" | grep -A2 "ali:" | grep "image:" | head -1)
        if [ -n "$ali_image_timeout" ]; then
            echo "   🎨 Wanxiang: $ali_image_timeout"
        else
            echo "   ❌ Wanxiang: 未找到超时配置"
        fi
        
    else
        echo "❌ 文件不存在"
    fi
done

echo ""
echo "=================================="
echo "📊 配置同步状态总结:"
echo ""

# 统计超时配置
echo "Volc API 超时配置:"
grep -r "timeout:" config_*.yaml | grep volc

echo ""
echo "千问文本生成超时配置:"
grep -r "timeout:" config_*.yaml | grep -A1 "ali:" | grep "text:"

echo ""
echo "万象图像生成超时配置:"
grep -r "timeout:" config_*.yaml | grep -A2 "ali:" | grep "image:"

echo ""
echo "=================================="
echo "🎯 验证完成！"
echo ""
echo "💡 预期结果:"
echo "- Volc: 120s"
echo "- Qianwen: 120s" 
echo "- Wanxiang: 180s"
echo ""
echo "📝 如果发现不一致，请检查配置文件格式和缩进"
