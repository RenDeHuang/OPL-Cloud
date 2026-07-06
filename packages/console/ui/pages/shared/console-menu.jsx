import React from "react";
import {
  Activity,
  Bell,
  Boxes,
  ClipboardCheck,
  CreditCard,
  Database,
  FileText,
  Gauge,
  Headphones,
  KeyRound,
  Layers,
  Server,
  Settings2,
  ShieldCheck,
  UserRound,
  WalletCards
} from "lucide-react";
import { adminMenuRoutes, ownerMenuRoutes } from "../../consoleRoutes.js";

function menuIcon(path) {
  const map = {
    "/console/overview": <Gauge size={17} />,
    "/console/workspaces": <Server size={17} />,
    "/console/gateway": <KeyRound size={17} />,
    "/console/billing": <WalletCards size={17} />,
    "/console/account": <Settings2 size={17} />,
    "/console/support": <Headphones size={17} />,
    "/console/alerts": <Bell size={17} />,
    "/admin/overview": <Gauge size={17} />,
    "/admin/users": <UserRound size={17} />,
    "/admin/governance": <ShieldCheck size={17} />,
    "/admin/workspaces": <Server size={17} />,
    "/admin/billing": <CreditCard size={17} />,
    "/admin/gateway": <KeyRound size={17} />,
    "/admin/fabric": <Boxes size={17} />,
    "/admin/ledger": <Database size={17} />,
    "/admin/runtime": <Activity size={17} />,
    "/admin/support": <Headphones size={17} />,
    "/admin/audit": <ClipboardCheck size={17} />,
    "/admin/settings": <Layers size={17} />
  };
  return map[path] || <FileText size={17} />;
}

export function buildMenu(isAdmin) {
  const owner = ownerMenuRoutes.map((route) => ({
    path: route.path,
    name: route.label,
    icon: menuIcon(route.path)
  }));
  const admin = isAdmin ? [{
    path: "/admin",
    name: "Admin",
    icon: <ShieldCheck size={17} />,
    children: adminMenuRoutes.map((route) => ({
      path: route.path,
      name: route.label,
      icon: menuIcon(route.path)
    }))
  }] : [];
  return [...owner, ...admin];
}
