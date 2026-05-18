import { inject } from "vue";
import { managementClient, type ManagementClient } from "@/api/managementClient";

export function useManagementClient(): ManagementClient {
  return inject<ManagementClient>("managementClient", managementClient);
}
