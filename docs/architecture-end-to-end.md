# Multica 端到端架构：从登录到任务完成

> 演讲材料 · 配合 3 张图讲完一整条主链路

---

## 一、系统分层架构

```mermaid
flowchart TB
    subgraph Clients["客户端（同一份业务代码，三端壳）"]
        direction LR
        Web["apps/web<br/>Next.js App Router"]
        Desktop["apps/desktop<br/>Electron + React"]
        Mobile["apps/mobile<br/>Expo / RN<br/>（只共享 types）"]
    end

    subgraph Shared["共享包"]
        direction LR
        Views["@multica/views<br/>业务页面 / 表单 / 弹窗"]
        Core["@multica/core<br/>API Client · React Query Hooks<br/>Zustand Stores · 类型"]
        UI["@multica/ui<br/>原子组件（shadcn / Base UI）"]
    end

    subgraph Backend["Go Backend（server/）"]
        direction TB
        Router["Chi Router<br/>server/cmd/server/router.go"]
        Handler["Handler 层<br/>server/internal/handler/*"]
        Service["Service / Domain<br/>service · skill · scheduler · issueguard"]
        SQLC["sqlc Queries<br/>server/pkg/db/generated/"]
        Hub["Realtime Hub<br/>server/internal/realtime/<br/>（WebSocket 广播 + Redis Relay）"]
    end

    subgraph Infra["基础设施"]
        direction LR
        PG[("PostgreSQL 17<br/>+ pgvector")]
        Redis[("Redis<br/>多实例消息中继（可选）")]
        Nginx["Nginx<br/>反向代理 :18000"]
    end

    subgraph Runtime["Agent 运行时（Daemon）"]
        direction TB
        Daemon["multica daemon<br/>server/internal/daemon/<br/>本地 / 远程跑"]
        Claude["Claude / Skill Bundles<br/>在沙盒内执行"]
    end

    Web & Desktop --> Views
    Mobile -. "只 import type" .-> Core
    Views --> Core
    Views --> UI
    Core -- "HTTP / WS" --> Nginx
    Nginx --> Router
    Router --> Handler
    Handler --> Service
    Handler --> SQLC
    SQLC --> PG
    Handler --> Hub
    Hub -- "ws push" --> Nginx
    Hub <-. "fanout" .-> Redis

    Daemon <== "长连 /api/daemon/ws" ==> Handler
    Daemon --> Claude
```

**讲解要点**

- **三端一套业务代码**：Web / Desktop 直接共享 `views + core + ui`；Mobile 独立但共享类型，保持产品语义一致（计数、权限、状态机）。
- **state 分两层**：TanStack Query 管服务端状态（issues、agents、members），Zustand 管客户端状态（当前 workspace、筛选器、草稿）。WebSocket 事件只回写 Query 缓存，不动 Zustand。
- **realtime 是一等公民**：所有写操作走完数据库后立刻 publish 给 Hub，Hub 通过 WebSocket 推到所有连接的客户端，多实例时通过 Redis Relay 跨节点广播。
- **Daemon 是 agent 的执行器**：跟后端建一条长连 WebSocket，主动拉任务、上报进度、上传消息——agent 不是服务端线程，是独立进程。

---

## 二、端到端时序：登录 → 创建 → 执行 → 完成

```mermaid
sequenceDiagram
    autonumber
    actor U as 用户
    participant C as Client<br/>(web / desktop)
    participant N as Nginx
    participant H as Go Handler
    participant DB as Postgres
    participant Hub as Realtime Hub
    participant D as Daemon<br/>(Agent Runtime)
    participant LLM as Claude / Skills

    rect rgb(235,245,255)
    Note over U,H: ① 登录
    U->>C: 输入邮箱
    C->>N: POST /auth/send-code
    N->>H: → SendCode
    H->>DB: 生成验证码 + 落库
    H-->>C: 200 (邮件已发)
    U->>C: 填验证码
    C->>N: POST /auth/verify-code
    N->>H: → VerifyCode
    H->>DB: 校验码 + 建/查 user + 建 session
    H-->>C: Set-Cookie: session
    C->>C: 写 auth store，跳 dashboard
    end

    rect rgb(240,250,240)
    Note over U,Hub: ② 创建任务（Issue）
    U->>C: 新建 issue，指派给 Agent
    C->>C: TanStack Query：optimistic patch 缓存
    C->>N: POST /api/workspaces/{id}/issues
    Note right of C: Header: X-Workspace-ID
    N->>H: → CreateIssue
    H->>DB: INSERT issues<br/>(assignee_type=agent, assignee_id=...)
    H->>DB: INSERT tasks (排队 pending)
    H->>Hub: publishTask(workspace, payload)
    Hub-->>C: ws: issue.created
    C->>C: 协调缓存（确认 / 回滚）
    end

    rect rgb(255,248,235)
    Note over D,LLM: ③ Agent 拉任务 + 执行
    D->>N: GET /api/daemon/ws（长连）
    N->>H: → DaemonWebSocket
    D->>N: POST /api/daemon/runtimes/{rid}/tasks/claim
    N->>H: → ClaimTaskByRuntime
    H->>DB: SELECT … FOR UPDATE SKIP LOCKED
    H-->>D: 200 {taskId, prompt, skills}
    D->>N: POST /api/daemon/tasks/{tid}/start
    H->>DB: issues.status = in_progress
    H->>Hub: publishTask(status=in_progress)
    Hub-->>C: ws: issue.updated

    loop 流式进度
        D->>LLM: 调模型 / 跑 skill bundle
        LLM-->>D: 流式 token / 工具调用
        D->>N: POST /api/daemon/tasks/{tid}/progress
        D->>N: POST /api/daemon/tasks/{tid}/messages
        N->>H: → ReportTaskProgress / Messages
        H->>DB: 落 messages、用量
        H->>Hub: publishChat(workspace)
        Hub-->>C: ws: task.message
        C->>C: 实时渲染 agent 的思考 / 工具调用
    end
    end

    rect rgb(245,235,255)
    Note over D,C: ④ 完成
    D->>N: POST /api/daemon/tasks/{tid}/complete
    N->>H: → CompleteTask
    H->>DB: tasks.done + issues.status=done
    H->>DB: INSERT usage（token / 成本）
    H->>Hub: publishTask(status=done)
    Hub-->>C: ws: issue.updated
    C->>C: Query 缓存自动更新 → UI 打勾
    C-->>U: ✅ 任务完成通知
    end
```

**讲解要点（按节奏念）**

1. **登录段**：两步走——拿验证码、验证。后端落 session，前端把 cookie 拿到手就进 dashboard。Google OAuth 是同样的形状，多一层回调。
2. **创建段**：注意一句关键——**optimistic mutation**。UI 不等服务端，先在本地缓存上把 issue 摆出来，请求失败再回滚。这是用户感知"丝滑"的根因。
3. **执行段**：这里是 Multica 区别于传统任务系统的核心——**agent 是真正的 assignee**。Daemon 跟服务端是 WebSocket 长连，主动 claim 任务，进入沙盒跑 Claude/skill；每一步思考、每一次工具调用都通过 progress 接口流回服务端，再通过 realtime Hub 推给所有看着这个 issue 的人。**用户能看到 agent 在干什么**，不是黑盒。
4. **完成段**：daemon 上报 complete，服务端改 issue 状态、记账（token/usage），广播。客户端缓存被动更新，issue 在列表里自动打勾——**全过程没有手动刷新**。

---

## 三、数据流：state 与 realtime 的协作

```mermaid
flowchart LR
    subgraph Browser["客户端进程"]
        direction TB
        UI2[UI 组件]
        Hooks["@multica/core hooks<br/>useIssues / useAgents / useTask"]
        TQ[(TanStack Query<br/>Cache 服务端状态)]
        ZS[(Zustand Store<br/>当前 ws · 筛选 · 草稿)]
        WSClient[WebSocket Client]
    end

    subgraph Server["服务端"]
        APIs[REST Handlers]
        Hub2[Realtime Hub]
    end

    UI2 -- "读 server state" --> Hooks
    UI2 -- "读 client state" --> ZS
    Hooks -- "useQuery / useMutation" --> TQ
    TQ <-- "HTTP" --> APIs
    APIs --> Hub2
    Hub2 -- "ws push" --> WSClient
    WSClient -- "invalidate / patch" --> TQ
    TQ -- "缓存更新触发 re-render" --> UI2

    classDef cache fill:#e1f5ff,stroke:#0369a1
    class TQ,ZS cache
```

**讲解要点**

- **TanStack Query 是唯一的服务端状态来源**。所有 issue、agent、member 数据都在它的缓存里。
- **WebSocket 不绕过 Query**：服务端推消息过来，前端不直接改 Zustand，而是去 `invalidate` 或 `patch` Query 缓存，让缓存触发组件重渲染。这条规则在 `CLAUDE.md` 里是硬约束。
- **Zustand 只放"客户端的事"**：你选了哪个 workspace、筛选条件是什么、tab 布局——这些跟服务器无关的状态。
- 这种分离让"**多端同步**"变得自然：同一个 issue，你在 Web 上改了，Desktop 和手机上几乎瞬时看到——因为它们订阅了同一个 WebSocket 频道，更新了各自的 Query 缓存。

---

## 四、演讲收束（一句话总结）

> **Multica 把"agent"做成了任务系统的一等公民**——它有 ID、能被 @、能被指派、能改状态、能在所有人面前实时干活；后端通过 sqlc + Postgres 保证一致性，realtime Hub 保证三端实时同步，daemon 把模型执行从服务端剥离出来跑在沙盒里——这套架构让 AI 协作从"调 API"升级成"团队成员"。

---

## 附：关键文件索引（万一被问细节）

| 关注点 | 位置 |
|---|---|
| HTTP 路由总表 | `server/cmd/server/router.go` |
| 登录 / 验证码 / Google OAuth | `server/internal/handler/auth.go`（`VerifyCode:350`, `GoogleLogin:464`） |
| Issue 创建 / 更新 | `server/internal/handler/issue.go`（`CreateIssue:2078`, `UpdateIssue:2333`） |
| Daemon 任务生命周期 | `server/internal/handler/daemon.go`（`ClaimTaskByRuntime:1227`, `StartTask:2054`, `ReportTaskProgress:2121`, `CompleteTask:2155`, `FailTask:2325`） |
| Daemon WebSocket | `server/internal/handler/daemon_ws.go:11` |
| Realtime 广播 | `server/internal/realtime/sharded_stream_relay.go`, `redis_relay.go` |
| 事件 publish 入口 | `server/internal/handler/handler.go`（`publish:310`, `publishTask:322`, `publishChat:335`） |
| Daemon 主程序 | `server/internal/daemon/daemon.go` |
| 客户端 API/hooks | `packages/core/api/`, `packages/core/queries/` |
| 共享业务页面 | `packages/views/` |
