import { Alert as AppsAlert, type AlertProps as AppsAlertProps } from "@openai/apps-sdk-ui/components/Alert";

export type AlertProps = AppsAlertProps;

export function Alert(props: AlertProps) {
  return <AppsAlert {...props} />;
}
