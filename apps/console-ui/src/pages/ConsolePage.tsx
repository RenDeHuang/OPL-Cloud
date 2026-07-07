import React from "react";
import { ProLayout } from "@ant-design/pro-components";
import { Button, Tag } from "antd";
import { LogOut, UserRound } from "lucide-react";
import { logout as logoutSession } from "../api/auth-api.ts";
import { navigate, routeTo } from "../consoleRoutes.ts";
import { renderConsoleRoute } from "../routes/route-registry.tsx";
import { useConsoleState } from "../store/console-state.ts";
import { buildMenu } from "./shared/console-menu.tsx";

export default function ConsolePage({ route, session, onLogout }: any) {
  const isAdmin = session.user.role === "admin";
  const path = window.location.pathname;
  const consoleState = useConsoleState({ isAdmin, path, csrfToken: session.csrfToken, accountId: session.user?.accountId || "" });

  async function logout() {
    try {
      await logoutSession(session.csrfToken);
    } finally {
      onLogout();
      navigate(routeTo("public.home"));
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
      title="OPL Console"
      logo={<div className="proLogo">OPL</div>}
      location={{ pathname: path }}
      layout="mix"
      navTheme="light"
      menuDataRender={() => buildMenu(isAdmin)}
      menuItemRender={(item, dom) => (
        <a onClick={(event) => {
          event.preventDefault();
          navigate(item.path || routeTo("console.overview"));
        }} href={item.path}>{dom}</a>
      )}
      actionsRender={() => [
        <Tag color={isAdmin ? "purple" : "blue"} key="role">{isAdmin ? "Admin" : "Lab Owner"}</Tag>,
        <Button key="logout" icon={<LogOut size={15} />} onClick={logout}>退出</Button>
      ]}
      avatarProps={{
        title: <span className="shellEmail">{session.user.email}</span>,
        size: "small",
        icon: <UserRound size={16} />
      }}
    >
      {renderConsoleRoute(ctx)}
    </ProLayout>
  );
}
