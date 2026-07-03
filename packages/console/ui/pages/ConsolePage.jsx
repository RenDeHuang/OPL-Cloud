import React from "react";
import { ProLayout } from "@ant-design/pro-components";
import { Button, Tag } from "antd";
import { LogOut, UserRound } from "lucide-react";
import { logout as logoutSession } from "../api/auth-api.js";
import { navigate } from "../consoleRoutes.js";
import { useConsoleState } from "../store/console-state.js";
import { AccountPage } from "./account/AccountPage.jsx";
import {
  AdminBillingPage,
  AdminFabricPage,
  AdminLedgerPage,
  AdminOverviewPage,
  AdminRuntimePage,
  AdminSupportPage,
  AdminUsersPage
} from "./admin/AdminOverviewPage.jsx";
import { BillingPage } from "./billing/BillingPage.jsx";
import { AlertsPage, ApprovalsPage, ReceiptsPage, ResourcesPage } from "./catalog/FabricPages.jsx";
import { GatewayPage } from "./gateway/GatewayPage.jsx";
import { OverviewPage } from "./OverviewPage.jsx";
import { buildMenu } from "./shared/console-menu.jsx";
import { ForbiddenPage } from "./shared/page-widgets.jsx";
import { NewSupportTicketPage, SupportPage, SupportTicketPage } from "./support/SupportPage.jsx";
import { CreateWorkspacePage } from "./workspaces/CreateWorkspacePage.jsx";
import { WorkspaceDetailPage } from "./workspaces/WorkspaceDetailPage.jsx";
import { WorkspacesPage } from "./workspaces/WorkspacesPage.jsx";

export default function ConsolePage({ route, session, onLogout }) {
  const isAdmin = session.user.role === "admin";
  const path = window.location.pathname;
  const consoleState = useConsoleState({ isAdmin, path, csrfToken: session.csrfToken });

  async function logout() {
    try {
      await logoutSession(session.csrfToken);
    } finally {
      onLogout();
      navigate("/");
    }
  }

  if (!consoleState.state) return <div className="loading">Loading OPL Console...</div>;

  const ctx = {
    route,
    path,
    session,
    isAdmin,
    ...consoleState
  };

  return (
    <ProLayout
      title="OPL Cloud"
      logo={<div className="proLogo">OPL</div>}
      location={{ pathname: path }}
      layout="mix"
      navTheme="light"
      menuDataRender={() => buildMenu(isAdmin)}
      menuItemRender={(item, dom) => (
        <a onClick={(event) => {
          event.preventDefault();
          navigate(item.path || "/console/overview");
        }} href={item.path}>{dom}</a>
      )}
      actionsRender={() => [
        <Tag color={isAdmin ? "purple" : "blue"} key="role">{isAdmin ? "Admin" : "Lab Owner"}</Tag>,
        <Button key="logout" icon={<LogOut size={15} />} onClick={logout}>退出</Button>
      ]}
      avatarProps={{
        title: session.user.email,
        size: "small",
        icon: <UserRound size={16} />
      }}
    >
      {renderRoute(ctx)}
    </ProLayout>
  );
}

function renderRoute(ctx) {
  const { path, route, isAdmin } = ctx;
  if (route.area === "admin" && !isAdmin) return <ForbiddenPage />;
  if (path.startsWith("/admin/users")) return <AdminUsersPage {...ctx} />;
  if (path.startsWith("/admin/billing")) return <AdminBillingPage {...ctx} />;
  if (path.startsWith("/admin/fabric")) return <AdminFabricPage {...ctx} />;
  if (path.startsWith("/admin/ledger")) return <AdminLedgerPage {...ctx} />;
  if (path.startsWith("/admin/runtime")) return <AdminRuntimePage {...ctx} />;
  if (path.startsWith("/admin/support")) return <AdminSupportPage {...ctx} />;
  if (path.startsWith("/admin")) return <AdminOverviewPage {...ctx} />;
  if (path.startsWith("/console/workspaces/new")) return <CreateWorkspacePage {...ctx} />;
  if (path.startsWith("/console/workspaces/") || path.startsWith("/admin/workspaces/")) return <WorkspaceDetailPage {...ctx} />;
  if (path.startsWith("/console/workspaces")) return <WorkspacesPage {...ctx} />;
  if (path.startsWith("/console/gateway")) return <GatewayPage {...ctx} />;
  if (path.startsWith("/console/billing")) return <BillingPage {...ctx} />;
  if (path.startsWith("/console/account")) return <AccountPage {...ctx} />;
  if (path.startsWith("/console/support/new")) return <NewSupportTicketPage {...ctx} />;
  if (path.startsWith("/console/support/")) return <SupportTicketPage {...ctx} />;
  if (path.startsWith("/console/support")) return <SupportPage {...ctx} />;
  if (path.startsWith("/console/resources")) return <ResourcesPage {...ctx} />;
  if (path.startsWith("/console/approvals")) return <ApprovalsPage {...ctx} />;
  if (path.startsWith("/console/receipts")) return <ReceiptsPage {...ctx} />;
  if (path.startsWith("/console/alerts")) return <AlertsPage {...ctx} />;
  if (path === "/403") return <ForbiddenPage />;
  return <OverviewPage {...ctx} />;
}
