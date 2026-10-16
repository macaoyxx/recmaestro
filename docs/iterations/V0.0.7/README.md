# V0.0.7 — 文档 + 前端 SPA

- 状态：🟡 进行中
- 起止：2026-09-21 ~ —
- 相关提交：（暂无，待实现）

## 目标（为什么做）

让用户有一个可用的图形界面完成所有操作（登录、建任务、看日志、管文件、看节点），并提供可交互的 API 文档。前端打包后 go:embed 进调度器单二进制。

## 范围（做了什么，清单）

- **Swagger 文档**：swaggo/swag 从 Gin handler 注释生成 `docs/swagger.json|yaml`；gin-swagger 挂载 `/swagger/*`；支持 Authorize 填 JWT。
- **前端 SPA（Vue3 + Vite + Element Plus）**：
  - 登录页
  - 任务列表（状态筛选 + 失败/中断红色角标）
  - 任务详情：日志 Tab（时间线、筛选、实时、error 卡片、下载）、文件 Tab（勾选/手动合并/删除/重新合并/不可达禁用）
  - 节点监控（在线/离线、磁盘/CPU/内存、当前任务、容量）
  - 产物列表与下载
  - 顶栏「API 文档」入口（跳 `/swagger/index.html`）
- 前端构建产物 `go:embed` 进 scheduler 二进制，静态托管。
- 鉴权联调：前端登录后携带 JWT 调用全部 API。

## 影响面

- 依赖迭代 V0.0.2（鉴权）~ V0.0.6 的全部 API。
- 纯展示与交互层，不改后端主逻辑。

## 验证标准

- Swagger UI 可访问，能填 JWT 测试受保护接口。
- 前端登录后能完成「建任务 → 看进度/日志 → 管文件 → 下载产物 → 看节点」全流程。
- 合并失败任务在页面上能手动重新合并、删除文件。
- 单二进制启动即带完整 UI 与文档。

## 结果

- 完成：Swagger 文档（swag 注释 + swag init 生成 docs + gin-swagger 挂载）、前端 SPA（Vue3 + Vite + Element Plus：登录/任务列表/任务详情日志+文件 Tab/节点监控）、go:embed 静态托管。
- 过程中修复的真实问题：
  - swag 库版本与 CLI 不匹配（生成字段 `LeftDelim/RightDelim` 旧库不认识）→ 升级 swag 到 v1.16.6。
  - gin `StaticFS("/")` 的 catch-all 通配与 `/api` 前缀冲突导致 panic → 改用 `NoRoute` + `http.FileServer` 兜底。
- 验证通过：
  - Swagger UI 可访问，`/swagger/doc.json` 含 16 个接口路径。
  - 前端首页 / 静态资源 HTTP 200，API 登录不受影响。
  - 浏览器实测（IAB）：登录成功 → 任务列表渲染 → 节点页（worker-1 online、CPU 2.4%、内存 7.1%、磁盘 914GB）→ 任务详情日志 Tab 显示全生命周期日志（claimed→started→stopped→merging→merged→validated→completed）。
- 说明：
  - 浏览器（Windows）访问 WSL 内服务需用 WSL IP（172.18.237.106）而非 localhost（此环境 WSL2 localhost 转发不可用）。
  - 前端交互（创建任务 / 文件 Tab 操作）依赖已验证的 API，浏览器自动化仅验证了登录/列表/节点/日志 Tab 渲染。
