import { describe, expect, test } from "bun:test";
import { create } from "@bufbuild/protobuf";
import {
  ManagementTlsAgentRolloutSchema,
  ManagementTlsAgentRolloutState,
  ManagementTlsRotationPhase,
} from "@/gen/proto/p2pstream/v1/management_pb";
import {
  managementCertificateExpiryWarning,
  managementTlsAgentDetail,
  summarizeManagementTlsFleet,
} from "./managementTlsPresentation";

describe("management TLS presentation", () => {
  test("summarizes only explicit cleanup participants while retaining disabled attention", () => {
    const agents = [
      create(ManagementTlsAgentRolloutSchema, { enabled: true, includedInRollout: true, state: ManagementTlsAgentRolloutState.READY }),
      create(ManagementTlsAgentRolloutSchema, { enabled: true, includedInRollout: true, needsTrustAttention: true, state: ManagementTlsAgentRolloutState.PENDING }),
      create(ManagementTlsAgentRolloutSchema, { enabled: true, includedInRollout: false, state: ManagementTlsAgentRolloutState.INCOMPATIBLE }),
      create(ManagementTlsAgentRolloutSchema, { enabled: false, includedInRollout: false, needsTrustAttention: true, state: ManagementTlsAgentRolloutState.DISABLED }),
    ];
    expect(summarizeManagementTlsFleet(agents)).toEqual({
      enabled: 3,
      participants: 2,
      readyParticipants: 1,
      attention: 2,
      rolloutPercent: 50,
    });
  });

  test("explains stale disabled and cleanup-incompatible agents accurately", () => {
    const disabled = create(ManagementTlsAgentRolloutSchema, { enabled: false, needsTrustAttention: true });
    expect(managementTlsAgentDetail(disabled, ManagementTlsRotationPhase.IDLE)).toContain("before enabling");
    const incompatible = create(ManagementTlsAgentRolloutSchema, { enabled: true, state: ManagementTlsAgentRolloutState.INCOMPATIBLE });
    expect(managementTlsAgentDetail(incompatible, ManagementTlsRotationPhase.CLEANING_UP)).toContain("could not install");
  });

  test("warns only for certificates within the thirty-day renewal window", () => {
    const now = Date.UTC(2026, 7, 20);
    expect(managementCertificateExpiryWarning(BigInt(now + 31 * 86_400_000), now)).toBe("");
    expect(managementCertificateExpiryWarning(BigInt(now + 86_400_000), now)).toContain("1 day");
    expect(managementCertificateExpiryWarning(BigInt(now - 1), now)).toContain("expired");
  });
});
