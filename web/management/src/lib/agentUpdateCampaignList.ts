import { AgentUpdateCampaignState, type AgentUpdateCampaign } from "@/gen/proto/p2pstream/v1/management_pb";

/** Campaigns per history page. */
export const CAMPAIGN_HISTORY_PAGE_SIZE = 10;
/** Server maximum page size; active campaigns are always loaded in one page. */
export const ACTIVE_CAMPAIGN_LIMIT = 100;
/** Server limit for the literal campaign search. */
export const CAMPAIGN_QUERY_MAX_LENGTH = 200;

export const ACTIVE_CAMPAIGN_STATES: readonly AgentUpdateCampaignState[] = [
  AgentUpdateCampaignState.RUNNING,
  AgentUpdateCampaignState.PAUSED,
];

export const FINISHED_CAMPAIGN_STATES: readonly AgentUpdateCampaignState[] = [
  AgentUpdateCampaignState.CANCELLED,
  AgentUpdateCampaignState.COMPLETED,
];

export type CampaignHistoryStateFilter = "finished" | "completed" | "cancelled";

export const campaignHistoryStateOptions: { label: string; value: CampaignHistoryStateFilter }[] = [
  { label: "All finished", value: "finished" },
  { label: "Completed", value: "completed" },
  { label: "Cancelled", value: "cancelled" },
];

export interface CampaignListRequest {
  limit: bigint;
  offset: bigint;
  query: string;
  states: AgentUpdateCampaignState[];
}

export interface CampaignHistoryFilter {
  page: number;
  query: string;
  state: CampaignHistoryStateFilter;
}

export interface CampaignListPage {
  campaigns: AgentUpdateCampaign[];
  totalCount: number;
}

export function historyStates(filter: CampaignHistoryStateFilter): AgentUpdateCampaignState[] {
  if (filter === "completed") return [AgentUpdateCampaignState.COMPLETED];
  if (filter === "cancelled") return [AgentUpdateCampaignState.CANCELLED];
  return [...FINISHED_CAMPAIGN_STATES];
}

/** Trims the search and caps it at the server limit. */
export function normalizeCampaignQuery(query: string): string {
  return Array.from(query.trim()).slice(0, CAMPAIGN_QUERY_MAX_LENGTH).join("").trim();
}

export function campaignHistoryFilterActive(filter: Pick<CampaignHistoryFilter, "query" | "state">): boolean {
  return Boolean(normalizeCampaignQuery(filter.query)) || filter.state !== "finished";
}

export function activeCampaignsRequest(): CampaignListRequest {
  return { limit: BigInt(ACTIVE_CAMPAIGN_LIMIT), offset: 0n, query: "", states: [...ACTIVE_CAMPAIGN_STATES] };
}

export function historyCampaignsRequest(filter: CampaignHistoryFilter): CampaignListRequest {
  const page = normalizePage(filter.page);
  return {
    limit: BigInt(CAMPAIGN_HISTORY_PAGE_SIZE),
    offset: BigInt((page - 1) * CAMPAIGN_HISTORY_PAGE_SIZE),
    query: normalizeCampaignQuery(filter.query),
    states: historyStates(filter.state),
  };
}

/** Counts every finished campaign; only needed while a search or state filter hides some of them. */
export function finishedCampaignCountRequest(): CampaignListRequest {
  return { limit: 1n, offset: 0n, query: "", states: [...FINISHED_CAMPAIGN_STATES] };
}

/**
 * Maps a list response onto the page that was requested. Campaigns outside the
 * requested states are dropped so a campaign never renders in both groups, and
 * the total never falls below what the page itself proves exists.
 */
export function campaignListPage(
  response: { campaigns?: AgentUpdateCampaign[]; totalCount?: bigint },
  request: CampaignListRequest,
): CampaignListPage {
  const states = new Set(request.states);
  const campaigns = (response.campaigns ?? []).filter((campaign) => states.size === 0 || states.has(campaign.state));
  const reported = Number(response.totalCount ?? 0n);
  const totalCount = Math.max(Number.isFinite(reported) ? reported : 0, Number(request.offset) + campaigns.length);
  return { campaigns, totalCount };
}

export function campaignPageCount(totalCount: number): number {
  return Math.max(1, Math.ceil(Math.max(0, totalCount) / CAMPAIGN_HISTORY_PAGE_SIZE));
}

/** Keeps a page inside the available range after the history shrinks. */
export function clampCampaignPage(page: number, totalCount: number): number {
  return Math.min(normalizePage(page), campaignPageCount(totalCount));
}

export function campaignHistoryResultLabel(page: number, shown: number, totalCount: number, filtered: boolean): string {
  const noun = filtered ? "matching campaign" : "campaign";
  if (totalCount <= 0) return filtered ? "No matching campaigns" : "No campaigns";
  if (totalCount <= CAMPAIGN_HISTORY_PAGE_SIZE && shown >= totalCount) return countLabel(totalCount, noun);
  const start = (normalizePage(page) - 1) * CAMPAIGN_HISTORY_PAGE_SIZE + 1;
  const end = Math.min(totalCount, start + Math.max(0, shown) - 1);
  if (end < start) return `0 of ${countLabel(totalCount, noun)}`;
  return `${start}–${end} of ${countLabel(totalCount, noun)}`;
}

function countLabel(count: number, noun: string): string {
  return `${count} ${noun}${count === 1 ? "" : "s"}`;
}

function normalizePage(page: number): number {
  return Number.isFinite(page) && page >= 1 ? Math.floor(page) : 1;
}
