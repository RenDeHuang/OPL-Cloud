import React from "react";

export default function OplAppLogo({ className = "appLogoMark" }: { className?: string }) {
  return (
    <svg className={className} viewBox="0 0 38 38" role="img" aria-label="one-person-lab-app logo">
      <rect width="38" height="38" rx="8" fill="#101828" />
      <path d="M10 25.5V12.5h4.2v13h-4.2Zm7.4 0V12.5h6.2c3.2 0 5.4 1.9 5.4 4.8 0 2.9-2.2 4.8-5.4 4.8h-2v3.4h-4.2Zm4.2-6.8h1.6c1 0 1.6-.5 1.6-1.4s-.6-1.4-1.6-1.4h-1.6v2.8Z" fill="#fff" />
      <path d="M9.5 29h19" stroke="#42d392" strokeWidth="2" strokeLinecap="round" />
    </svg>
  );
}
