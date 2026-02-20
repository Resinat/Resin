import { Card } from "./ui/Card";

type ComingSoonPageProps = {
  title: string;
  description: string;
};

export function ComingSoonPage({ title, description }: ComingSoonPageProps) {
  return (
    <Card className="placeholder-card">
      <h2>{title}</h2>
      <p>{description}</p>
    </Card>
  );
}
