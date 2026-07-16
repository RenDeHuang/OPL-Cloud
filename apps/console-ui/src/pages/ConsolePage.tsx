import "antd/dist/reset.css";
import { ProLayout } from "@ant-design/pro-components";
import { Button, Tag } from "antd";
import { LogOut, UserRound } from "lucide-react";
import { useEffect } from "react";
import { logoutLocalFirst } from "../api/auth-api.ts";
import { navigate, routeTo } from "../consoleRoutes.ts";
import { renderConsoleRoute } from "../routes/route-registry.tsx";
import { useConsoleState } from "../store/console-state.ts";
import { buildMenu } from "./shared/console-menu.tsx";
import OplAppLogo from "./shared/OplAppLogo.tsx";

export default function ConsolePage({ route, session, onLogout }: any) {
  const isAdmin = session.isOperator === true;
  const path = window.location.pathname;
  const consoleState = useConsoleState({ isAdmin, path, csrfToken: session.csrfToken, accountId: session.user?.accountId || "" });

  async function logout() {
    await logoutLocalFirst(session.csrfToken, onLogout, () => navigate(routeTo("public.home")));
  }

  useEffect(() => {
    if (consoleState.loadError === "not_authenticated") onLogout();
  }, [consoleState.loadError, onLogout]);

  if (!consoleState.state && consoleState.loadError) {
    return (
      <div className="loading">
        <div className="loadFailure" role="alert">
          <strong>无法加载 OPL Console</strong>
          <span>{consoleState.loadError}</span>
          <Button onClick={() => consoleState.refresh()}>重试</Button>
        </div>
      </div>
    );
  }

  if (!consoleState.state) return <div className="loading" role="status" aria-live="polite">正在加载账号数据...</div>;

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
      logo={<OplAppLogo className="proLogo" />}
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
        <Tag color={isAdmin ? "purple" : "blue"} key="role">{isAdmin ? "运维" : "用户"}</Tag>,
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
