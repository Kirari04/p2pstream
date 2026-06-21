import { ref } from "vue";
import { useRoute } from "vue-router";
import { localManagementClient } from "@/api/managementClient";
import { messageFromError } from "@/lib/errors";
import type { GetSetupStateResponse, User } from "@/gen/proto/p2pstream/v1/management_pb";

type BootstrapSessionState = "setup-required" | "authenticated" | "unauthenticated";
type AuthenticatedCallback = () => Promise<void>;
type LogoutCallback = () => void;

export function useManagementSession() {
  const route = useRoute();
  const setupState = ref<GetSetupStateResponse | null>(null);
  const currentUser = ref<User | null>(null);
  const setupForm = ref({ username: "admin", password: "" });
  const setupToken = ref("");
  const loginForm = ref({ username: "admin", password: "" });
  const isLoading = ref(true);
  const isBusy = ref(false);
  const isLogoutConfirmOpen = ref(false);
  const error = ref<string | null>(null);

  async function bootstrapSession(): Promise<BootstrapSessionState> {
    setupState.value = await localManagementClient.getSetupState({});
    if (setupState.value.setupRequired) {
      currentUser.value = null;
      return "setup-required";
    }

    try {
      const userResp = await localManagementClient.getCurrentUser({});
      currentUser.value = userResp.user ?? null;
    } catch {
      currentUser.value = null;
      return "unauthenticated";
    }

    return currentUser.value ? "authenticated" : "unauthenticated";
  }

  async function submitSetup(afterAuthenticated: AuthenticatedCallback) {
    isBusy.value = true;
    error.value = null;
    try {
      await localManagementClient.setupAdmin({
        username: setupForm.value.username,
        password: setupForm.value.password,
        setupToken: setupToken.value,
      });
      await login(setupForm.value.username, setupForm.value.password);
      setupState.value = await localManagementClient.getSetupState({});
      await afterAuthenticated();
    } catch (err) {
      error.value = messageFromError(err);
    } finally {
      isBusy.value = false;
    }
  }

  async function submitLogin(afterAuthenticated: AuthenticatedCallback) {
    isBusy.value = true;
    error.value = null;
    try {
      await login(loginForm.value.username, loginForm.value.password);
      await afterAuthenticated();
    } catch (err) {
      error.value = messageFromError(err);
    } finally {
      isBusy.value = false;
    }
  }

  async function login(username: string, password: string) {
    const loginResp = await localManagementClient.login({ username, password });
    currentUser.value = loginResp.user ?? null;
  }

  function requestLogout() {
    if (isBusy.value) return;
    isLogoutConfirmOpen.value = true;
  }

  function cancelLogout() {
    if (isBusy.value) return;
    isLogoutConfirmOpen.value = false;
  }

  async function confirmLogout(afterLogout: LogoutCallback) {
    const didLogout = await logout(afterLogout);
    if (didLogout) {
      isLogoutConfirmOpen.value = false;
    }
  }

  async function logout(afterLogout: LogoutCallback): Promise<boolean> {
    isBusy.value = true;
    error.value = null;
    try {
      await localManagementClient.logout({});
      currentUser.value = null;
      afterLogout();
      loginForm.value.password = "";
      return true;
    } catch (err) {
      error.value = messageFromError(err);
      return false;
    } finally {
      isBusy.value = false;
    }
  }

  function initializeSetupToken() {
    setupToken.value = setupTokenFromURL();
  }

  function setupTokenFromURL(): string {
    const routeToken = stringQueryValue(route.query.setup_token);
    if (routeToken) {
      scrubSetupTokenFromURL();
      return routeToken;
    }
    try {
      const token = new URLSearchParams(window.location.search).get("setup_token")?.trim() ?? "";
      if (token) scrubSetupTokenFromURL();
      return token;
    } catch {
      return "";
    }
  }

  function scrubSetupTokenFromURL() {
    try {
      const url = new URL(window.location.href);
      if (!url.searchParams.has("setup_token")) return;
      url.searchParams.delete("setup_token");
      window.history.replaceState(window.history.state, "", `${url.pathname}${url.search}${url.hash}`);
    } catch {
      // Ignore browsers or test environments without full history support.
    }
  }

  function stringQueryValue(value: unknown): string {
    if (Array.isArray(value)) return stringQueryValue(value[0]);
    return typeof value === "string" ? value.trim() : "";
  }

  return {
    setupState,
    currentUser,
    setupForm,
    setupToken,
    loginForm,
    isLoading,
    isBusy,
    isLogoutConfirmOpen,
    error,
    bootstrapSession,
    submitSetup,
    submitLogin,
    requestLogout,
    cancelLogout,
    confirmLogout,
    initializeSetupToken,
  };
}
