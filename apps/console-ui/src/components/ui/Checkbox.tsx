import { Checkbox as AppsCheckbox } from "@openai/apps-sdk-ui/components/Checkbox";
import { useId, type ReactNode } from "react";

export interface CheckboxProps {
  checked: boolean;
  onChange: (checked: boolean) => void;
  label: ReactNode;
  description?: ReactNode;
  disabled?: boolean;
  invalid?: boolean;
  name?: string;
  value?: string;
}

export function Checkbox({ checked, description, invalid, label, onChange, ...props }: CheckboxProps) {
  const id = useId();
  return (
    <div className="console-checkbox" data-invalid={Boolean(invalid)}>
      <AppsCheckbox
        checked={checked}
        disabled={props.disabled}
        id={id}
        label={<span>{label}</span>}
        name={props.name}
        onCheckedChange={onChange}
        value={props.value}
      />
      {description ? <span className="console-checkbox__description">{description}</span> : null}
    </div>
  );
}
