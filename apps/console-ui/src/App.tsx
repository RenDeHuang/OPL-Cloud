import { useConsoleController } from "./app/use-console-controller.ts";
import { ConsoleShell } from "./layout/ConsoleShell.tsx";
import { AdminPages } from "./pages/AdminPages.tsx";
import { CustomerPages } from "./pages/CustomerPages.tsx";
import { ForbiddenPage, LoginPage, LogoutRecovery, NotFoundPage, PublicHome, SessionRecovery } from "./pages/PublicPages.tsx";

export default function App() {
  const controller = useConsoleController();

  if (controller.authStatus === "logout_pending" || controller.authStatus === "logout_unconfirmed") {
    return <LogoutRecovery controller={controller} />;
  }
  if (controller.path === "/") return <PublicHome controller={controller} />;
  if (controller.path === "/login") return <LoginPage controller={controller} />;
  if (controller.path === "/403") return <ForbiddenPage controller={controller} />;
  if (!controller.isKnownRoute) return <NotFoundPage controller={controller} />;
  if (controller.authStatus !== "ready" || !controller.session) return <SessionRecovery controller={controller} />;

  return (
    <ConsoleShell controller={controller}>
      {controller.isAdminRoute ? <AdminPages controller={controller} /> : <CustomerPages controller={controller} />}
    </ConsoleShell>
  );
}
