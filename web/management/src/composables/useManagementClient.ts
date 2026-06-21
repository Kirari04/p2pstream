import { inject } from "vue";
import { managementClient, type ManagementClient } from "@/api/managementClient";
import { managementClientKey } from "@/composables/managementContextKeys";

export function useManagementClient(): ManagementClient {
  return inject(managementClientKey, managementClient);
}
