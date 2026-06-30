import { Outlet } from "react-router";
import { Sidebar } from "@/features/sidebar/Sidebar";

export function App() {
  return (
    <div className="flex h-full w-full">
      <Sidebar />
      <main className="flex-1 min-w-0 flex flex-col bg-paper">
        <Outlet />
      </main>
    </div>
  );
}
