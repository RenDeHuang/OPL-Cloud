import React from "react";
import { PageContainer, ProCard, ProFormSelect, ProFormText, StepsForm } from "@ant-design/pro-components";
import { Alert, Descriptions } from "antd";
import { createWorkspace } from "../../api/workspaces-api.js";
import { navigate } from "../../consoleRoutes.js";
import { available, money, packageText, planHold } from "../shared/formatters.js";

export function CreateWorkspacePage({ state, wallet, selectedCreatePlan, setCreatePackageId, session, runAction }) {
  const enough = available(wallet) >= planHold(selectedCreatePlan);
  return (
    <PageContainer title="创建 Workspace" subTitle="Package, price, 7-day hold">
      <ProCard>
        <StepsForm
          onFinish={async (values) => {
            await runAction(
              () => createWorkspace({
                workspaceName: values.workspaceName,
                packageId: values.packageId
              }, session.csrfToken),
              "Workspace 已创建"
            );
            navigate("/console/workspaces");
            return true;
          }}
        >
          <StepsForm.StepForm name="name" title="Name">
            <ProFormText name="workspaceName" label="名称" initialValue="Lab Workspace" rules={[{ required: true }]} />
          </StepsForm.StepForm>
          <StepsForm.StepForm name="package" title="Package">
            <ProFormSelect
              name="packageId"
              label="套餐"
              initialValue={selectedCreatePlan?.id || "basic"}
              options={(state.packages || []).map((plan) => ({ label: `${plan.name} · ${packageText(plan)}`, value: plan.id }))}
              fieldProps={{ onChange: setCreatePackageId }}
              rules={[{ required: true }]}
            />
          </StepsForm.StepForm>
          <StepsForm.StepForm name="confirm" title="Confirm">
            <ProCard bordered>
              <Descriptions column={1} size="small">
                <Descriptions.Item label="套餐">{selectedCreatePlan?.name}</Descriptions.Item>
                <Descriptions.Item label="计算">{money(selectedCreatePlan?.price?.computeHourly)}/hour</Descriptions.Item>
                <Descriptions.Item label="存储">{money(selectedCreatePlan?.price?.storageGbMonth)}/GB/month</Descriptions.Item>
                <Descriptions.Item label="7-day hold">{money(planHold(selectedCreatePlan))}</Descriptions.Item>
                <Descriptions.Item label="可用余额">{money(available(wallet))}</Descriptions.Item>
              </Descriptions>
              {!enough && <Alert type="warning" showIcon message="余额不足，无法完成预冻结。" />}
            </ProCard>
          </StepsForm.StepForm>
        </StepsForm>
      </ProCard>
    </PageContainer>
  );
}
