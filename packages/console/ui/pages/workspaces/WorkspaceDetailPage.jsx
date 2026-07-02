import React from "react";
import { PageContainer, ProCard } from "@ant-design/pro-components";
import { Button, Descriptions, Empty, List, Space, Tag, Typography } from "antd";
import { Database, HardDrive, Link as LinkIcon, RefreshCw, RotateCw, Square, Trash2 } from "lucide-react";
import { api } from "../../api/console-api.js";
import { navigate } from "../../consoleRoutes.js";
import { packageText, statusColor, statusLabel, valueLabel } from "../shared/formatters.js";

export function WorkspaceDetailPage({ selected, selectedPlan, state, session, runAction }) {
  if (!selected) return <PageContainer title="Workspace"><Empty description="暂无 Workspace" /></PageContainer>;
  const backups = (state.storageBackups || []).filter((backup) => backup.workspaceId === selected.id);
  return (
    <PageContainer
      title={selected.name}
      subTitle="Workspace URL, compute, storage"
      extra={<Button onClick={() => navigate("/console/workspaces")}>返回列表</Button>}
    >
      <ProCard gutter={16} wrap>
        <ProCard title="Workspace URL" colSpan={{ xs: 24, xl: 12 }}>
          <Space direction="vertical" size={16} className="fullWidth">
            <Typography.Text copyable={selected.access?.tokenStatus === "active"} ellipsis>{selected.url}</Typography.Text>
            <Space wrap>
              <Button icon={<LinkIcon size={15} />} disabled={selected.access?.tokenStatus !== "active"} onClick={() => window.open(selected.url, "_blank", "noopener,noreferrer")}>打开</Button>
              <Button icon={<RefreshCw size={15} />} disabled={selected.access?.tokenStatus !== "active"} onClick={() => runAction(() => api("/api/workspaces/reset-token", { workspaceId: selected.id }, session.csrfToken), "URL 已重置")}>重置</Button>
              <Button danger icon={<Trash2 size={15} />} disabled={selected.access?.tokenStatus !== "active"} onClick={() => runAction(() => api("/api/workspaces/delete-token", { workspaceId: selected.id }, session.csrfToken), "URL 已停用")}>停用</Button>
            </Space>
          </Space>
        </ProCard>
        <ProCard title="计算与存储" colSpan={{ xs: 24, xl: 12 }}>
          <Descriptions column={1} size="small">
            <Descriptions.Item label="状态"><Tag color={statusColor(selected.state)}>{statusLabel(selected)}</Tag></Descriptions.Item>
            <Descriptions.Item label="套餐">{selectedPlan?.name} · {packageText(selectedPlan)}</Descriptions.Item>
            <Descriptions.Item label="计算">{selected.server?.spec} · {valueLabel(selected.server?.status)}</Descriptions.Item>
            <Descriptions.Item label="存储">{selected.disk?.sizeGb}GB · {valueLabel(selected.disk?.status)}</Descriptions.Item>
          </Descriptions>
        </ProCard>
      </ProCard>
      <ProCard className="sectionCard" gutter={16} wrap>
        <ProCard title="生命周期" colSpan={{ xs: 24, xl: 12 }}>
          <Space wrap>
            <Button icon={<Square size={15} />} onClick={() => runAction(() => api("/api/workspaces/stop-server", { workspaceId: selected.id, confirm: true }, session.csrfToken), "计算已停止")}>停止计算</Button>
            <Button icon={<RotateCw size={15} />} onClick={() => runAction(() => api("/api/workspaces/restart-server", { workspaceId: selected.id }, session.csrfToken), "计算已启动")}>启动计算</Button>
            <Button danger icon={<Trash2 size={15} />} onClick={() => runAction(() => api("/api/workspaces/destroy-server", { workspaceId: selected.id, confirm: true }, session.csrfToken), "计算已销毁")}>销毁计算</Button>
            <Button danger icon={<HardDrive size={15} />} onClick={() => runAction(() => api("/api/workspaces/destroy-disk", { workspaceId: selected.id, confirmDataLoss: true }, session.csrfToken), "存储已销毁")}>销毁存储</Button>
          </Space>
        </ProCard>
        <ProCard title="备份" colSpan={{ xs: 24, xl: 12 }}>
          <Space direction="vertical" className="fullWidth">
            <Button icon={<Database size={15} />} onClick={() => runAction(() => api("/api/workspaces/storage-backups", { workspaceId: selected.id, reason: "console", retentionPolicy: { retainLast: 2 } }, session.csrfToken), "备份已创建")}>创建备份</Button>
            <List
              size="small"
              dataSource={backups.slice(-4).reverse()}
              locale={{ emptyText: "暂无备份" }}
              renderItem={(backup) => <List.Item><Tag>{valueLabel(backup.status)}</Tag><Typography.Text ellipsis>{backup.id}</Typography.Text></List.Item>}
            />
          </Space>
        </ProCard>
      </ProCard>
    </PageContainer>
  );
}
