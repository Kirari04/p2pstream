import { useDialog, type DialogReactive } from "naive-ui";

export function useConfirmDialog() {
  const dialog = useDialog();
  let activeDialog: DialogReactive | null = null;
  let activeResolve: ((value: boolean) => void) | null = null;

  function confirm(
    title: string,
    description: string,
    confirmLabel = "Delete",
  ): Promise<boolean> {
    cancelActiveDialog();
    return new Promise((resolve) => {
      let settled = false;
      const settle = (value: boolean) => {
        if (settled) return;
        settled = true;
        if (activeResolve === settle) {
          activeDialog = null;
          activeResolve = null;
        }
        resolve(value);
      };

      activeResolve = settle;
      activeDialog = dialog.warning({
        title,
        content: description,
        positiveText: confirmLabel,
        negativeText: "Cancel",
        maskClosable: true,
        onPositiveClick: () => settle(true),
        onNegativeClick: () => settle(false),
        onClose: () => settle(false),
        onMaskClick: () => settle(false),
      });
    });
  }

  function handleCancel() {
    cancelActiveDialog();
  }

  function cancelActiveDialog() {
    const dialogToCancel = activeDialog;
    const resolveToCancel = activeResolve;
    activeDialog = null;
    activeResolve = null;
    dialogToCancel?.destroy();
    resolveToCancel?.(false);
  }

  return { confirm, handleCancel };
}
