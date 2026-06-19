import { reactive } from "vue";
import { useDialog } from "naive-ui";

export interface ConfirmDialogState {
  open: boolean;
  title: string;
  description: string;
  confirmLabel: string;
  resolve: ((value: boolean) => void) | null;
}

export function useConfirmDialog() {
  const dialog = useDialog();
  const state = reactive<ConfirmDialogState>({
    open: false,
    title: "",
    description: "",
    confirmLabel: "Delete",
    resolve: null,
  });

  function confirm(
    title: string,
    description: string,
    confirmLabel = "Delete",
  ): Promise<boolean> {
    return new Promise((resolve) => {
      let settled = false;
      const settle = (value: boolean) => {
        if (settled) return;
        settled = true;
        resolve(value);
      };

      dialog.warning({
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

  function handleConfirm() {
    state.resolve?.(true);
    state.open = false;
    state.resolve = null;
  }

  function handleCancel() {
    state.resolve?.(false);
    state.open = false;
    state.resolve = null;
  }

  return { state, confirm, handleConfirm, handleCancel };
}
