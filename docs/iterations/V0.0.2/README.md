# V0.0.2 — 认证与用户

- 状态：🟡 进行中
- 起止：2026-09-21 ~ —
- 相关提交：（暂无，待实现）

## 目标（为什么做）

让调度器具备账号登录校验能力：用户必须先登录拿到 JWT 才能访问受保护接口，并按角色（admin/user）限制操作权限。这是所有前端功能和 API 安全的前提。

## 范围（做了什么，清单）

- 用户模型与数据访问：`live_rec_users` 表（bcrypt 密码哈希、role、status、failed_login_count、last_login_at）。
- 初始管理员种子账号（首次启动/迁移时创建，密码从环境变量读取）。
- **JWT**：签发、校验、刷新（access + refresh token），secret 从 `JWT_SECRET` 环境变量读取。
- 登录接口：`POST /api/login`、`POST /api/refresh`。
- **登录限流**：按用户名/IP 防暴力破解（失败计数 + 锁定）。
- 鉴权中间件 + 角色权限：admin 可管理全部，user 可查看/创建任务（具体权限矩阵见架构文档）。
- 用户管理接口（admin）：创建/禁用/重置密码。

## 影响面

- 依赖迭代 V0.0.1 的配置与数据库迁移。
- 后续所有 scheduler API 挂在鉴权中间件之后。

## 验证标准

- 正确账号密码登录成功，返回可用 JWT；错误密码被拒并计数。
- 连续失败触发限流/锁定。
- 无 token / 过期 token / 篡改 token 访问受保护接口返回 401。
- 普通用户访问 admin 专属接口返回 403（角色越权拦截）。
- refresh token 能换新 access token。

## 结果

- 完成：User 模型 + UserStore（GORM）、JWT（access/refresh）、bcrypt 密码、鉴权中间件 + 角色权限、登录限流（失败计数 + 锁定）、登录/刷新/用户管理接口、初始管理员种子账号。
- 验证通过：
  - `go build ./...`、`go vet ./...` 通过。
  - 单元测试 9 项通过（auth 5 项：JWT 签发/解析/过期/篡改/错误 secret + bcrypt；config 4 项）。
  - WSL 实测：无 token 访问 `/api/me` 401；admin 登录成功；普通用户越权访问 admin 接口 403；refresh 换新 access token；篡改 token 401；连续 5 次错误密码锁定（401×4 → 403）；锁定后正确密码仍 403。
