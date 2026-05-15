import { describe, expect, test } from "bun:test";
import {
  defaultWafTriggerForm,
  setWafTriggerMetricEnabled,
  wafTriggerFormFromProto,
  wafTriggerPayloadFromForm,
} from "@/lib/publicWafTriggerForm";

describe("publicWafTriggerForm", () => {
  test("keeps zero trigger metrics disabled when loading from proto", () => {
    const form = wafTriggerFormFromProto({
      requestWindowMillis: 10000n,
      minimumRequestRate: 0n,
      trafficSpikeMultiplier: 0,
      proxyActiveRequests: 0n,
      backendActiveRequests: 0n,
      agentActiveRequests: 0n,
      serverCpuPercent: 0,
      agentCpuPercent: 0,
      minimumActiveMillis: 45000n,
      quietPeriodMillis: 90000n,
    });

    expect(form.metrics.minimumRequestRate).toEqual({ enabled: false, value: 0 });
    expect(form.metrics.trafficSpikeMultiplier).toEqual({ enabled: false, value: 0 });
    expect(form.metrics.proxyActiveRequests).toEqual({ enabled: false, value: 0 });
    expect(form.metrics.backendActiveRequests).toEqual({ enabled: false, value: 0 });
    expect(form.metrics.agentActiveRequests).toEqual({ enabled: false, value: 0 });
    expect(form.metrics.serverCpuPercent).toEqual({ enabled: false, value: 0 });
    expect(form.metrics.agentCpuPercent).toEqual({ enabled: false, value: 0 });
    expect(form.minimumActiveSeconds).toBe(45);
    expect(form.quietSeconds).toBe(90);
  });

  test("serializes disabled metrics as zero values", () => {
    let form = defaultWafTriggerForm();
    form = setWafTriggerMetricEnabled(form, "minimumRequestRate", false);
    form = setWafTriggerMetricEnabled(form, "trafficSpikeMultiplier", false);
    form = setWafTriggerMetricEnabled(form, "proxyActiveRequests", false);
    form = setWafTriggerMetricEnabled(form, "backendActiveRequests", false);
    form = setWafTriggerMetricEnabled(form, "agentActiveRequests", false);
    form = setWafTriggerMetricEnabled(form, "serverCpuPercent", false);
    form = setWafTriggerMetricEnabled(form, "agentCpuPercent", false);

    const payload = wafTriggerPayloadFromForm(form);

    expect(payload.minimumRequestRate).toBe(0n);
    expect(payload.trafficSpikeMultiplier).toBe(0);
    expect(payload.proxyActiveRequests).toBe(0n);
    expect(payload.backendActiveRequests).toBe(0n);
    expect(payload.agentActiveRequests).toBe(0n);
    expect(payload.serverCpuPercent).toBe(0);
    expect(payload.agentCpuPercent).toBe(0);
  });

  test("builds a CPU-only waiting-room trigger payload", () => {
    let form = defaultWafTriggerForm();
    form = setWafTriggerMetricEnabled(form, "minimumRequestRate", false);
    form = setWafTriggerMetricEnabled(form, "trafficSpikeMultiplier", false);
    form = setWafTriggerMetricEnabled(form, "proxyActiveRequests", false);
    form = setWafTriggerMetricEnabled(form, "backendActiveRequests", false);
    form = setWafTriggerMetricEnabled(form, "agentActiveRequests", false);
    form = setWafTriggerMetricEnabled(form, "agentCpuPercent", false);
    form.minimumActiveSeconds = 120;
    form.metrics.serverCpuPercent.value = 92;

    const payload = wafTriggerPayloadFromForm(form);

    expect(payload.minimumRequestRate).toBe(0n);
    expect(payload.trafficSpikeMultiplier).toBe(0);
    expect(payload.proxyActiveRequests).toBe(0n);
    expect(payload.backendActiveRequests).toBe(0n);
    expect(payload.agentActiveRequests).toBe(0n);
    expect(payload.serverCpuPercent).toBe(92);
    expect(payload.agentCpuPercent).toBe(0);
    expect(payload.minimumActiveMillis).toBe(120000n);
  });

  test("re-enabling a disabled metric restores its default threshold", () => {
    let form = defaultWafTriggerForm();
    form = setWafTriggerMetricEnabled(form, "serverCpuPercent", false);
    form = setWafTriggerMetricEnabled(form, "serverCpuPercent", true);

    expect(form.metrics.serverCpuPercent).toEqual({ enabled: true, value: 85 });
  });

  test("default create form keeps existing automatic trigger defaults enabled", () => {
    const payload = wafTriggerPayloadFromForm(defaultWafTriggerForm());

    expect(payload.requestWindowMillis).toBe(10000n);
    expect(payload.minimumRequestRate).toBe(50n);
    expect(payload.trafficSpikeMultiplier).toBe(4);
    expect(payload.proxyActiveRequests).toBe(100n);
    expect(payload.backendActiveRequests).toBe(100n);
    expect(payload.agentActiveRequests).toBe(50n);
    expect(payload.serverCpuPercent).toBe(85);
    expect(payload.agentCpuPercent).toBe(85);
    expect(payload.minimumActiveMillis).toBe(30000n);
    expect(payload.quietPeriodMillis).toBe(60000n);
  });
});
