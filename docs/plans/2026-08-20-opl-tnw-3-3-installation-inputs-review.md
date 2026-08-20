# OPL-TNW.3.3 安装输入去默认化 RP

> 本 RP 只审查 Cloud 源码中安装事实的显式输入边界，不代表 Instance 部署、生产资格或 Product Release 证据。

## 决策结论

OPL Cloud 不再为具体安装隐式选择 domain、deployment mode、Fabric
provider、Workspace image repository、Kubernetes namespace 或 Tencent
provisioner binary。调用者必须提供这些事实；缺失或非法输入在启动、选择
adapter 或运行时 readback 前 fail closed。Provider-specific 解释继续由
Fabric adapter 持有，Control Plane 只消费显式的 provider-neutral runtime
fact。

## 当前问题

当前 Cloud source 仍有多处安装事实 fallback/default：

1. Control Plane 将缺失的 Workspace domain 解释为 `workspace.medopl.cn`，并
   将缺失的 deployment mode/provider 解释为 `platform_owned`/`local-docker`。
2. Fabric cmd 在缺失 `OPL_FABRIC_PROVIDER` 时选择 `local-docker`。
3. Tencent adapter 将缺失 namespace、provisioner binary 和 Workspace image
   repository 解释为 Cloud/medopl 固定值。
4. Local-Docker adapter 将缺失 trusted image source 解释为
   `ghcr.io/gaofeng21cn/one-person-lab-webui`。
5. portable environment example 直接携带一份具体 Workspace image，导致
   安装资产看起来像有产品默认 image。

这些路径使安装 owner、Cloud product 和 provider adapter 的责任混合，且
不同调用者可能在缺少输入时得到不同的隐式安装。

## 已确认的实现事实

- `deploy/portable/compose.deployment-*.yaml` 和
  `compose.fabric-*.yaml` 已是当前真实安装 caller，显式注入 mode/provider。
- `compose.local-workspace.yaml` 已要求调用者提供 immutable
  `OPL_WORKSPACE_IMAGE`；Local-Docker overlay 也将同一引用注入 trusted
  image 输入。
- Tencent production readiness 已将 domain、image、namespace 和
  provisioner binary 列为 required input；Tencent adapter 仍保留源内
  fallback。
- Control Plane Workspace Launch 已从 `OPL_WORKSPACE_IMAGE` 读取并在输入
  缺失时拒绝；本 RP 不新增第二个 image owner。
- `opl-instance-medopl` 的 domain/provider/image/profile 仍属于 Instance
  owner；本 RP 不读取、修改或验证其部署状态。

## 必须分开的事实与语义

| 事实 | Owner | 本 RP 边界 |
| --- | --- | --- |
| Deployment mode / selected Fabric provider | 安装 caller / Control Plane 与 Fabric cmd 的启动输入 | 缺失即拒绝，不在 Cloud 选择默认值 |
| Workspace domain | 安装 caller；Control Plane 只做 host 投影 | 缺失即拒绝，不写入 medopl hostname |
| Workspace image digest | 安装 caller / Instance 或 local installer | 只接受显式 immutable digest；不保存默认 repository |
| Tencent namespace / provisioner path | Instance/Tencent adapter 输入 | adapter 只解释 caller 输入，缺失即失败 |
| Local-Docker trusted image sources | Local installer / Fabric Local-Docker adapter | 只信任显式 source/reference，缺失即失败 |
| Provider-specific resource behavior | Fabric selected adapter | 不上移到 Console、Control Plane 或通用 contract |

## 最小实现

1. 删除 Control Plane server/cmd 与 Fabric cmd 的 mode/provider fallback，并
   为直接测试 caller 提供显式 test setup。
2. 删除 Control Plane 与 Tencent adapter 的 `workspace.medopl.cn` fallback。
3. 删除 Tencent adapter 的固定 Workspace image repository、namespace 和
   provisioner binary fallback；保留 required-input 的 adapter-owned 解释与
   readback。
4. 删除 Local-Docker adapter 的默认 Workspace image repository；只消费显式
   trusted image source/reference。
5. 将 portable env example 中的具体 Workspace image 改为安装者必须替换的
   immutable placeholder，并补齐 domain 的显式输入说明。
6. 更新受影响的当前实现/状态投影与 focused tests；不改 provider-neutral
   public DTO、数据库 schema、历史数据或跨仓库 Instance 文件。

## 迁移/删除顺序

1. 在 fresh `origin/main` 上创建并审查本 RP，锁定以下实现 write set。
2. 先增加缺失输入的 fail-closed focused tests，再删除各 fallback/default。
3. 更新本仓库当前实现文档与安装示例，确保没有第二个当前 owner。
4. 运行 Fabric/Control Plane focused tests、contract/source assertions 和
   `npm run verify:local:full`。
5. 推送实现分支，创建普通非 Draft PR，等待 required CI；不自行合并。

## 精确实现 write set

- `services/control-plane/internal/server/deployment_profile.go`
- `services/control-plane/internal/server/app_state.go`
- `services/control-plane/internal/server/server_test.go`
- `services/control-plane/cmd/control-plane/main.go`
- `services/control-plane/cmd/control-plane/main_test.go`
- `services/fabric/cmd/fabric/main.go`
- `services/fabric/cmd/fabric/main_test.go`
- `services/fabric/internal/fabric/tencent_provider.go`
- `services/fabric/internal/fabric/tencent_provider_runtime.go`
- `services/fabric/internal/fabric/workspace_runtime.go`
- `services/fabric/internal/fabric/local_docker_provider.go`
- `services/fabric/internal/fabric/tencent_provider_test.go`
- `services/fabric/internal/fabric/tencent_workspace_launch_vertical_test.go`
- `services/fabric/internal/fabric/service_test.go`
- `deploy/portable/opl-cloud.env.example`
- 受影响的 `docs/implementation-architecture.md`、`docs/opl-fabric.md`、
  `docs/status.md` 投影段落

本 RP 不包含 persisted metadata key migration；该事实需要独立的 caller、
历史数据和迁移证据，不在 `opl-tnw.3.3` 的 fallback/default slice 内。

## 必须保留的安全门

- Workspace image 继续要求 caller 提供 immutable `repository@sha256`，且
  Launch binding/readback 必须使用同一输入。
- Provider profile、provider binding、spec digest 和 owner-authoritative
  readback 继续 fail closed；不从缺失值推导资源。
- Control Plane 不获得 Docker、Tencent、Kubernetes 或 provider SDK 知识。
- 不执行 Provider、钱包、客户 Workspace、数据库迁移、Instance 或生产
  mutation。
- 不 dispatch Product Release，不修改 A2/A3、C4/C5/C6 或 Acceptance B
  语义/路径。

## 验收标准

- [ ] Cloud source 不再包含 medopl domain、private Workspace image
  repository 或 Local-Docker image repository fallback。
- [ ] 缺失 deployment mode/provider/domain/namespace/provisioner/image
  trusted input 的 focused tests 均 fail closed。
- [ ] Control Plane、Fabric cmd 和两个 adapter 的真实 caller 都显式提供
  所需安装输入。
- [ ] Provider-specific 行为仍只存在于 Fabric adapter；Control Plane 的
  public/provider-neutral contract 无新增 provider-specific 字段。
- [ ] portable env example 不携带具体安装 image/domain 默认值。
- [ ] focused tests、`npm run verify:local` 与 `npm run verify:local:full`
  通过，PostgreSQL/Docker gate 零跳过。
- [ ] 实现分支普通非 Draft PR 的 required CI 通过；合并和 canonical
  回读由上游负责。

## Issue 终态

本 RP 只在实现 PR 创建后作为其审查基线；实现 PR 合并后，才能把
`FABRIC-PROVIDER-PROFILE-01` 的 fallback/default 子项标记为 source-complete。
Instance domain/provider/image、部署 readback、生产资格、Product Release
和 persisted metadata migration 继续由各自 owner/后续 lane 处理。
