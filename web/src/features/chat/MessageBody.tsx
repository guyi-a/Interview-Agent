import { memo } from "react";
import { Streamdown } from "streamdown";
import remarkGfm from "remark-gfm";
import remarkMath from "remark-math";
import rehypeKatex from "rehype-katex";
import "katex/dist/katex.min.css";
import { cn } from "@/lib/utils";

type Props = {
  content: string;
  dense?: boolean;
  streaming?: boolean;
};

type AnchorProps = React.ComponentProps<"a"> & { node?: unknown };
type TableProps = React.ComponentProps<"table"> & { node?: unknown };
type PreProps = React.ComponentProps<"pre"> & { node?: unknown };

function ExternalLink({
  href,
  children,
  className,
  node: _node,
  ...rest
}: AnchorProps) {
  if (!href) return <>{children}</>;
  const isExternal = /^https?:\/\//i.test(href);
  return (
    <a
      {...rest}
      href={href}
      target={isExternal ? "_blank" : undefined}
      rel={isExternal ? "noreferrer noopener" : undefined}
      className={cn("md-link", className)}
    >
      {children}
    </a>
  );
}

function PlainTable({ node: _node, className, ...rest }: TableProps) {
  return <table {...rest} className={className} />;
}

function PlainPre({ node: _node, className, ...rest }: PreProps) {
  return <pre {...rest} className={className} />;
}

export const MessageBody = memo(
  ({ content, dense, streaming }: Props) => (
    <Streamdown
      className={cn("md-body", dense && "md-body-dense")}
      remarkPlugins={[remarkGfm, remarkMath]}
      rehypePlugins={[rehypeKatex]}
      components={{ a: ExternalLink, table: PlainTable, pre: PlainPre }}
      controls={{ table: false, code: false }}
      mode={streaming ? "streaming" : "static"}
      caret={streaming ? "circle" : undefined}
    >
      {content}
    </Streamdown>
  ),
  (prev, next) =>
    prev.content === next.content &&
    prev.dense === next.dense &&
    prev.streaming === next.streaming,
);

MessageBody.displayName = "MessageBody";
