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
      <input
        checked={checked}
        className="console-checkbox__native"
        disabled={props.disabled}
        id={id}
        name={props.name}
        onChange={(event) => onChange(event.currentTarget.checked)}
        type="checkbox"
        value={props.value}
      />
      <AppsCheckbox checked={checked} disabled={props.disabled} label={<span>{label}</span>} onCheckedChange={onChange} />
      {description ? <span className="console-checkbox__description">{description}</span> : null}
    </div>
  );
}
