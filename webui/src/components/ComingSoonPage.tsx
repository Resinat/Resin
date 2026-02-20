import { Card } from "./ui/Card";

type ComingSoonPageProps = {
  title: string;
  description: string;
};

export function ComingSoonPage({ title, description }: ComingSoonPageProps) {
  return (
    <Card className="placeholder-card">
      <p className="eyebrow">Module in Progress</p>
      <h2>{title}</h2>
      <p>{description}</p>
    </Card>
  );
}
