# API 架构总结

## 系统架构

本系统包含两个独立的应用：

### 1. 后台管理系统（Admin）
- **路由前缀**: `/v1/admin/`
- **认证**: Admin Token (JWT)
- **用户**: 管理员
- **功能**: 管理模板、节点、查看所有数据

### 2. 有数Web前端（User）
- **路由前缀**: `/v1/`
- **认证**: User Token (JWT)
- **用户**: 普通用户
- **功能**: 执行SOP、查看自己的数据

---

## 登录API

### 后台管理登录
```bash
POST /v1/admin/login

{
  "username": "admin",
  "password": "admin123456"
}

# 返回 admin_token
```

### 有数Web登录
```bash
POST /v1/web/login

{
  "username": "user123",
  "password": "admin123456"
}

# 返回 user_token
```

---

## SOP 功能API

### 后台管理系统（需要admin_token）

**模板管理**:
- `POST /v1/admin/sop/templates` - 创建模板
- `GET /v1/admin/sop/templates` - 查询所有模板
- `GET /v1/admin/sop/templates/:id` - 查询模板详情
- `PUT /v1/admin/sop/templates/:id` - 更新模板
- `DELETE /v1/admin/sop/templates/:id` - 删除模板

**节点管理**:
- `POST /v1/admin/sop/nodes` - 创建节点
- `GET /v1/admin/sop/templates/:id/nodes` - 查询模板的所有节点
- `GET /v1/admin/sop/nodes/:id` - 查询节点详情
- `PUT /v1/admin/sop/nodes/:id` - 更新节点
- `DELETE /v1/admin/sop/nodes/:id` - 删除节点

**执行管理**:
- `POST /v1/admin/sop/templates/:id/run` - 为指定用户执行模板
- `GET /v1/admin/sop/runs` - 查询所有执行记录
- `GET /v1/admin/sop/runs/:id` - 查询执行记录
- `GET /v1/admin/sop/runs/:id/detail` - 查询执行详情

**笔记管理**:
- `GET /v1/admin/sop/notes/:id` - 查询笔记
- `GET /v1/admin/sop/users/:user_id/notes` - 查询指定用户的笔记

### 有数Web前端（需要user_token）

**模板查询**:
- `GET /v1/sop/templates` - 获取可用模板（只返回active状态）

**执行SOP**:
- `POST /v1/sop/templates/:id/execute` - 执行模板
  ```json
  {
    "initial_input": "产品介绍..."
  }
  ```
  注意：user_id自动从token获取

**查询记录**:
- `GET /v1/sop/runs` - 我的执行记录列表
- `GET /v1/sop/runs/:id` - 我的执行记录详情
- `GET /v1/sop/runs/:id/detail` - 我的执行详情（含所有节点）

**查询笔记**:
- `GET /v1/sop/notes` - 我的笔记列表
- `GET /v1/sop/notes/:id` - 我的笔记详情

---

## 使用场景

### 场景1：管理员配置SOP模板

1. 登录后台: `POST /v1/admin/login`
2. 创建模板: `POST /v1/admin/sop/templates`
3. 创建节点1: `POST /v1/admin/sop/nodes` (sort=1, 拆解产品)
4. 创建节点2: `POST /v1/admin/sop/nodes` (sort=2, 拆解文案)
5. 创建节点3: `POST /v1/admin/sop/nodes` (sort=3, 拆解风格)
6. 创建节点4: `POST /v1/admin/sop/nodes` (sort=4, 生成文稿)

### 场景2：用户使用SOP（对应sop-detail.html）

1. 登录有数Web: `POST /v1/web/login`
2. 查看可用模板: `GET /v1/sop/templates`
3. 输入产品介绍，点击执行: `POST /v1/sop/templates/1/execute`
   ```json
   {
     "initial_input": "这是一款智能手表..."
   }
   ```
4. 获得run_id，轮询查看进度: `GET /v1/sop/runs/1/detail`
5. 查看节点执行结果:
   ```json
   {
     "run": {"status": "running"},
     "node_runs": [
       {"sort": 0, "status": "succeeded", "output": "产品分析..."},
       {"sort": 1, "status": "running", "output": ""},
       {"sort": 2, "status": "pending", "output": ""},
       {"sort": 3, "status": "pending", "output": ""}
     ]
   }
   ```
6. 所有节点完成后，查看最终笔记

### 场景3：用户回看历史记录

1. 查看历史执行: `GET /v1/sop/runs`
2. 选择某次执行: `GET /v1/sop/runs/123/detail`
3. 查看所有节点的输入输出（完整回放）
4. 查看最终笔记: `GET /v1/sop/notes/456`

---

## 前端集成建议（sop-detail.html）

### 执行流程
```javascript
// 1. 执行SOP
const response = await fetch('/v1/sop/templates/1/execute', {
  method: 'POST',
  headers: {
    'Authorization': 'Bearer ' + userToken,
    'Content-Type': 'application/json'
  },
  body: JSON.stringify({
    initial_input: productIntro
  })
});

const { data } = await response.json();
const runId = data.id;

// 2. 轮询查看进度
const intervalId = setInterval(async () => {
  const detailRes = await fetch(`/v1/sop/runs/${runId}/detail`, {
    headers: { 'Authorization': 'Bearer ' + userToken }
  });
  
  const { data } = await detailRes.json();
  const { run, node_runs } = data;
  
  // 更新UI显示每个节点的状态和输出
  updateNodeStatus(node_runs);
  
  // 如果完成或失败，停止轮询
  if (run.status === 'succeeded' || run.status === 'failed') {
    clearInterval(intervalId);
    
    if (run.status === 'succeeded') {
      // 显示最终笔记
      displayFinalNote(run.final_note_id);
    }
  }
}, 2000); // 每2秒查询一次
```

---

## 数据隔离保证

1. **用户端API**: 自动从token获取user_id，无法访问其他用户数据
2. **管理端API**: 可以指定user_id，可以查看所有数据
3. **conversation_id**: 每次执行独立隔离，不会与其他对话混淆

---

## 文件清单

### 新增文件
1. `internal/numind/controller/v1/sop/sop.go` - 用户端SOP控制器
2. `SOP_API_ARCHITECTURE.md` - API架构说明文档
3. `API_SUMMARY.md` - 本文档

### 已有文件
1. `internal/numind/controller/v1/admin_sop/sop.go` - 后台管理SOP控制器
2. `internal/numind-admin/router.go` - 后台管理路由（包含 /v1/admin/login）
3. `internal/numind/router.go` - 用户端路由（包含 /v1/web/login 和 /v1/sop/*）
