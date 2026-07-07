import { useEffect, useMemo, useRef, useState } from "react";
import { Form, message } from "antd";
import {
  getConsoleState,
  getManagementState,
  getOperatorSummary,
  getProductionReadiness,
  getRuntimeReadiness
} from "../api/console-read-api.ts";
import { useTickets } from "../pages/support/useTickets.ts";

export function useConsoleState({ isAdmin, path, csrfToken, accountId }: any) {
  const [state, setState] = useState<any>(null);
  const [managementState, setManagementState] = useState<any>({ users: [], accounts: [] });
  const [adminOps, setAdminOps] = useState<any>({ operator: null, runtime: null, launch: null, error: "" });
  const [topUpOpen, setTopUpOpen] = useState(false);
  const [topUpForm] = Form.useForm();
  const [createPackageId, setCreatePackageId] = useState("basic");
  const [pendingActions, setPendingActions] = useState<Set<string>>(new Set());
  const pendingActionKeys = useRef<Set<string>>(new Set());
  const tickets = useTickets({ csrfToken, all: isAdmin && path.startsWith("/admin/support") });

  async function refresh() {
    const [next, management] = await Promise.all([
      getConsoleState(accountId),
      isAdmin ? getManagementState() : Promise.resolve(null)
    ]);
    setState(next);
    if (management) setManagementState(management);
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

  async function runAction(action, success = "Done", { returnFailure = false, actionKey = "" } = {}) {
    if (actionKey && pendingActionKeys.current.has(actionKey)) {
      if (returnFailure) {
        return {
          ok: false,
          status: "pending",
          failureReason: "操作正在进行"
        };
      }
      return false;
    }
    if (actionKey) {
      pendingActionKeys.current.add(actionKey);
      setPendingActions(new Set(pendingActionKeys.current));
    }
    try {
      const result = await action();
      await refresh();
      await refreshAdminOps();
      await tickets.refresh();
      message.success(success);
      return result || true;
    } catch (err) {
      try {
        await refresh();
        await refreshAdminOps();
        await tickets.refresh();
      } catch (refreshError) {
        message.error(refreshError.message);
      }
      message.error(err.message);
      if (returnFailure) {
        return {
          ok: false,
          status: "failed",
          failureReason: err.message,
          ...(err.payload || {})
        };
      }
      return false;
    } finally {
      if (actionKey) {
        pendingActionKeys.current.delete(actionKey);
        setPendingActions(new Set(pendingActionKeys.current));
      }
    }
  }

  useEffect(() => {
    refresh().catch((err) => message.error(err.message));
  }, []);

  useEffect(() => {
    const resources = [
      ...(state?.computeAllocations || []),
      ...(state?.storageVolumes || []),
      ...(state?.storageAttachments || [])
    ];
    const hasPendingResource = resources.some((item) =>
      ["provisioning", "attaching", "destroying", "detaching"].includes(item.status)
    );
    if (!hasPendingResource) return undefined;
    const timer = setInterval(() => {
      refresh().catch((err) => message.error(err.message));
    }, 10000);
    return () => clearInterval(timer);
  }, [state?.computeAllocations, state?.storageVolumes, state?.storageAttachments]);

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
    managementState,
    tickets,
    topUpOpen,
    setTopUpOpen,
    topUpForm,
    pendingActions,
    runAction,
    refresh,
    refreshAdminOps
  };
}
