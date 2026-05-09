"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";

export default function PipelineOverviewRedirect() {
  const router = useRouter();
  useEffect(() => {
    router.replace("/runs?view=pipelines");
  }, [router]);
  return (
    <div className="flex-1 flex items-center justify-center text-sm text-[var(--muted)]">
      Redirecting...
    </div>
  );
}
