import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  metadataBase: new URL(process.env.NEXT_PUBLIC_SITE_URL || "http://localhost:3000"),
  title: "FlashSeat — live ticket on-sale simulator",
  description: "Thousands of real HTTP buyers race for seats while a Go reservation engine, Redis, and PostgreSQL keep every seat sold exactly once. Break it live and watch it hold.",
  icons: { icon: "/favicon.svg" },
  openGraph: { title: "FlashSeat", description: "A ticket on-sale you can watch, and break.", images: [{ url: "/og.png", width: 1200, height: 630 }] },
};

export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
