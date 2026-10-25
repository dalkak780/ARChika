import React from "react";
import type { ButtonHTMLAttributes } from "react";

const paths = {
  first: "M4 4v16m8-14-6 6 6 6m8-12-6 6 6 6",
  previous: "m15 6-6 6 6 6",
  next: "m9 6 6 6-6 6",
  last: "M20 4v16M4 6l6 6-6 6m8-12 6 6-6 6",
  previousWork: "M4 4h16v16H4z m10 3-5 5 5 5",
  nextWork: "M4 4h16v16H4z m6 3 5 5-5 5",
  overview: "M3 3h7v7H3z m11 0h7v7h-7z M3 14h7v7H3z m11 0h7v7h-7z",
  settings:
    "m9 3-1 3-3 1-2 4 2 2 1 4 3 1 1 3h4l1-3 3-1 2-4-2-2-1-4-3-1-1-3z M15 12a3 3 0 1 1-6 0 3 3 0 0 1 6 0",
  help: "M9 9a3 3 0 1 1 5 2c-2 1-2 2-2 3m0 3h.01 M22 12a10 10 0 1 1-20 0 10 10 0 0 1 20 0",
  fullscreen: "M3 9V3h6m6 0h6v6m0 6v6h-6m-6 0H3v-6",
  exitFullscreen: "M9 3v6H3m12-6v6h6m0 6h-6v6M3 15h6v6",
  play: "m7 4 14 8-14 8z",
  pause: "M8 4v16m8-16v16",
  direction: "M3 7h18m-4-4 4 4-4 4M21 17H3m4-4-4 4 4 4",
  close: "m6 6 12 12M6 18 18 6",
  favorite: "m12 3 3 6 6 1-4 5 1 6-6-3-6 3 1-6-4-5 6-1z",
  metadata: "M12 11v6m0-10h.01 M22 12a10 10 0 1 1-20 0 10 10 0 0 1 20 0",
} as const;
export type IconName = keyof typeof paths;
export function Icon({ name }: { name: IconName }) {
  return (
    <svg
      width="24"
      height="24"
      viewBox="0 0 24 24"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      fill="none"
      aria-hidden="true"
    >
      <path d={paths[name]} />
    </svg>
  );
}
export function IconButton({
  icon,
  label,
  children,
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { icon: IconName; label: string }) {
  return (
    <button
      {...props}
      className={`icon-button ${props.className ?? ""}`}
      aria-label={label}
      title={label}
    >
      <Icon name={icon} />
      {children}
    </button>
  );
}
