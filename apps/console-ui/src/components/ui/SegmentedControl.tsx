import { SegmentedControl as AppsSegmentedControl } from "@openai/apps-sdk-ui/components/SegmentedControl";

export interface SegmentedOption<T extends string> {
  value: T;
  label: string;
  disabled?: boolean;
}

export interface SegmentedControlProps<T extends string> {
  value: T;
  options: SegmentedOption<T>[];
  onChange: (value: T) => void;
  ariaLabel: string;
  block?: boolean;
}

export function SegmentedControl<T extends string>({ ariaLabel, block, onChange, options, value }: SegmentedControlProps<T>) {
  return (
    <div role="radiogroup" aria-label={ariaLabel}>
      <AppsSegmentedControl aria-label={ariaLabel} block={block} onChange={onChange} pill={false} value={value}>
        {options.map((option) => (
          <AppsSegmentedControl.Option disabled={option.disabled} key={option.value} value={option.value}>
            {option.label}
          </AppsSegmentedControl.Option>
        ))}
      </AppsSegmentedControl>
    </div>
  );
}
