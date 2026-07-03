import React from "react";
import { PageContainer, ProTable } from "@ant-design/pro-components";
import { Button, Empty, List, Space, Tag, Typography } from "antd";

function toneClass(tone = "neutral") {
  return ["good", "warn", "danger", "info"].includes(tone) ? tone : "neutral";
}

function tagColor(tone = "neutral") {
  return {
    good: "green",
    warn: "orange",
    danger: "red",
    info: "blue",
    neutral: "default"
  }[toneClass(tone)];
}

export function ConsoleSurface({ title, eyebrow, subtitle, extra, children, compact = false }) {
  return (
    <PageContainer
      title={(
        <div className="surfaceTitle">
          {eyebrow && <span>{eyebrow}</span>}
          <strong>{title}</strong>
        </div>
      )}
      subTitle={subtitle}
      extra={extra}
    >
      <div className={compact ? "consoleSurface compact" : "consoleSurface"}>
        {children}
      </div>
    </PageContainer>
  );
}

export function MetricStrip({ items = [] }) {
  return (
    <section className="metricStrip" aria-label="Console metrics">
      {items.map((item) => (
        <article className={`metricTile ${toneClass(item.tone)}`} key={item.label}>
          <div className="metricTopline">
            <span>{item.label}</span>
            {item.icon && <span className="metricIcon">{item.icon}</span>}
          </div>
          <strong>{item.value}</strong>
          {item.caption && <small>{item.caption}</small>}
        </article>
      ))}
    </section>
  );
}

export function InsightPanel({ title, eyebrow, actions, children, tone = "neutral", className = "" }) {
  return (
    <section className={`insightPanel ${toneClass(tone)} ${className}`.trim()}>
      <header>
        <div>
          {eyebrow && <span>{eyebrow}</span>}
          <h2>{title}</h2>
        </div>
        {actions && <div className="panelActions">{actions}</div>}
      </header>
      <div className="panelBody">{children}</div>
    </section>
  );
}

export function StatusPill({ label, tone = "neutral" }) {
  return <Tag color={tagColor(tone)} className="statusPill">{label}</Tag>;
}

export function ResourceSplit({ items = [] }) {
  return (
    <div className="resourceSplit">
      {items.map((item) => (
        <article key={item.label}>
          <div className="resourceLabel">
            <span>{item.label}</span>
            {item.status && <StatusPill label={item.status} tone={item.tone} />}
          </div>
          <strong>{item.value}</strong>
          {item.meta && <small>{item.meta}</small>}
        </article>
      ))}
    </div>
  );
}

export function ActionGroup({ actions = [] }) {
  return (
    <Space wrap size={8} className="actionGroup">
      {actions.map((action) => {
        if (React.isValidElement(action)) return action;
        return (
          <Button
            key={action.key || action.label}
            type={action.type}
            danger={action.danger}
            icon={action.icon}
            disabled={action.disabled}
            onClick={action.onClick}
          >
            {action.label}
          </Button>
        );
      })}
    </Space>
  );
}

export function TimelineList({ items = [], emptyText = "暂无记录" }) {
  if (!items.length) {
    return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={emptyText} />;
  }

  return (
    <List
      className="timelineList"
      dataSource={items}
      renderItem={(item) => (
        <List.Item>
          <div className={`timelineDot ${toneClass(item.tone)}`} />
          <div className="timelineContent">
            <strong>{item.title}</strong>
            {item.description && <Typography.Text type="secondary">{item.description}</Typography.Text>}
          </div>
          {item.meta && <Typography.Text type="secondary" className="timelineMeta">{item.meta}</Typography.Text>}
        </List.Item>
      )}
    />
  );
}

export function ObjectTable({ rowKey = "id", data = [], columns = [], emptyText = "暂无数据", ...rest }) {
  return (
    <ProTable
      className="objectTable"
      rowKey={rowKey}
      search={false}
      options={false}
      pagination={false}
      size="small"
      scroll={{ x: "max-content" }}
      dataSource={data}
      columns={columns}
      locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={emptyText} /> }}
      {...rest}
    />
  );
}
