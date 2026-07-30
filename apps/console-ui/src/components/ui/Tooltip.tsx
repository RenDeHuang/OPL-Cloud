import { Tooltip as AppsTooltip, type TooltipProps as AppsTooltipProps } from "@openai/apps-sdk-ui/components/Tooltip";

export type TooltipProps = AppsTooltipProps;

export function Tooltip(props: TooltipProps) {
  return <AppsTooltip {...props} />;
}
