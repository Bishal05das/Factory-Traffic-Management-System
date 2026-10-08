import type { Metadata } from "next";
import Link from "next/link";
import "./globals.css";

export const metadata: Metadata = {
  title: "Factory Traffic · Control room",
  description: "Factory junction monitoring, traffic control, and simulation.",
  icons: { icon: "/icon.svg" },
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return <html lang="en"><body><header className="app-header"><Link href="/" className="brand"><span className="brand-mark"><i /><i /><i /></span><span>Factory Traffic<span className="brand-caption">CONTROL ROOM</span></span></Link><nav aria-label="Main navigation"><Link href="/">Overview</Link><Link href="/junctions/A">Junction A</Link></nav><span className="header-label">Internal road network</span></header><main>{children}</main><footer>Factory Traffic Management <span>v1.0.0 · REST demonstration</span></footer></body></html>;
}
