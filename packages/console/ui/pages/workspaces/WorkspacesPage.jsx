import React from "react";
import { PageContainer, ProTable } from "@ant-design/pro-components";
import { Button, Tag, Typography } from "antd";
import { Link as LinkIcon, Plus, RefreshCw, Trash2 } from "lucide-react";
import { deleteWorkspaceToken, resetWorkspaceToken } from "../../api/workspaces-api.js";
import { navigate } from "../../consoleRoutes.js";
import { available, money, packageText, statusColor, statusLabel } from "../shared/formatters.js";

export function WorkspacesPage({ state, wallet, runAction, session }) {
  const planById = Object.fromEntries((state.packages || []).map((plan) => [plan.id, plan]));
  return (
    <PageContainer
      title="OPL Workspace"
      extra={<Button type="primary" icon={<Plus size={15} />} onClick={() => navigate("/console/workspaces/new")}>创建</Button>}
    >
      <ProTable
        rowKey="id"
        search={false}
        options={false}
        pagination={false}
        dataSource={state.workspaces}
        columns={[
          { title: "名称", dataIndex: "name", render: (_, row) => <Button type="link" onClick={() => navigate(`/console/workspaces/${row.id}`)}>{row.name}</Button> },
          { title: "状态", dataIndex: "state", render: (_, row) => <Tag color={statusColor(row.state)}>{statusLabel(row)}</Tag> },
          { title: "套餐", dataIndex: "packageId", render: (value) => packageText(planById[value]) },
          { title: "Workspace URL", dataIndex: "url", ellipsis: true, render: (_, row) => <Typography.Text copyable={row.access?.tokenStatus === "active"}>{row.url}</Typography.Text> },
          { title: "余额", render: () => money(available(wallet)) },
          {
            title: "操作",
            valueType: "option",
            render: (_, row) => [
              <Button key="open" size="small" icon={<LinkIcon size={14} />} disabled={row.access?.tokenStatus !== "active"} onClick={() => window.open(row.url, "_blank", "noopener,noreferrer")}>打开</Button>,
              <Button key="reset" size="small" icon={<RefreshCw size={14} />} disabled={row.access?.tokenStatus !== "active"} onClick={() => runAction(() => resetWorkspaceToken({ workspaceId: row.id }, session.csrfToken), "URL 已重置")}>重置</Button>,
              <Button key="delete" size="small" danger icon={<Trash2 size={14} />} disabled={row.access?.tokenStatus !== "active"} onClick={() => runAction(() => deleteWorkspaceToken({ workspaceId: row.id }, session.csrfToken), "URL 已停用")}>停用</Button>
            ]
          }
        ]}
      />
    </PageContainer>
  );
}
