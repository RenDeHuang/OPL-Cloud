import { AlertCircle, Inbox, RefreshCw } from "lucide-react";
import type { ReactNode } from "react";

import type { SourceEnvelope } from "../../api/dtos.ts";
import { Alert, Button } from "../ui/index.ts";

export interface SourceStateProps<T> {
  source: SourceEnvelope<T> | null;
  loading?: boolean;
  error?: string;
  empty?: boolean;
  emptyTitle?: string;
  emptyDescription?: string;
  unavailableTitle?: string;
  onRetry?: () => void;
  children: (data: T) => ReactNode;
}

export function SourceState<T>({
  children,
  empty,
  emptyDescription = "当前没有记录。",
  emptyTitle = "暂无数据",
  error,
  loading,
  onRetry,
  source,
  unavailableTitle = "暂不可用"
}: SourceStateProps<T>) {
  if (loading && !source) return <div className="source-loading" aria-live="polite"><span className="spinner" />正在读取</div>;

  if (error) {
    return <Alert color="danger" title="服务暂不可用" description={error} actions={onRetry ? <Button onClick={onRetry} size="sm" variant="outline"><RefreshCw size={14} />重试</Button> : undefined} />;
  }

  if (source?.status === "unavailable") {
    return <Alert color="warning" indicator={<AlertCircle size={18} />} title={unavailableTitle} description="请稍后重试。" actions={onRetry ? <Button onClick={onRetry} size="sm" variant="outline"><RefreshCw size={14} />重试</Button> : undefined} />;
  }

  if (!source) return <div className="source-loading" aria-live="polite"><span className="spinner" />等待读取</div>;

  if (source.status === "empty" || empty) {
    return <div className="source-empty"><Inbox aria-hidden="true" size={24} /><h3>{emptyTitle}</h3><p>{emptyDescription}</p></div>;
  }

  return <>{children(source.data)}</>;
}
