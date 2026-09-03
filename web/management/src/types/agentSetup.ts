import type { Agent } from "@/gen/proto/p2pstream/v1/management_pb";

export type CreatedAgentSetup = {
  agent: Agent | null;
  token: string;
  updaterEnrollmentToken: string;
  updaterEnrollmentExpiresAtUnixMillis: bigint;
  updaterPinnedRepository: string;
  updaterManagementAuthorityPublicKeyBase64: string;
  updaterManagementAuthorityKeyId: string;
  updaterManagementAuthorityEpoch: bigint;
};
