import { Badge as AppsBadge, type BadgeProps as AppsBadgeProps } from "@openai/apps-sdk-ui/components/Badge";

export type BadgeProps = AppsBadgeProps;

export function Badge({ pill = false, ...props }: BadgeProps) {
  return <AppsBadge {...props} pill={pill} />;
}
