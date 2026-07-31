import { Select as AppsSelect, type Option } from "@openai/apps-sdk-ui/components/Select";
import { useId, type ReactNode } from "react";

export interface SelectOption extends Option {
  value: string;
}

export interface SelectProps {
  value: string;
  options: SelectOption[];
  onChange: (value: string) => void;
  block?: boolean;
  label?: ReactNode;
  description?: ReactNode;
  error?: ReactNode;
  disabled?: boolean;
  id?: string;
  name?: string;
  placeholder?: string;
}

export function Select({ block, label, description, error, id: suppliedId, onChange, ...props }: SelectProps) {
  const generatedId = useId();
  const id = suppliedId || generatedId;

  return (
    <div className="console-field">
      {label ? <label className="console-field__label" htmlFor={id}>{label}</label> : null}
      <div aria-invalid={Boolean(error)} className="console-select" data-invalid={Boolean(error)}>
        <AppsSelect {...props} block={block} id={id} onChange={(option) => onChange(option.value)} pill={false} />
      </div>
      {description ? <span className="console-field__description">{description}</span> : null}
      {error ? <span className="console-field__error">{error}</span> : null}
    </div>
  );
}
