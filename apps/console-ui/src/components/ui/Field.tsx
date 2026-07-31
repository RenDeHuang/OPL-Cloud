import { Input } from "@openai/apps-sdk-ui/components/Input";
import { Textarea } from "@openai/apps-sdk-ui/components/Textarea";
import { useId, type InputHTMLAttributes, type ReactNode, type TextareaHTMLAttributes } from "react";

type SharedFieldProps = {
  label: ReactNode;
  description?: ReactNode;
  error?: ReactNode;
  optional?: boolean;
};

type InputFieldProps = SharedFieldProps & Omit<InputHTMLAttributes<HTMLInputElement>, "size"> & {
  multiline?: false;
};

type TextareaFieldProps = SharedFieldProps & TextareaHTMLAttributes<HTMLTextAreaElement> & {
  multiline: true;
};

export type FieldProps = InputFieldProps | TextareaFieldProps;

export function Field(props: FieldProps) {
  const generatedId = useId();
  const id = props.id || generatedId;
  const descriptionId = props.description ? `${id}-description` : undefined;
  const errorId = props.error ? `${id}-error` : undefined;
  const describedBy = [descriptionId, errorId].filter(Boolean).join(" ") || undefined;
  const { label, description, error, optional, multiline, ...controlProps } = props;

  return (
    <label className="console-field" htmlFor={id}>
      <span className="console-field__label">
        <span>{label}</span>
        {optional ? <span className="console-field__optional">可选</span> : null}
      </span>
      {multiline ? (
        <Textarea
          {...(controlProps as Omit<TextareaHTMLAttributes<HTMLTextAreaElement>, "size">)}
          aria-describedby={describedBy}
          aria-invalid={Boolean(error)}
          id={id}
          invalid={Boolean(error)}
        />
      ) : (
        <Input
          {...(controlProps as Omit<InputHTMLAttributes<HTMLInputElement>, "size">)}
          aria-describedby={describedBy}
          aria-invalid={Boolean(error)}
          id={id}
          invalid={Boolean(error)}
          pill={false}
        />
      )}
      {description ? <span className="console-field__description" id={descriptionId}>{description}</span> : null}
      {error ? <span className="console-field__error" id={errorId}>{error}</span> : null}
    </label>
  );
}
