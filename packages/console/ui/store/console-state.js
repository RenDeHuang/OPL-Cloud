import { useEffect, useMemo, useState } from "react";
import { Form, message } from "antd";
import {
  getConsoleState,
  getOperatorSummary,
  getProductionReadiness,
  getRuntimeReadiness
} from "../api/console-read-api.js";
import { useTickets } from "../pages/support/useTickets.js";

export function useConsoleState({ isAdmin, path, csrfToken }) {
  const [state, setState] = useState(null);
  const [adminOps, setAdminOps] = useState({ operator: null, runtime: null, launch: null, error: "" });
  const [topUpOpen, setTopUpOpen] = useState(false);
  const [topUpForm] = Form.useForm();
  const [createPackageId, setCreatePackageId] = useState("basic");
  const tickets = useTickets({ csrfToken, all: isAdmin && path.startsWith("/admin/support") });

  async function refresh() {
    const next = await getConsoleState();
    setState(next);
  }

  async function refreshAdminOps() {
    if (!isAdmin) return;
    try {
      const [operator, runtime, launch] = await Promise.all([
        getOperatorSummary(),
        getRuntimeReadiness(),
        getProductionReadiness()
      ]);
      setAdminOps({ operator, runtime, launch, error: "" });
    } catch (err) {
      setAdminOps((current) => ({ ...current, error: err.message }));
    }
  }

  async function runAction(action, success = "Done") {
    try {
      await action();
      await refresh();
      await refreshAdminOps();
      message.success(success);
      return true;
    } catch (err) {
      message.error(err.message);
      return false;
    }
  }

  useEffect(() => {
    refresh().catch((err) => message.error(err.message));
  }, []);

  useEffect(() => {
    refreshAdminOps();
  }, [isAdmin]);

  const wallet = state?.wallet || state?.account || { balance: 0, frozen: 0, available: 0, totalRecharged: 0 };
  const selectedId = path.match(/\/(?:console|admin)\/workspaces\/([^/]+)/)?.[1];
  const selected = useMemo(
    () => state?.workspaces?.find((workspace) => workspace.id === selectedId) || state?.workspaces?.[0],
    [state, selectedId]
  );
  const selectedPlan = useMemo(
    () => state?.packages?.find((plan) => plan.id === selected?.packageId) || state?.packages?.find((plan) => plan.id === createPackageId),
    [state, selected, createPackageId]
  );
  const selectedCreatePlan = useMemo(
    () => state?.packages?.find((plan) => plan.id === createPackageId) || state?.packages?.[0],
    [state, createPackageId]
  );

  return {
    state,
    wallet,
    selected,
    selectedPlan,
    selectedCreatePlan,
    setCreatePackageId,
    adminOps,
    tickets,
    topUpOpen,
    setTopUpOpen,
    topUpForm,
    runAction,
    refresh,
    refreshAdminOps
  };
}
