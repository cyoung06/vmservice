import Link from "next/link";

export default function NotFound() {
  return (
    <main className="flex flex-1 flex-col items-center justify-center gap-2 bg-background-secondary px-4 text-center">
      <h1 className="text-base font-semibold text-foreground">Page not found</h1>
      <Link href="/" className="text-[13px] text-accent hover:underline">
        Back to your machines
      </Link>
    </main>
  );
}
