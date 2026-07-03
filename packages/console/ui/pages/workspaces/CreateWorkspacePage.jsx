import React from "react";
import { Alert, Button, Form, Input, Select } from "antd";
import { Link as LinkIcon, Plus } from "lucide-react";
import { createWorkspace } from "../../api/workspaces-api.js";
import { navigate, routeTo } from "../../consoleRoutes.js";
import { ConsoleSurface, InsightPanel, ResourceSplit, StatusPill } from "../shared/commercial-console.jsx";
import { valueLabel } from "../shared/formatters.js";

export function CreateWorkspacePage({ state, session, runAction }) {
  const attachments = (state.storageAttachments || []).filter((item) => item.status === "attached");
  const initialAttachmentId = attachments[0]?.id;
  const computeById = new Map((state.computeResources || []).map((item) => [item.id, item]));
  const storageById = new Map((state.storageVolumes || []).map((item) => [item.id, item]));
  const ready = attachments.length > 0;
  return (
    <ConsoleSurface title="Create Workspace" eyebrow="Workspace entry" subtitle="Create a URL entry from an attached compute and storage pair" compact>
      <div className="consoleGrid">
        <InsightPanel title="创建 Workspace URL" eyebrow="Workspace">
          <Form
            layout="vertical"
            initialValues={{ workspaceName: "Lab Workspace", attachmentId: initialAttachmentId }}
            onFinish={async (values) => {
              const created = await runAction(
                () => createWorkspace({
                  workspaceName: values.workspaceName,
                  attachmentId: values.attachmentId
                }, session.csrfToken),
                "Workspace 已创建"
              );
              if (created) navigate(routeTo("workspace.list"));
            }}
          >
            <Form.Item name="workspaceName" label="名称" rules={[{ required: true, message: "请输入 Workspace 名称" }]}>
              <Input placeholder="Lab Workspace" />
            </Form.Item>
            <Form.Item name="attachmentId" label="挂载关系" rules={[{ required: true, message: "请选择挂载关系" }]}>
              <Select
                options={attachments.map((attachment) => {
                  const compute = computeById.get(attachment.computeId);
                  const storage = storageById.get(attachment.storageId);
                  return {
                    label: `${compute?.name || attachment.computeId} + ${storage?.name || attachment.storageId}`,
                    value: attachment.id
                  };
                })}
              />
            </Form.Item>
            {!ready && <Alert type="warning" showIcon message="需要先开通计算、开通存储，并完成挂载。" />}
            {ready && <Alert type="success" showIcon message="Workspace 只生成 URL token 和 WebUI 入口，不再开新计算或新存储。" />}
            <Button className="formSubmit" type="primary" htmlType="submit" icon={<Plus size={15} />} disabled={!ready}>
              创建 Workspace
            </Button>
          </Form>
        </InsightPanel>

        <InsightPanel
          title="入口检查"
          eyebrow="Attachment"
          actions={<StatusPill label={ready ? "Ready" : "Blocked"} tone={ready ? "good" : "warn"} />}
        >
          <ResourceSplit
            items={(ready ? attachments : [{ id: "missing", status: "missing" }]).slice(0, 3).map((attachment) => {
              const compute = computeById.get(attachment.computeId);
              const storage = storageById.get(attachment.storageId);
              return {
                label: attachment.id === "missing" ? "挂载关系" : "可用挂载",
                value: attachment.id === "missing" ? "None" : (compute?.name || attachment.computeId),
                meta: attachment.id === "missing" ? "create compute + storage + attachment first" : `${storage?.name || attachment.storageId} · ${attachment.mountPath || "/data"}`,
                status: valueLabel(attachment.status),
                tone: attachment.status === "attached" ? "good" : "warn"
              };
            })}
          />
          {ready && (
            <Button icon={<LinkIcon size={15} />} onClick={() => navigate(routeTo("attachment.detail", { id: initialAttachmentId }))}>
              查看挂载
            </Button>
          )}
        </InsightPanel>
      </div>
    </ConsoleSurface>
  );
}
