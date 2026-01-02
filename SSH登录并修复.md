# SSH登录服务器并修复404问题

## 步骤1：打开终端

在Mac上，您可以：
- 按 `Command + Space` 搜索 "终端" 或 "Terminal"
- 或者在 应用程序 > 实用工具 > 终端

## 步骤2：SSH登录

在终端中输入以下命令：

```bash
ssh root@62.234.165.77
```

**会提示：**
```
root@62.234.165.77's password:
```

输入您的服务器root密码（**输入时不会显示任何字符，这是正常的**），然后按回车。

## 步骤3：执行修复命令

登录成功后，**完整复制粘贴**以下命令（这是一整条命令）：

```bash
cd /root/numind-server && mkdir -p nginx web && cat > nginx/nginx.conf <<'EOF'
user nginx;
worker_processes auto;
error_log /var/log/nginx/error.log warn;
pid /var/run/nginx.pid;
events { worker_connections 1024; }
http {
    include /etc/nginx/mime.types;
    default_type application/octet-stream;
    sendfile on;
    keepalive_timeout 65;
    client_max_body_size 100M;
    server {
        listen 9200;
        root /usr/share/nginx/html;
        index index.html login.html;
        location ~ ^/(v1|api)/ {
            proxy_pass http://numind-server:8000;
            proxy_set_header Host $host;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_http_version 1.1;
            proxy_set_header Upgrade $http_upgrade;
            proxy_set_header Connection "upgrade";
        }
        location = /healthz { proxy_pass http://numind-server:8000; }
        location / { try_files $uri $uri/ /index.html; }
    }
}
EOF
LOGIN=$(find /root -name "login.html" -type f 2>/dev/null | grep -v node_modules | head -1) && [ -n "$LOGIN" ] && cp -r $(dirname "$LOGIN")/* web/ && grep -q "./web:/usr/share/nginx/html" docker-compose.yml || sed -i '/nginx:/,/networks:/{ /volumes:/a\      - ./web:/usr/share/nginx/html:ro' docker-compose.yml && docker-compose down && sleep 2 && docker-compose up -d && sleep 10 && echo "✅ 修复完成！请访问 http://62.234.165.77:9200/login.html"
```

**粘贴后按回车执行。**

## 步骤4：等待完成

您会看到类似这样的输出：
```
Stopping numind-nginx ... done
Stopping numind-server ... done
Creating numind-nginx ... done
Creating numind-server ... done
✅ 修复完成！请访问 http://62.234.165.77:9200/login.html
```

## 步骤5：测试

打开浏览器访问：
```
http://62.234.165.77:9200/login.html
```

登录后应该不会再出现 "Page not found." 错误了！

---

## 💡 遇到问题？

### 问题1：输入密码后提示 "Permission denied"
- **原因**：密码输入错误
- **解决**：重新输入正确的密码

### 问题2：提示 "Connection refused" 或 "Connection timeout"
- **原因**：网络问题或服务器防火墙
- **解决**：检查服务器是否开启，防火墙是否允许SSH（22端口）

### 问题3：命令执行后没有输出
- **原因**：命令可能正在执行中
- **解决**：等待15-30秒，应该会有输出

### 问题4：找不到login.html文件
执行以下命令查找：
```bash
find /root -name "*.html" -type f 2>/dev/null
```

然后手动复制到web目录：
```bash
cp /path/to/your/frontend/*.html /root/numind-server/web/
cp /path/to/your/frontend/*.css /root/numind-server/web/
cp /path/to/your/frontend/*.js /root/numind-server/web/
docker-compose restart nginx
```

---

## 📞 需要帮助？

如果您在SSH登录或执行命令时遇到任何问题，请告诉我具体的错误信息！
