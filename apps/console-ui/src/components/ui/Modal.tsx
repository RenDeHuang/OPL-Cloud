import { X } from "lucide-react";
import { useEffect, useRef, type ReactNode } from "react";
import { createPortal } from "react-dom";

import { Button } from "./Button.tsx";

export interface ModalProps {
  open: boolean;
  title: string;
  description?: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
  onClose: () => void;
  className?: string;
}

const focusableSelector = "button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex='-1'])";

export function Modal({ children, className = "", description, footer, onClose, open, title }: ModalProps) {
  const rootRef = useRef<HTMLDivElement>(null);
  const returnFocusRef = useRef<HTMLElement | null>(null);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  useEffect(() => {
    if (!open) return;
    returnFocusRef.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    window.requestAnimationFrame(() => rootRef.current?.querySelector<HTMLElement>(focusableSelector)?.focus());

    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        onCloseRef.current();
        return;
      }
      if (event.key !== "Tab" || !rootRef.current) return;
      const focusable = [...rootRef.current.querySelectorAll<HTMLElement>(focusableSelector)];
      if (!focusable.length) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };

    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("keydown", onKeyDown);
      document.body.style.overflow = previousOverflow;
      returnFocusRef.current?.focus();
    };
  }, [open]);

  if (!open) return null;

  return createPortal(
    <div className="console-modal-backdrop" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
      <div
        aria-describedby={description ? "console-modal-description" : undefined}
        aria-labelledby="console-modal-title"
        aria-modal="true"
        className={`console-modal ${className}`.trim()}
        ref={rootRef}
        role="dialog"
      >
        <header className="console-modal__header">
          <div>
            <h2 id="console-modal-title">{title}</h2>
            {description ? <p id="console-modal-description">{description}</p> : null}
          </div>
          <Button aria-label="关闭" color="secondary" onClick={onClose} size="sm" uniform variant="ghost">
            <X aria-hidden="true" size={18} />
          </Button>
        </header>
        <div className="console-modal__body">{children}</div>
        {footer ? <footer className="console-modal__footer">{footer}</footer> : null}
      </div>
    </div>,
    document.body
  );
}
