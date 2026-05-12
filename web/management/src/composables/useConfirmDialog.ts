import { reactive } from "vue";

export interface ConfirmDialogState {
  open: boolean;
  title: string;
  description: string;
  confirmLabel: string;
  resolve: ((value: boolean) => void) | null;
}

export function useConfirmDialog() {
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
      state.title = title;
      state.description = description;
      state.confirmLabel = confirmLabel;
      state.resolve = resolve;
      state.open = true;
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
