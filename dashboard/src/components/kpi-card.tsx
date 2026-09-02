import { Card } from "@heroui/react";
import type { ReactNode } from "react";

type KpiCardProps = {
  label: string;
  value: ReactNode;
  hint?: ReactNode;
};

export function KpiCard({ label, value, hint }: KpiCardProps) {
  return (
    <Card className="min-h-[118px]">
      <Card.Header>
        <Card.Description>{label}</Card.Description>
        <Card.Title className="text-2xl font-semibold tracking-tight">{value}</Card.Title>
      </Card.Header>
      {hint ? <Card.Footer className="text-sm text-muted">{hint}</Card.Footer> : null}
    </Card>
  );
}
