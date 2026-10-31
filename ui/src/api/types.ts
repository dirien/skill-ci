// Types of the reporting site API (/api/v1/). They mirror the Go types in
// controller/internal/reportsite/apiv1 by hand; the Go types are the source.
// The Go test TestFixturesMatchTypes checks every fixture in ./fixtures against
// them, so a fixture that type-checks here and passes there is a real response.
//
// JSON rules: lists are always present ([] when empty); an optional field (?)
// is left out when unknown, never null; null appears only in VersionRow.cells
// and GridCase.withSkill / withoutSkill, for a missing cell. Times are RFC 3339
// strings in UTC.

export type Arm = "with-skill" | "without-skill";
export type TrialsState = "stored" | "withheld" | "none" | "unknown";
export type Phase = "Pending" | "Running" | "Succeeded" | "Failed" | "Planned";
export type Verdict = "Helps" | "Neutral" | "Hurts" | "Obsolete" | "Regressed" | "Inconclusive" | "Rejected";
export type TierStatus = "passed" | "failed" | "incomplete" | "skipped";
export type Category = "msg" | "user" | "think" | "tools" | "web" | "pkg" | "skill" | "net" | "judge" | "usage" | "meta";
export type NetworkState = "collected" | "partial" | "unavailable" | "off";

/** Every log category in display order (apiv1.Categories). */
export const CATEGORIES: readonly Category[] = [
  "msg", "user", "think", "tools", "web", "pkg", "skill", "net", "judge", "usage", "meta",
];

export interface Interval {
  lower: number;
  upper: number;
  /** For example "95%". */
  level: string;
}

export interface Tiers {
  tier1?: TierStatus;
  tier2?: TierStatus;
  tier3?: TierStatus;
}

export interface Ratio {
  passed: number;
  total: number;
}

export interface Pagination {
  page: number;
  pageSize: number;
  total: number;
  pages: number;
}

export interface Thresholds {
  helps: number;
  hurts: number;
  promote: number;
  demote: number;
  obsolete: number;
  regression: number;
  confidence: number;
}

/** GET /api/v1/site */
export interface Site {
  namespace: string;
  project: string;
  version: string;
  thresholds: Thresholds;
  generatedAt: string;
}

/** GET /api/v1/overview */
export interface Overview {
  generatedAt: string;
  refreshSeconds: number;
  counts: Counts;
  attention: AttentionItem[];
  recent: RunSummary[];
  channels: ChannelRow[];
  channelsError?: string;
  network: NetworkPolicy;
}

export interface Counts {
  runs: number;
  pending: number;
  running: number;
  succeeded: number;
  failed: number;
  planned: number;
  skills: number;
  onStable: number;
  heldBack: number;
  rejected: number;
}

export interface AttentionItem {
  kind: "failed" | "held-back" | "rejected" | "running" | "pending";
  skill: string;
  version: string;
  run?: string;
  entry?: string;
  message: string;
  since: string;
}

export interface ChannelRow {
  skill: string;
  stable?: string;
  beta?: string;
  heldBack: string[];
  rejected: string[];
  versions: number;
}

export interface NetworkPolicy {
  default: string;
  modes: string[];
  bundles: string[];
  enforcement: string;
  enforcedBy?: string;
  source: "SkillCIConfig" | "LastValid" | "BuiltIn";
  sourceText: string;
  hubbleRelay?: string;
  problem?: string;
}

export interface RunSummary {
  name: string;
  skill: string;
  version: string;
  digest: string;
  agent: string;
  agentVersion?: string;
  ranVersion?: string;
  model: string;
  entry: string;
  required: boolean;
  attempts: number;
  trigger: string;
  pushedBy?: string;
  phase: Phase;
  verdict?: Verdict;
  withSkill?: number;
  withoutSkill?: number;
  lift?: number;
  liftCI?: Interval;
  n?: string;
  tiers: Tiers;
  publication?: string;
  networkMode?: string;
  retries: number;
  createdAt: string;
  completedAt?: string;
  durationSec?: number;
  message?: string;
  hasReport: boolean;
  trials: TrialsState;
  /** The skillci.dev/trials-withheld value: why the bundle was withheld, or why there is none (no-trials, disabled). */
  trialsReason?: string;
}

export interface RunFilter {
  skill?: string;
  agent?: string;
  model?: string;
  verdict?: string;
  phase?: string;
  publication?: string;
  trigger?: string;
  q?: string;
  sort: "newest" | "oldest" | "skill" | "lift";
}

export interface FilterOptions {
  skills: string[];
  agents: string[];
  models: string[];
  verdicts: string[];
  phases: string[];
  publications: string[];
  triggers: string[];
}

/** GET /api/v1/runs */
export interface RunList {
  items: RunSummary[];
  pagination: Pagination;
  filter: RunFilter;
  options: FilterOptions;
  refreshSeconds: number;
}

/** GET /api/v1/skills */
export interface SkillList {
  items: SkillSummary[];
  channelsError?: string;
}

export interface SkillSummary {
  skill: string;
  stable?: string;
  beta?: string;
  newest?: string;
  versions: number;
  runs: number;
  active: number;
  pending: number;
  running: number;
  lastRunAt?: string;
  heldBack: string[];
  rejected: string[];
  required: Ratio;
}

/** GET /api/v1/skills/{skill} */
export interface SkillDetail {
  skill: string;
  stable?: string;
  beta?: string;
  runs: number;
  channelsError?: string;
  columns: ModelColumn[];
  rows: VersionRow[];
  refreshSeconds: number;
}

export interface ModelColumn {
  agent: string;
  agentVersion?: string;
  model: string;
  entry: string;
  required: boolean;
}

export interface VersionRow {
  version: string;
  digest: string;
  channels: string[];
  /** One per column; null where the entry never ran on this digest. */
  cells: (MatrixCell | null)[];
  decision: Decision;
}

export interface MatrixCell {
  run: string;
  phase: Phase;
  verdict?: Verdict;
  withSkill?: number;
  withoutSkill?: number;
  lift?: number;
  liftCI?: Interval;
  n?: string;
  tiers: Tiers;
  trials: TrialsState;
}

export interface Decision {
  kind: "pass" | "neutral" | "regressed" | "failed";
  headline: string;
  detail: string;
}

/** GET /api/v1/models */
export interface ModelList {
  source: string;
  error?: string;
  items: ModelEntry[];
}

export interface ModelEntry {
  agent: string;
  agentVersion?: string;
  model: string;
  entry: string;
  required: boolean;
  sandboxProfile?: string;
  listed: boolean;
  runs: number;
  failed: number;
  retries: number;
  verdicts: Record<string, number>;
  meanLift?: number;
  lastRunAt?: string;
}

/** The body of every API error response. */
export interface ApiErrorBody {
  error: {
    status: number;
    code: "bad_request" | "not_found" | "withheld" | "not_implemented" | "registry_error" | "unavailable" | "internal";
    message: string;
  };
}

/** GET /api/v1/runs/{name}[?job=] */
export interface RunDetail {
  run: RunSummary;
  specAgentVersion?: string;
  channels: string[];
  withSkillStdErr?: number;
  withoutSkillStdErr?: number;
  samples: Samples;
  findings: string[];
  conditions: Condition[];
  regressed?: string;
  inconclusive?: string;
  retried?: string;
  network: RunNetwork;
  sandbox: Sandbox;
  report: Report;
  trials: TrialGrid;
  attempts: RunAttempt[];
  refreshSeconds: number;
}

export interface Samples {
  withSkill: number[];
  withoutSkill: number[];
}

export interface Condition {
  type: string;
  status: string;
  reason: string;
  message: string;
  changedAt: string;
}

export interface RunNetwork {
  mode?: string;
  source?: string;
  declared: string[];
  hosts: string[];
  policy?: string;
  enforced: boolean;
  reason?: string;
  message?: string;
  legacy: boolean;
}

export interface Sandbox {
  profile?: string;
  specProfile?: string;
  warmPool?: string;
}

export interface Report {
  state: "none" | "missing" | "error" | "files" | "ok";
  error?: string;
  digest?: string;
  dimensions: Dimension[];
  activation?: Activation;
  files: ReportFile[];
}

export interface Dimension {
  key: string;
  label: string;
  withSkill: number;
  withoutSkill: number;
  delta: number;
  overall: boolean;
}

export interface Activation {
  status: string;
  activated: number;
  positive: number;
}

export interface ReportFile {
  name: string;
  /** A server page outside the app (/runs/{name}/report): link with a full page load. */
  href: string;
}

export interface RunAttempt {
  number: number;
  jobName: string;
  reportDigest?: string;
  message?: string;
  current: boolean;
  trials: TrialsState;
}

export interface TrialGrid {
  state: "stored" | "withheld" | "fallback" | "none" | "missing" | "error";
  reason?: string;
  job?: string;
  bundleDigest?: string;
  attemptsPerCase: number;
  cases: GridCase[];
  totals: TrialTotals;
  credentials: Credential[];
  network?: NetworkSummary;
  withheld: WithheldTrial[];
}

export interface GridCase {
  id: string;
  question: string;
  expectedBehaviors: string[];
  /** One cell per attempt (index attempt - 1); null when missing. */
  withSkill: (TrialCell | null)[];
  withoutSkill: (TrialCell | null)[];
}

export interface TrialCell {
  id: string;
  arm: Arm;
  case: string;
  attempt: number;
  status: "scored" | "unscored" | "error" | "withheld";
  score?: number;
  passed?: boolean;
  errorType?: string;
  durationSec?: number;
  agentSec?: number;
  steps: number;
  toolCalls: number;
  tokensIn?: number;
  tokensOut?: number;
  tokensCached?: number;
  costUsd?: number;
  costEstimated: boolean;
  criteria?: Ratio;
  skillInvoked?: boolean;
  hasSession: boolean;
  dropped: number;
}

export interface TrialTotals {
  trials: number;
  scored: number;
  unscored: number;
  withheld: number;
  tokensIn: number;
  tokensOut: number;
  tokensCached: number;
  costUsd: number;
  costEstimated: boolean;
  agentSec: number;
  durationSec: number;
}

export interface Credential {
  role: "agent" | "judge";
  env: string;
  host: string;
}

export interface NetworkSummary {
  state: NetworkState;
  reason?: string;
  forwarded: number;
  dropped: number;
}

export interface WithheldTrial {
  id: string;
  arm: Arm;
  case: string;
  attempt: number;
  reason: "secret-scan" | "size" | "limit" | "unreadable";
}

export interface RunRef {
  name: string;
  skill: string;
  version: string;
  agent: string;
  agentVersion?: string;
  model: string;
  entry: string;
  job: string;
  currentJob: boolean;
}

/** GET /api/v1/runs/{name}/trials/{trial}[?job=] */
export interface TrialSession {
  run: RunRef;
  trial: TrialCell;
  header: TrialHeader;
  metadata: TrialMetadata;
  log: TrialLog;
  criteria: Criteria;
  findings: Finding[];
  files: FileChange[];
  judge: JudgeMetric[];
  network?: TrialNetwork;
  truncations: Truncation[];
  redaction: RedactionCounts;
  downloads: Downloads;
}

export interface TrialHeader {
  prompt: string;
  status: "scored" | "unscored" | "error";
  score?: number;
  armMean?: number;
  dimensions: TrialDimension[];
  error?: TrialError;
  final?: string;
}

export interface TrialDimension {
  key: string;
  label: string;
  score: number;
  armMean?: number;
}

export interface TrialError {
  type: string;
  message: string;
  occurredAt?: string;
  traceback?: string;
}

export interface TrialMetadata {
  agent: string;
  agentVersion?: string;
  model: string;
  provider?: string;
  arm: Arm;
  case: string;
  attempt: number;
  attemptsPerCase: number;
  workspace?: string;
  skill: SkillUse;
  credentials: Credential[];
  startedAt?: string;
  finishedAt?: string;
  durationSec?: number;
  phases: PhaseTiming[];
  tokens: Tokens;
  costUsd?: number;
  costEstimated: boolean;
  criteria: Ratio;
  trigger: string;
  pushedBy?: string;
  schemaVersion?: string;
}

export interface SkillUse {
  state: "invoked" | "loaded" | "not-used" | "unknown";
  expected?: string;
  matched?: string;
  reason?: string;
}

export interface PhaseTiming {
  name: "environment_setup" | "agent_setup" | "agent_execution" | "verifier";
  label: string;
  startedAt: string;
  finishedAt: string;
  durationSec: number;
}

export interface Tokens {
  in?: number;
  out?: number;
  cached?: number;
}

export interface TrialLog {
  entries: LogEntry[];
  /** One per category in CATEGORIES order, zero counts included. */
  counts: CategoryCount[];
  jumps: Jumps;
}

export interface CategoryCount {
  category: Category;
  count: number;
  focus: boolean;
}

export interface Jumps {
  firstTool?: string;
  skill?: string;
  firstError?: string;
  final?: string;
}

export interface LogEntry {
  /** Anchor id: s-<step>, s-<step>-<n>, j-<metric> or n-<n>. */
  id: string;
  /** ATIF step_id; 0 for judge and network entries. */
  step: number;
  category: Category;
  source: "user" | "agent" | "system" | "judge" | "network";
  at?: string;
  offsetSec?: number;
  title: string;
  text?: string;
  textTruncated: boolean;
  tool?: ToolCall;
  usage?: Usage;
  flow?: FlowRow;
  metric?: string;
  error: boolean;
  /** Accessible name, for example "Step 12, Bash tool call, failed". */
  label: string;
}

export interface ToolCall {
  name: string;
  callId: string;
  kind: "shell" | "read" | "write" | "edit" | "patch" | "skill" | "web" | "other";
  command?: string;
  path?: string;
  input: string;
  output?: ToolOutput;
}

export interface ToolOutput {
  preview: string;
  bytes: number;
  lines: number;
  truncated: boolean;
  shortened: boolean;
  error: boolean;
  /** Full text, text/plain, served by the API: open with a full page load. */
  href: string;
}

export interface Usage {
  tokens: Tokens;
  costUsd?: number;
  estimated: boolean;
}

export interface Criteria {
  passed: number;
  total: number;
  behaviors: BehaviorResult[];
  behaviorSteps: number[];
  accuracy?: AccuracyCriteria;
  checks: Check[];
}

export interface BehaviorResult {
  index: number;
  text: string;
  passed?: boolean;
  reason?: string;
}

export interface AccuracyCriteria {
  criteria: NamedCheck[];
  reason: string;
  steps: number[];
}

export interface NamedCheck {
  name: string;
  passed: boolean;
}

export interface Check {
  metric: string;
  name: string;
  label: string;
  passed?: boolean;
  score?: number;
  reason?: string;
  steps: number[];
}

export interface Finding {
  kind: "friction" | "opportunity";
  rule: string;
  metric?: string;
  title: string;
  detail?: string;
  steps: number[];
  runLevel: boolean;
}

export interface FileChange {
  path: string;
  op: "write" | "edit" | "patch" | "command";
  step: number;
  entryId: string;
  tool: string;
  content?: string;
  hunks: Hunk[];
  patch?: string;
  captured: boolean;
  truncated: boolean;
}

export interface Hunk {
  old: string;
  new: string;
}

export interface JudgeMetric {
  key: string;
  label: string;
  score?: number;
  passed?: boolean;
  reason?: string;
  model?: string;
  steps: number[];
  /** details.<metric> of reward.json as stored. */
  details?: unknown;
}

export interface TrialNetwork {
  state: NetworkState;
  reason?: string;
  from?: string;
  to?: string;
  dns: DNSRow[];
  flows: FlowRow[];
  truncated: boolean;
  lost: number;
}

export interface DNSRow {
  name: string;
  count: number;
  firstAt: string;
  lastAt: string;
  rcode?: string;
}

export interface FlowRow {
  verdict: "FORWARDED" | "DROPPED" | "ERROR" | "AUDIT";
  destination: string;
  ip: string;
  port: number;
  protocol: string;
  count: number;
  firstAt: string;
  lastAt: string;
  reason?: string;
  step: number;
}

export interface Truncation {
  pointer: string;
  originalBytes: number;
}

export interface RedactionCounts {
  version: string;
  exact: number;
  pattern: number;
  envDump: number;
  render: number;
}

export interface Downloads {
  trajectory?: string;
}

/** GET /api/v1/runs/{name}/cases/{case}/compare */
export interface Compare {
  run: RunRef;
  case: CaseInfo;
  attempt: number;
  attemptsPerCase: number;
  withSkill: CompareSide;
  withoutSkill: CompareSide;
}

export interface CaseInfo {
  id: string;
  question: string;
  expectedBehaviors: string[];
}

export interface CompareSide {
  trial?: TrialCell;
  missing?: string;
  header?: TrialHeader;
  metadata?: TrialMetadata;
  criteria?: Criteria;
  log: LogEntry[];
  files: FileChange[];
}
