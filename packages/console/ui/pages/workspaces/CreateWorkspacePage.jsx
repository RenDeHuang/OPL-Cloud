import React from "react";
import { Alert, Button, Form, Input, Select } from "antd";
import { Link as LinkIcon, Plus } from "lucide-react";
import { createWorkspace } from "../../api/workspaces-api.js";
import { navigate, routeTo } from "../../consoleRoutes.js";
import { ConsoleSurface, InsightPanel, OperationTimeline, ResourceSplit, StatusPill } from "../shared/commercial-console.jsx";
import { valueLabel } from "../shared/formatters.js";

const workspaceEntryStages = Object.freeze(["已提交", "生成 URL", "URL 可用"]);

export function CreateWorkspacePage({ state, session, runAction }) {
  const attachments = (state.storageAttachments || []).filter((item) => item.status === "attached");
  const initialAttachmentId = attachments[0]?.id;
  const computeById = new Map((state.computeAllocations || []).map((item) => [item.id, item]));
  const storageById = new Map((state.storageVolumes || []).map((item) => [item.id, item]));
  const ready = attachments.length > 0;
  return (
    <ConsoleSurface title="创建工作区入口" eyebrow="入口" subtitle="从已挂载的计算和存储创建访问 URL" compact>
      <div className="consoleGrid">
        <InsightPanel title="创建访问 URL" eyebrow="工作区入口">
          <Form
            layout="vertical"
            initialValues={{ workspaceName: "实验工作区", attachmentId: initialAttachmentId }}
            onFinish={async (values) => {
              const created = await runAction(
                () => createWorkspace({
                  workspaceName: values.workspaceName,
                  attachmentId: values.attachmentId
                }, session.csrfToken),
                "工作区入口已创建"
              );
              if (created) navigate(routeTo("workspace.list"));
            }}
          >
            <Form.Item name="workspaceName" label="名称" rules={[{ required: true, message: "请输入工作区名称" }]}>
              <Input placeholder="实验工作区" />
            </Form.Item>
            <Form.Item name="attachmentId" label="挂载关系" rules={[{ required: true, message: "请选择挂载关系" }]}>
              <Select
                options={attachments.map((attachment) => {
                  const computeAllocationId = attachment.computeAllocationId;
                  const compute = computeById.get(computeAllocationId);
                  const storage = storageById.get(attachment.storageId);
                  return {
                    label: `${compute?.name || computeAllocationId} + ${storage?.name || attachment.storageId}`,
                    value: attachment.id
                  };
                })}
              />
            </Form.Item>
            {!ready && <Alert type="warning" showIcon message="需要先开通计算、开通存储，并完成挂载。" />}
            {ready && <Alert type="success" showIcon message="工作区入口只生成访问 URL，不再开新计算或新存储。" />}
            <OperationTimeline operations={[]} stages={workspaceEntryStages} emptyText="提交后生成访问 URL" />
            <Button className="formSubmit" type="primary" htmlType="submit" icon={<Plus size={15} />} disabled={!ready}>
              创建工作区入口
            </Button>
          </Form>
        </InsightPanel>

        <InsightPanel
          title="入口检查"
          eyebrow="挂载"
          actions={<StatusPill label={ready ? "可创建" : "缺少资源"} tone={ready ? "good" : "warn"} />}
        >
          <ResourceSplit
            items={(ready ? attachments : [{ id: "missing", status: "missing" }]).slice(0, 3).map((attachment) => {
              const computeAllocationId = attachment.computeAllocationId;
              const compute = computeById.get(computeAllocationId);
              const storage = storageById.get(attachment.storageId);
              return {
                label: attachment.id === "missing" ? "挂载关系" : "可用挂载",
                value: attachment.id === "missing" ? "无" : (compute?.name || computeAllocationId),
                meta: attachment.id === "missing" ? "请先开通计算、开通存储并完成挂载" : `${storage?.name || attachment.storageId} · ${attachment.mountPath || "/data"}`,
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
