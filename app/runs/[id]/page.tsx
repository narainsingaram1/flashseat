import { RunReplay } from "@/components/RunReplay";

export const metadata = { title: "Replay — FlashSeat" };

export default async function RunPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  return <RunReplay id={id} />;
}
