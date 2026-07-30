import { Button as AppsButton, type ButtonProps as AppsButtonProps } from "@openai/apps-sdk-ui/components/Button";
import type { ReactNode } from "react";

export interface ButtonProps extends Omit<AppsButtonProps, "children" | "color" | "loading"> {
  children: ReactNode;
  busy?: boolean;
  color?: AppsButtonProps["color"];
}

export function Button({ busy = false, children, color = "secondary", disabled, ...props }: ButtonProps) {
  return (
    <AppsButton
      {...props}
      aria-busy={busy || undefined}
      color={color}
      disabled={disabled || busy}
      loading={busy}
      pill={false}
    >
      {children}
    </AppsButton>
  );
}
