# crawl4ai 渲染服务部署

agent 的 `web_fetch` 工具优先用 crawl4ai 渲染网页（真实浏览器跑 JS → 干净 Markdown）；
crawl4ai 未配置或不可达时自动退回裸 HTTP 抓取（零回归）。本目录是它的部署清单。

- 镜像：`unclecode/crawl4ai:0.8.6`（pin 版本）
- 端口：`11235`
- 资源：建议宿主机预留 **≥4GB RAM**，`--shm-size=1g`（浏览器）

---

## dev 部署

```bash
# 在部署机上
docker compose -f deploy/crawl4ai/docker-compose.yml up -d

# 健康检查
curl -f http://localhost:11235/health
```

然后让后端指向它：把 **dev** 的 `crawl4ai.base_url` 填成后端可达的地址：

- 后端进程跑在宿主机上 → `http://localhost:11235`
- 后端跑在另一个容器里 → 用宿主机网关地址（如 `http://<部署机内网IP>:11235`，
  或 Docker Desktop 的 `http://host.docker.internal:11235`）。**注意**：不要把后端容器
  加入 `crawl4ai_net`——那会破坏隔离；用宿主机已发布端口访问。

改完 `config_dev.yaml` 的 `crawl4ai.base_url` 后重启后端（或重新部署）生效。
`base_url` 为空时 `web_fetch` 就是纯裸 HTTP（与上线前一致）。

---

## 网络隔离验证（spec §5 硬要求，S5 必做）

crawl4ai 只接入独立 bridge 网络 `crawl4ai_net`，不加入后端/DB 网络。验证它打不到内部容器：

```bash
# 找一个内部服务容器的 IP（如后端或 mysql）
docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}' numind-server-dev

# 从 crawl4ai 容器内尝试连它 —— 应当连接失败/超时（无路由）
docker exec crawl4ai curl -f --max-time 3 http://<上一步的内部容器IP>:9091/healthz ; echo "exit=$?"
# 期望：非 0 退出（connection refused / timeout）= 隔离生效
```

---

## prod 部署（运维手动，本 feature 不触 prod）

1. 同样用本 compose 起 crawl4ai 容器（建议 11235 只在内网/回环暴露，不对公网开放）。
2. **prod 配置**：本仓库规则禁止 AI 改 `config_prod.yaml`。运维在 prod 配置中**手动加**：
   ```yaml
   crawl4ai:
     base_url: "http://<prod 可达地址>:11235"
     token: ""
     timeout_seconds: 30
     content_filter: "fit"
   ```
   加之前 prod 的 `web_fetch` 维持裸 HTTP（无回归）；加之后重启后端自动启用渲染。
3. **出网硬化（强烈建议）**：在宿主机防火墙/安全组对 crawl4ai 容器的出站做限制——
   只放行公网，**封禁 RFC1918 内网段**（10/8、172.16/12、192.168/16）与云 metadata
   （169.254.169.254）。compose 的专网只隔离了"容器到容器"，无法限制经宿主机的出网；
   这一步是渲染路径 SSRF 的最终防线。

---

## 排障

- `web_fetch` 总走裸 HTTP？查后端日志 `crawl4ai render failed, falling back to raw HTTP`
  的 error 字段，或确认 `crawl4ai.base_url` 已配且容器 `/health` 正常。
- 渲染慢/超时：调 `crawl4ai.timeout_seconds`；查容器内存是否撞 4g 上限。
- 容器频繁重启：多半是 RAM/shm 不足，加大宿主机内存或 `shm_size`。
