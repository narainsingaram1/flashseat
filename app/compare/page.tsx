import { Compare } from "@/components/Compare";

export const metadata = { title: "Compare — FlashSeat" };

export default async function ComparePage({ searchParams }: { searchParams: Promise<{ a?: string; b?: string }> }) {
  const { a, b } = await searchParams;
  return <Compare a={a ?? ""} b={b ?? ""} />;
}
