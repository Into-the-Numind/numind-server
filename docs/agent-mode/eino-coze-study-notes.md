# Eino / Coze Studio 源码深读笔记

> **版本**：cloudwego/eino @ 48e942b（2026-05-20 深读），coze-dev/coze-studio @ 22275b1（2026-05-20 深读）
>
> **目的**：为 agent-mode-phase0-verification A4 假设验证，回答 3 个必答问题，输出对 #3 tool-registry feature 的直接影响。

---

## §1 Eino Graph API 心智模型

### 1.1 State 传递机制（必答问题 Q1）

**核心结论**：Eino 的 State 是通过 Go `context.Context` 携带的，存在一个链式结构（`internalState.parent` 指针），支持嵌套图的词法作用域；并发安全由每个 State 节点持有独立 `sync.Mutex` 保证，**不是**全局锁。节点对 State 的访问必须通过 `compose.ProcessState[S]()` 泛型函数，框架自动加锁/解锁。

**关键代码**：`cloudwego/eino/compose/state.go` @ 48e942b

**引用代码 1**（第 32-72 行）：

```go
// state.go @ cloudwego/eino:48e942b, lines 32-72

type stateKey struct{}

type internalState struct {
    state  any
    mu     sync.Mutex
    parent *internalState
}

// StatePreHandler is a function called before the node is executed.
// Notice: if user called Stream but with StatePreHandler, the StatePreHandler will read all stream chunks and merge them into a single object.
type StatePreHandler[I, S any] func(ctx context.Context, in I, state S) (I, error)

// StatePostHandler is a function called after the node is executed.
// Notice: if user called Stream but with StatePostHandler, the StatePostHandler will read all stream chunks and merge them into a single object.
type StatePostHandler[O, S any] func(ctx context.Context, out O, state S) (O, error)

// StreamStatePreHandler is a function that is called before the node is executed with stream input and output.
type StreamStatePreHandler[I, S any] func(ctx context.Context, in *schema.StreamReader[I], state S) (*schema.StreamReader[I], error)

// StreamStatePostHandler is a function that is called after the node is executed with stream input and output.
type StreamStatePostHandler[O, S any] func(ctx context.Context, out *schema.StreamReader[O], state S) (*schema.StreamReader[O], error)

func convertPreHandler[I, S any](handler StatePreHandler[I, S]) *composableRunnable {
    rf := func(ctx context.Context, in I, opts ...any) (I, error) {
        cState, pMu, err := getState[S](ctx)
        if err != nil {
            return in, err
        }
        pMu.Lock()
        defer pMu.Unlock()

        return handler(ctx, in, cState)
    }

    return runnableLambda[I, I](rf, nil, nil, nil, false)
}
```

**解读**：

- `internalState` 是 State 容器的内部表示，`state any` 字段存用户定义的 struct（如 `*AgentState`），`mu` 是该层 State 的锁，`parent` 指向外层图的 State（嵌套图时使用）。
- `stateKey{}` 是 context key，State 通过 `context.WithValue(ctx, stateKey{}, &internalState{...})` 注入，全程随 ctx 流动。
- 每个节点的 PreHandler/PostHandler 都会调用 `getState[S](ctx)` 获取强类型 State，并在调用 handler 前加锁、调用后解锁，保证并发安全。
- **关键 gotcha**：PreHandler 接收的是 State **本身**（指针），不是副本，修改即持久化。PostHandler 同理。

**引用代码 2**（`getState` + `ProcessState`，第 165-196 行）：

```go
// state.go @ cloudwego/eino:48e942b, lines 165-196

// ProcessState processes the state from the context in a concurrency-safe way.
// This is the recommended way to access and modify state in custom nodes.
// The provided function handler will be executed with exclusive access to the state (protected by mutex).
//
// State Lookup Behavior:
// - If the requested state type exists in the current graph, it will be returned
// - If not found in current graph, ProcessState will search in parent graph states (for nested graphs)
// - This enables nested graphs to access state from their parent graphs
// - Follows lexical scoping: inner state of the same type shadows outer state
func ProcessState[S any](ctx context.Context, handler func(context.Context, S) error) error {
    s, pMu, err := getState[S](ctx)
    if err != nil {
        return fmt.Errorf("get state from context fail: %w", err)
    }
    pMu.Lock()
    defer pMu.Unlock()
    return handler(ctx, s)
}

func getState[S any](ctx context.Context) (S, *sync.Mutex, error) {
    state := ctx.Value(stateKey{})

    if state == nil {
        var s S
        return s, nil, fmt.Errorf("have not set state")
    }

    interState := state.(*internalState)

    for interState != nil {
        if cState, ok := interState.state.(S); ok {
            return cState, &interState.mu, nil
        }
        interState = interState.parent
    }

    var s S
    return s, nil, fmt.Errorf("cannot find state with type: %v in states chain, "+
        "current state type: %v",
        generic.TypeOf[S](), reflect.TypeOf(state.(*internalState).state))
}
```

**解读**：

- `getState[S]` 用 Go 泛型做类型断言，沿 `parent` 链向上查找，实现嵌套图的词法作用域：内层图可以访问外层 State，同类型 State 内层优先。
- `ProcessState[S]` 是唯一推荐的节点内访问 State 的方式（非 PreHandler 路径也可调用）。
- 对于 numind agent 的实现：每次 Agent Graph 执行需要一个 `*AgentState` 记录消息历史、工具调用结果等，这正是通过 `WithGenLocalState` 注入、再用 `ProcessState` 访问的标准模式。

---

### 1.2 节点编排（Chain vs Graph）

**关键代码**：`cloudwego/eino/compose/chain.go` + `generic_graph.go` @ 48e942b

**引用代码 3**（Chain vs Graph 设计，`chain.go` 第 37-80 行）：

```go
// chain.go @ cloudwego/eino:48e942b, lines 37-80

// NewChain create a chain with input/output type.
func NewChain[I, O any](opts ...NewGraphOption) *Chain[I, O] {
    ch := &Chain[I, O]{
        gg: NewGraph[I, O](opts...),
    }

    ch.gg.cmp = ComponentOfChain

    return ch
}

// Chain is a chain of components.
// Chain nodes can be parallel / branch / sequence components.
// Chain is designed to be used in a builder pattern (should Compile() before use).
// And the interface is `Chain style`, you can use it like: `chain.AppendXX(...).AppendXX(...)`
//
// Normal usage:
//  1. create a chain with input/output type: `chain := NewChain[inputType, outputType]()`
//  2. add components to chainable list:
//     2.1 add components: `chain.AppendChatTemplate(...).AppendChatModel(...).AppendToolsNode(...)`
//     2.2 add parallel or branch node if needed: `chain.AppendParallel()`, `chain.AppendBranch()`
//  3. compile: `r, err := c.Compile()`
//  4. run:
//     4.1 `one input & one output` use `r.Invoke(ctx, input)`
//     4.2 `one input & multi output chunk` use `r.Stream(ctx, input)`
//     4.3 `multi input chunk & one output` use `r.Collect(ctx, inputReader)`
//     4.4 `multi input chunk & multi output chunk` use `r.Transform(ctx, inputReader)`
//
// Using in graph or other chain:
// chain1 := NewChain[inputType, outputType]()
// graph := NewGraph[](runTypePregel)
// graph.AddGraph("key", chain1) // chain is an AnyGraph implementation
//
// // or in another chain:
// chain2 := NewChain[inputType, outputType]()
// chain2.AppendGraph(chain1)
type Chain[I, O any] struct {
    err error

    gg *Graph[I, O]

    nodeIdx int

    preNodeKeys []string
```

**解读**：

- `Chain` 本质上是 `Graph` 的语法糖：`NewChain` 内部直接创建 `NewGraph`，只是把 `cmp` 字段标记为 `ComponentOfChain` 以区分。Chain 强制线性拓扑，节点按 `Append` 顺序自动连边，编写简洁但不支持环。
- `Graph` 是底层核心，支持 DAG（无环）和 Pregel（有环，用于 ReAct Loop）两种运行模式。ReAct Agent 必须用 Graph，因为 Model → Tools → Model 是有环的。
- **重要**：两者编译后都产生 `Runnable[I, O]` 接口，运行时 API 完全相同。Chain 可嵌入 Graph（`graph.AddGraph("key", chain1)`），Graph 也可作为另一个 Graph 的子图。

**对 numind 的含义**：SOP 线性流程可用 Chain，Agent ReAct 循环必须用 Graph（Pregel 模式）。

---

### 1.3 Stream vs Generate 分流

**关键代码**：`cloudwego/eino/components/model/interface.go` @ 48e942b

**引用代码 4**（`BaseModel` + `ToolCallingChatModel` 接口，第 30-97 行）：

```go
// interface.go @ cloudwego/eino/components/model:48e942b, lines 30-97

// BaseModel is the generic base model interface parameterized by message type M.
type BaseModel[M any] interface {
    Generate(ctx context.Context, input []M, opts ...Option) (M, error)
    Stream(ctx context.Context, input []M, opts ...Option) (*schema.StreamReader[M], error)
}

// BaseChatModel is a backward-compatible type alias for BaseModel specialized
// with *schema.Message.
type BaseChatModel = BaseModel[*schema.Message]

// Deprecated: Use [ToolCallingChatModel] instead.
//
// ChatModel extends [BaseChatModel] with tool binding via [ChatModel.BindTools].
// BindTools mutates the instance in place, which causes a race condition when
// the same instance is used concurrently: one goroutine's tool list can
// overwrite another's. Prefer [ToolCallingChatModel.WithTools], which returns
// a new immutable instance and is safe for concurrent use.
type ChatModel interface {
    BaseChatModel
    BindTools(tools []*schema.ToolInfo) error
}

// ToolCallingChatModel extends [BaseChatModel] with safe tool binding.
//
// Unlike the deprecated [ChatModel.BindTools], [ToolCallingChatModel.WithTools]
// does not mutate the receiver — it returns a new instance with the given tools
// attached. This makes it safe to share a base model instance across goroutines
// and derive per-request variants with different tool sets:
//
//  base, _ := openai.NewChatModel(ctx, cfg)           // shared, no tools
//  withSearch, _ := base.WithTools([]*schema.ToolInfo{searchTool})
//  withCalc, _  := base.WithTools([]*schema.ToolInfo{calcTool})
type ToolCallingChatModel interface {
    BaseChatModel

    WithTools(tools []*schema.ToolInfo) (ToolCallingChatModel, error)
}
```

**解读**：

- Eino 在 `BaseModel[M]` 层就分了两个方法：`Generate`（同步，返回完整 M）和 `Stream`（流式，返回 `*schema.StreamReader[M]`）。框架在 Runnable 层会做自动降级：若组件只实现了 `Stream`，调用 `Invoke` 时框架会 collect 流合并成单一输出，反之亦然。
- `ChatModel.BindTools` 已废弃，原因是它修改实例本身（并发不安全）。新 API `ToolCallingChatModel.WithTools` 返回新实例，支持并发场景共享 base 实例。
- **对 V2 AiserviceAdapter 的直接约束**：adapter 需要实现 `BaseChatModel`（即 `Generate + Stream` 两个方法），并在 ReAct Agent 的 `AgentConfig` 中使用 `ToolCallingModel` 字段（不是 `Model`，因为 `Model` 对应已废弃的 `ChatModel` 接口）。这意味着 adapter 还需要实现 `WithTools(tools []*schema.ToolInfo) (ToolCallingChatModel, error)` 来绑定工具。

---

## §2 Eino 工具注册接口

### 2.1 Tool interface（必答问题 Q2）

**核心结论**：Eino 的工具体系分为 4 层接口（`BaseTool` → `InvokableTool` / `StreamableTool` → `EnhancedInvokableTool` / `EnhancedStreamableTool`），`BaseTool.Info()` 只负责返回工具元数据（供 LLM 决策），`InvokableTool.InvokableRun()` 负责执行。框架在运行时优先检查 Enhanced 接口，其次普通接口，入参统一是 JSON 字符串（普通）或 `*schema.ToolArgument`（增强）。

**关键代码**：`cloudwego/eino/components/tool/interface.go` @ 48e942b

**引用代码 4**（完整工具接口层次，第 32-79 行）：

```go
// interface.go @ cloudwego/eino/components/tool:48e942b, lines 32-79

// BaseTool provides the metadata that a ChatModel uses to decide whether and
// how to call a tool. Info returns a [schema.ToolInfo] containing the tool
// name, description, and parameter JSON schema.
//
// BaseTool alone is sufficient when passing tool definitions to a ChatModel
// via WithTools — the model only needs the schema to generate tool calls.
// To also execute the tool, implement [InvokableTool] or [StreamableTool].
type BaseTool interface {
    Info(ctx context.Context) (*schema.ToolInfo, error)
}

// InvokableTool is a tool that can be executed by ToolsNode.
//
// InvokableRun receives the model's tool call arguments as a JSON-encoded
// string and returns a plain string result that is sent back to the model as
// a tool message. The framework handles JSON decoding automatically when using
// the [utils.InferTool] or [utils.NewTool] constructors.
type InvokableTool interface {
    BaseTool

    // InvokableRun executes the tool with arguments encoded as a JSON string.
    InvokableRun(ctx context.Context, argumentsInJSON string, opts ...Option) (string, error)
}

// StreamableTool is a streaming variant of [InvokableTool].
//
// StreamableRun returns a [schema.StreamReader] that yields string chunks
// incrementally. The caller (ToolsNode) is responsible for closing the reader.
type StreamableTool interface {
    BaseTool

    StreamableRun(ctx context.Context, argumentsInJSON string, opts ...Option) (*schema.StreamReader[string], error)
}

// EnhancedInvokableTool is a tool that returns structured multimodal results.
//
// Unlike [InvokableTool], arguments arrive as a [schema.ToolArgument] (not a
// raw JSON string) and the result is a [schema.ToolResult] which can carry
// text, images, audio, video, and file content.
//
// When a tool implements both a standard and an enhanced interface, ToolsNode
// prioritises the enhanced interface.
type EnhancedInvokableTool interface {
    BaseTool
    InvokableRun(ctx context.Context, toolArgument *schema.ToolArgument, opts ...Option) (*schema.ToolResult, error)
}

// EnhancedStreamableTool is the streaming variant of [EnhancedInvokableTool].
type EnhancedStreamableTool interface {
    BaseTool
    StreamableRun(ctx context.Context, toolArgument *schema.ToolArgument, opts ...Option) (*schema.StreamReader[*schema.ToolResult], error)
}
```

**字段说明**：

| 接口 | 入参 | 出参 | 用途 |
|------|------|------|------|
| `BaseTool.Info()` | ctx | `*schema.ToolInfo` | 向 LLM 提供工具名/描述/JSON Schema，LLM 用此决定是否调用 |
| `InvokableTool.InvokableRun()` | ctx, JSON string, opts | string | 执行工具，返回字符串结果（作为 ToolMessage 内容反馈给 LLM）|
| `StreamableTool.StreamableRun()` | ctx, JSON string, opts | StreamReader[string] | 流式执行，适合长耗时工具（搜索/代码执行） |
| `EnhancedInvokableTool.InvokableRun()` | ctx, `*ToolArgument`, opts | `*ToolResult` | 多模态结果（图片、文件等），优先级高于普通接口 |

**对 numind 的含义**：Phase 0 及 #3 tool-registry 只需实现 `InvokableTool`（同步执行 SOP 步骤或知识库查询）。`EnhancedInvokableTool` 留到后期（如工具返回图片/文件时）。

---

### 2.2 工具调用 lifecycle

**关键代码**：`cloudwego/eino/flow/agent/react/react.go` @ 48e942b

**引用代码 5**（ReAct Agent 工具注册与 State 联动，第 284-397 行核心段，精简为 40 行）：

```go
// react.go @ cloudwego/eino/flow/agent/react:48e942b, lines 284-397 (精简)

// NewAgent creates a ReAct agent that feeds tool response into next round of Chat Model generation.
func NewAgent(ctx context.Context, config *AgentConfig) (_ *Agent, err error) {
    // 1. 从 ToolsConfig.Tools 批量调用 t.Info(ctx) 获取工具 schema，传给 chatModel
    if toolInfos, err = genToolInfos(ctx, config.ToolsConfig); err != nil {
        return nil, err
    }
    // 2. 把工具 schema 绑定到 chatModel（WithTools，返回新实例，线程安全）
    if chatModel, err = agent.ChatModelWithTools(config.Model, config.ToolCallingModel, toolInfos); err != nil {
        return nil, err
    }
    // 3. 注入 toolResultCollectorMiddleware — 工具执行后把结果发送给外部 stream 监听者
    config.ToolsConfig.ToolCallMiddlewares = append(
        []compose.ToolMiddleware{newToolResultCollectorMiddleware()},
        config.ToolsConfig.ToolCallMiddlewares...,
    )
    // 4. 创建 ToolsNode（DispatchTable：工具名 → 实现），同时绑定 Middleware
    if toolsNode, err = compose.NewToolNode(ctx, &config.ToolsConfig); err != nil {
        return nil, err
    }
    // 5. Graph = Pregel（有环），每次执行初始化一个新 *state{Messages: []}
    graph := compose.NewGraph[[]*schema.Message, *schema.Message](compose.WithGenLocalState(func(ctx context.Context) *state {
        return &state{Messages: make([]*schema.Message, 0, config.MaxStep+1)}
    }))
    // 6. modelPreHandle：State PreHandler — 把输入 messages append 到 state.Messages
    modelPreHandle := func(ctx context.Context, input []*schema.Message, state *state) ([]*schema.Message, error) {
        state.Messages = append(state.Messages, input...)
        if messageModifier != nil {
            modifiedInput := make([]*schema.Message, len(state.Messages))
            copy(modifiedInput, state.Messages)
            return messageModifier(ctx, modifiedInput), nil
        }
        return state.Messages, nil
    }
    // 7. 注册 Model 节点和 Tools 节点，两者都绑定 State PreHandler
    _ = graph.AddChatModelNode(nodeKeyModel, chatModel, compose.WithStatePreHandler(modelPreHandle))
    _ = graph.AddToolsNode(nodeKeyTools, toolsNode, compose.WithStatePreHandler(toolsNodePreHandle))
    // 8. Branch 决策：模型输出含 ToolCalls → 走 Tools 节点；否则 → END
    _ = graph.AddBranch(nodeKeyModel, compose.NewStreamGraphBranch(modelPostBranchCondition, ...))
    // 9. 编译成 Runnable
    runnable, err := graph.Compile(ctx, compose.WithMaxRunSteps(config.MaxStep), compose.WithNodeTriggerMode(compose.AnyPredecessor))
    return &Agent{runnable: runnable, graph: graph}, nil
}
```

**工具调用完整生命周期**：

```
NewAgent() 构造时：
  Info(ctx) × N  →  ToolInfo 集合  →  chatModel.WithTools(toolInfos)  →  ToolsNode(dispatch table)

每次 agent.Generate(ctx, messages) 时：
  Runnable.Invoke(ctx, messages)
    → [State 初始化: *state{Messages: []}]
    → ChatModel 节点（modelPreHandle: messages append to state）
    → ChatModel.Generate(ctx, state.Messages)   ← 已绑定 tools schema
    → [Branch: has ToolCalls?]
        → Yes → ToolsNode（toolsNodePreHandle: state.Messages append assistant msg）
                 → 并行执行 ToolCalls (InvokableRun per call)
                 → 结果封装为 ToolMessages
                 → 返回 Tools 节点输出 []*schema.Message
                 → [Branch: ReturnDirectly?]
                     → Yes → DirectReturn 节点 → END
                     → No  → 回到 ChatModel 节点（下一轮）
        → No  → END
```

**关键发现**：ToolsNode 是框架内部并发执行多个 ToolCall 的容器，`ExecuteSequentially: false`（默认）时所有 ToolCall 并行跑。Middleware 链在每个 ToolCall 执行前后包裹，类似 HTTP middleware pattern。

---

## §3 Coze Studio 工具加载机制评估

### 3.1 Coze 仓库结构与工具源

Coze Studio 后端（Go）在 `backend/domain/agent/singleagent/internal/agentflow/` 下实现 Agent 构建逻辑，工具来自 4 个独立来源：

| 工具来源 | 文件 | 实现接口 |
|----------|------|---------|
| Plugin（HTTP API） | `node_tool_plugin.go` | `tool.InvokableTool` |
| Knowledge（知识库） | `node_tool_knowledge.go` | `tool.InvokableTool`（via `utils.InferTool`） |
| Database（结构化 DB） | `node_tool_database.go` | `tool.InvokableTool` |
| Variables（Agent 变量） | `node_tool_variables.go` | `tool.InvokableTool` |

**引用代码 6**（Coze Studio 工具聚合入口，`agent_flow_builder.go` 第 102-167 行）：

```go
// agent_flow_builder.go @ coze-dev/coze-studio:22275b1, lines 102-167

// BuildAgent 构建完整 Agent Graph，工具按来源分别加载后合并
pluginTools, err := newPluginTools(ctx, &toolConfig{
    spaceID:       conf.Agent.SpaceID,
    userID:        conf.UserID,
    agentIdentity: conf.Identity,
    toolConf:      conf.Agent.Plugin,
    conversationID: conf.ConversationID,
})

wfTools, returnDirectlyTools, err := newWorkflowTools(ctx, &workflowConfig{
    wfInfos: conf.Agent.Workflow,
})

var dbTools []tool.InvokableTool
if len(conf.Agent.Database) > 0 {
    dbTools, err = newDatabaseTools(ctx, &databaseConfig{...})
}

var avTools []tool.InvokableTool
if len(avs) > 0 {
    avTools, err = newAgentVariableTools(ctx, avConf)
}

// 合并所有工具到 agentTools []tool.BaseTool
agentTools := make([]tool.BaseTool, 0, len(pluginTools)+len(wfTools)+len(dbTools)+len(avTools))
agentTools = append(agentTools, slices.Transform(pluginTools, func(a tool.InvokableTool) tool.BaseTool { return a })...)
agentTools = append(agentTools, slices.Transform(wfTools, func(a workflow.ToolFromWorkflow) tool.BaseTool { return a.(tool.BaseTool) })...)
agentTools = append(agentTools, slices.Transform(dbTools, func(a tool.InvokableTool) tool.BaseTool { return a })...)
agentTools = append(agentTools, slices.Transform(avTools, func(a tool.InvokableTool) tool.BaseTool { return a })...)

// 有工具 → ReAct Agent；无工具 → 纯 LLM 节点
if len(agentTools) > 0 {
    isReActAgent = true
    agent, err := react.NewAgent(ctx, &react.AgentConfig{
        ToolCallingModel: chatModel,
        ToolsConfig: compose.ToolsNodeConfig{
            Tools: agentTools,
        },
        ToolReturnDirectly: returnDirectlyTools,
    })
    agentGraph, agentNodeOpts = agent.ExportGraph()
    agentNodeName = keyOfReActAgent
} else {
    agentNodeName = keyOfLLM
}
```

**Coze Plugin 工具实现**（`node_tool_plugin.go` 第 87-148 行）：

```go
// node_tool_plugin.go @ coze-dev/coze-studio:22275b1, lines 87-148

type pluginInvokableTool struct {
    userID      string
    isDraft     bool
    toolInfo    *pluginEntity.ToolInfo
    projectInfo *model.ProjectInfo
    pluginFrom  *bot_common.PluginFrom
    conversationID int64
}

func (p *pluginInvokableTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
    paramInfos, err := p.toolInfo.Operation.ToEinoSchemaParameterInfo(ctx)
    if err != nil {
        return nil, err
    }

    if len(paramInfos) == 0 {
        return &schema.ToolInfo{
            Name:        p.toolInfo.GetName(),
            Desc:        p.toolInfo.GetDesc(),
            ParamsOneOf: nil,
        }, nil
    }

    return &schema.ToolInfo{
        Name:        p.toolInfo.GetName(),
        Desc:        p.toolInfo.GetDesc(),
        ParamsOneOf: schema.NewParamsOneOfByParams(paramInfos),
    }, nil
}

func (p *pluginInvokableTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
    req := &model.ExecuteToolRequest{
        UserID:          p.userID,
        PluginID:        p.toolInfo.PluginID,
        ToolID:          p.toolInfo.ID,
        ArgumentsInJson: argumentsInJSON,
        ExecScene: func() consts.ExecuteScene {
            if p.isDraft {
                return consts.ExecSceneOfDraftAgent
            }
            return consts.ExecSceneOfOnlineAgent
        }(),
    }
    resp, err := crossplugin.DefaultSVC().ExecuteTool(ctx, req, opts...)
    if err != nil {
        return "", err
    }
    return resp.TrimmedResp, nil
}
```

**核心观察**：Coze Studio 的每个工具（`pluginInvokableTool`）是一个**运行时实例**，在请求开始时通过 `MGetAgentTools` 从数据库加载工具定义（`ToolInfo`）并实例化。没有静态注册表，没有全局单例；每次 `BuildAgent` 调用都重新从 DB 拉取并构建新的工具实例。这本质上是**按需实例化**（lazy per-request factory），而非**预注册池**（static registry）。

---

### 3.2 是否值得借鉴（必答问题 Q3）

**结论：部分借鉴（pattern 借鉴，不照搬实现）**

**理由**：

**值得借鉴的 pattern**：

1. **各工具来源独立封装为 `tool.InvokableTool` 实现**：Coze 用 `pluginInvokableTool` / `knowledgeTool` / `databaseTool` / `agentVariableTool` 分别封装，互不感知，统一汇合为 `[]tool.BaseTool` 传入 `react.NewAgent`。这是干净的策略模式，与架构蓝本 §4.2 ToolFactory 的"按工具类型独立 Builder"方向一致，**值得照搬这个结构**。

2. **`utils.InferTool` 大量使用**：`node_tool_knowledge.go` 使用 `utils.InferTool(name, desc, fn, utils.WithSchemaCustomizer(...))` 从函数签名自动推导 JSON Schema。这极大减少了手写 schema 的样板代码，**numind 实现工具时应优先用此方式**。

3. **工具并发执行由 ToolsNode 托管**：Coze 不自己写并发 map，完全依赖 Eino ToolsNode 内置并发（`ExecuteSequentially: false`）。numind 也应如此，不要自己手写并发工具执行逻辑。

**不值得借鉴的实现细节**：

1. **按请求实例化 vs 静态注册**：Coze 每次请求都从 DB 加载并实例化 `pluginInvokableTool`，适合 Coze 的动态 Plugin 生态（用户在运行时增删 Plugin）。numind 的工具是**系统级固定工具集**（SOP executor、knowledge search、date tool），应使用**静态注册表 + 编译时确定的工具列表**。在 Agent 执行时从注册表按配置 ID 查找并组合，不需要每次请求都从 DB 构建实例。

2. **`crossdomain.DefaultSVC()` 全局单例**：Coze 用全局 `defaultSVC` 变量（`plugin.SetDefaultSVC(svc)`）做依赖注入，这是变量替换型的 IoC，对测试不友好。numind 应遵循项目现有 biz/store 模式，通过结构体字段注入依赖。

3. **工具类型检查缺乏编译期保证**：`slices.Transform(wfTools, func(a workflow.ToolFromWorkflow) tool.BaseTool { return a.(tool.BaseTool) })` 中 `a.(tool.BaseTool)` 是运行时类型断言，若 `workflow.ToolFromWorkflow` 不实现 `tool.BaseTool` 则 panic。numind 应在注册阶段做接口断言，提前暴露错误。

---

## §4 对 #3 tool-registry feature 的影响

### 直接复用

1. **`tool.InvokableTool` 作为 numind 工具的统一接口**：所有 SOP 工具（step executor、knowledge query、date getter 等）均实现此接口，不需要自定义接口层。`Info(ctx) → *schema.ToolInfo`（含 JSON Schema）+ `InvokableRun(ctx, json) → string` 即是完整契约。

2. **`utils.InferTool[T, D]` 作为首选构建方式**：对于有明确 Go 结构体入参的工具（如 `type SearchKnowledgeReq struct { Query string \`json:"query"\`; Limit int \`json:"limit"\` }`），直接 `utils.InferTool("search_knowledge", "...", func(ctx, req *SearchKnowledgeReq) (string, error) {...})`，JSON Schema 自动生成。

3. **`ToolsNodeConfig.Tools []tool.BaseTool` 作为 Agent 构建入口**：工具注册表最终产出 `[]tool.BaseTool`，传入 `react.NewAgent` 的 `ToolsConfig.Tools`。注册表本身是 compile-time 确定的工具集合，运行时按 Agent 配置过滤出子集。

4. **`ToolsNodeConfig.ToolCallMiddlewares` 接入 billing**：Eino 的 ToolMiddleware 模式允许在工具执行前后注入逻辑。numind 的 credits 计费钩子（Reserve/Reconcile）应在此挂载，而不是修改工具实现本身。这比 Coze 的方式更干净（Coze 将 billing 耦合在 `crossplugin.DefaultSVC().ExecuteTool` 内部）。

### 需要改造

1. **`Info(ctx)` 的 schema 动态性问题**：Eino 的 `Info(ctx)` 接受 ctx 参数，理论上支持动态 schema（如 Coze 的知识库工具会在 schema 里枚举当前 Agent 的知识库 ID 列表）。numind 的 SOP 工具如果需要动态参数（如枚举可用 SOP 列表），同样可以在 `Info(ctx)` 时从 DB 查询当前用户的 SOP 列表并注入 schema 的 enum 字段。这比静态 schema 复杂，需要控制 `Info()` 的 DB 查询耗时（加本地缓存）。

2. **`schema.ToolInfo.Name` 的唯一性管理**：ToolsNode 用工具名作为 dispatch key，重名会导致 panic 或调用错工具。工具注册表需要在 `NewToolNode` 前做工具名唯一性校验。Eino 本身没有做这个检查（由调用方负责），是一个需要 numind 自己补充的 gate。

3. **`ToolAliasConfig`（别名机制）**：当 LLM 幻觉产生与注册名不完全匹配的工具名时（如 `recall_knowledge` vs `recallKnowledge`），需要配置 `ToolAliasConfig.NameAliases`。工具注册表应该统一管理别名配置，而不是散落在各处。

### 风险点

1. **工具执行 panic 隔离**：`tool_node.go` 对工具执行有 `recover()` 机制（需确认），但如果工具 panic 导致整个 ToolsNode goroutine 崩溃，会影响整个 Agent 的当次执行。numind 需要确认 Eino ToolsNode 的 panic 隔离范围，必要时在 `InvokableRun` 内部加 `defer recover()` 保护，将 panic 转为 error。

2. **`ExecuteSequentially` 与 credits 计费**：默认并行执行多个 ToolCall，如果每个工具调用都走 credits Reserve/Reconcile，需要确认并发 Reserve 不会导致 credits 超扣（多个 goroutine 同时预扣，均通过，但总量超额）。应在 Middleware 层串行化 Reserve，或在并发 Reserve 前先做批量检查。

3. **`Info(ctx)` 在流程关键路径上**：`genToolInfos` 在 `NewAgent` 时批量调 `Info(ctx)` 获取所有工具 schema，如果某个工具的 `Info()` 有 DB 查询，这会是 Agent 初始化的瓶颈。对于每次请求都创建新 Agent（如 numind 的 per-request agent）需要有 schema 缓存策略。

4. **Eino 版本锁定**：当前读取 commit 为 `48e942b`，API 仍在快速迭代（`ChatModel` 已废弃，`ToolCallingChatModel` 是新接口；`AgenticModel` 是更新的接口层）。#3 tool-registry 实现时应对齐 `ToolCallingChatModel`，避免用已废弃的 `BindTools` 方法。版本固定在 go.sum 中，升级前需做接口兼容性检查。

---

*深读时间：2026-05-20。代码快照：eino @ 48e942b，coze-studio @ 22275b1。*
