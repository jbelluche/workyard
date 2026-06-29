import "./globals.css";

export const metadata = {
  title: "Customer Desk",
  description: "Workyard multi-service customer UI"
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
