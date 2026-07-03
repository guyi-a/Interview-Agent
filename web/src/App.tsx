import { Outlet } from "react-router";
import { Sidebar } from "@/features/sidebar/Sidebar";

export function App() {
  return (
    <div className="flex h-full w-full gap-3 bg-subtle/45 p-3">
      <Sidebar />
      <main className="min-w-0 flex flex-1 flex-col overflow-hidden rounded-[22px] bg-paper shadow-[0_0_0_1px_var(--color-rule)]">
        <Outlet />
      </main>
    </div>
  );
}
