import "./globals.css";

export const metadata = {
  title: "Operator Console",
  description: "Workyard multi-service operator UI"
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
