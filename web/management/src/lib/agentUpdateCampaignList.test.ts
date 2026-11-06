import { describe, expect, test } from "bun:test";
import { create } from "@bufbuild/protobuf";
import { AgentUpdateCampaignSchema, AgentUpdateCampaignState as State } from "@/gen/proto/p2pstream/v1/management_pb";
import {
  activeCampaignsRequest,
  campaignHistoryFilterActive,
  campaignHistoryResultLabel,
  campaignListPage,
  campaignPageCount,
  clampCampaignPage,
  finishedCampaignCountRequest,
  historyCampaignsRequest,
  normalizeCampaignQuery,
} from "./agentUpdateCampaignList";

function campaign(id: number, state: State) {
  return create(AgentUpdateCampaignSchema, { id: BigInt(id), name: `Campaign ${id}`, state });
}

describe("agent update campaign list", () => {
  test("active campaigns are requested in one page regardless of history filters", () => {
    expect(activeCampaignsRequest()).toEqual({ limit: 100n, offset: 0n, query: "", states: [State.RUNNING, State.PAUSED] });
  });

  test("history requests page by ten and map the state filter", () => {
    expect(historyCampaignsRequest({ page: 1, query: "", state: "finished" })).toEqual({ limit: 10n, offset: 0n, query: "", states: [State.CANCELLED, State.COMPLETED] });
    expect(historyCampaignsRequest({ page: 3, query: "  v0.1.53 ", state: "completed" })).toEqual({ limit: 10n, offset: 20n, query: "v0.1.53", states: [State.COMPLETED] });
    expect(historyCampaignsRequest({ page: 2, query: "", state: "cancelled" }).states).toEqual([State.CANCELLED]);
    expect(historyCampaignsRequest({ page: 0, query: "", state: "finished" }).offset).toBe(0n);
    expect(historyCampaignsRequest({ page: Number.NaN, query: "", state: "finished" }).offset).toBe(0n);
    expect(finishedCampaignCountRequest()).toEqual({ limit: 1n, offset: 0n, query: "", states: [State.CANCELLED, State.COMPLETED] });
  });

  test("search text is trimmed and capped at the server limit", () => {
    expect(normalizeCampaignQuery("   ")).toBe("");
    expect(normalizeCampaignQuery("x".repeat(250))).toHaveLength(200);
    expect(Array.from(normalizeCampaignQuery("🙂".repeat(250)))).toHaveLength(200);
    expect(campaignHistoryFilterActive({ query: "  ", state: "finished" })).toBe(false);
    expect(campaignHistoryFilterActive({ query: "canary", state: "finished" })).toBe(true);
    expect(campaignHistoryFilterActive({ query: "", state: "cancelled" })).toBe(true);
  });

  test("responses keep only the requested states and a consistent total", () => {
    const request = historyCampaignsRequest({ page: 2, query: "", state: "finished" });
    const page = campaignListPage({ campaigns: [campaign(1, State.RUNNING), campaign(2, State.COMPLETED), campaign(3, State.CANCELLED)], totalCount: 13n }, request);
    expect(page.campaigns.map((item) => item.id)).toEqual([2n, 3n]);
    expect(page.totalCount).toBe(13);
    // A response without a total still proves the rows it returned.
    expect(campaignListPage({ campaigns: [campaign(4, State.COMPLETED)] }, request).totalCount).toBe(11);
    expect(campaignListPage({}, activeCampaignsRequest())).toEqual({ campaigns: [], totalCount: 0 });
  });

  test("page count and clamping follow the total", () => {
    expect(campaignPageCount(0)).toBe(1);
    expect(campaignPageCount(10)).toBe(1);
    expect(campaignPageCount(11)).toBe(2);
    expect(clampCampaignPage(5, 21)).toBe(3);
    expect(clampCampaignPage(2, 0)).toBe(1);
    expect(clampCampaignPage(-1, 40)).toBe(1);
  });

  test("result labels describe the visible range", () => {
    expect(campaignHistoryResultLabel(1, 0, 0, false)).toBe("No campaigns");
    expect(campaignHistoryResultLabel(1, 0, 0, true)).toBe("No matching campaigns");
    expect(campaignHistoryResultLabel(1, 1, 1, false)).toBe("1 campaign");
    expect(campaignHistoryResultLabel(1, 4, 4, true)).toBe("4 matching campaigns");
    expect(campaignHistoryResultLabel(1, 10, 34, false)).toBe("1–10 of 34 campaigns");
    expect(campaignHistoryResultLabel(4, 4, 34, true)).toBe("31–34 of 34 matching campaigns");
  });
});
