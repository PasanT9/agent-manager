/**
 * Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com).
 *
 * WSO2 LLC. licenses this file to you under the Apache License,
 * Version 2.0 (the "License"); you may not use this file except
 * in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing,
 * software distributed under the License is distributed on an
 * "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
 * KIND, either express or implied.  See the License for the
 * specific language governing permissions and limitations
 * under the License.
 */

import type { ReactNode } from "react";
import {
  Alert,
  Box,
  Button,
  Card,
  CardContent,
  Chip,
  Divider,
  formatRelativeTime,
  ListingTable,
  Skeleton,
  Stack,
  Tooltip,
  Typography,
} from "@wso2/oxygen-ui";
import {
  Bell,
  BrainCircuit,
  CalendarDays,
  Clock,
  Coins,
  Filter,
  Flag,
  Layers,
  Mail,
  Pencil,
  Power,
  ServerCrash,
  Target,
  Trash2,
  Users,
  Wallet,
  Zap,
} from "@wso2/oxygen-ui-icons-react";
import { useNavigate } from "react-router-dom";
import {
  useBudgetUsage,
  useDeleteBudget,
  useGetBudget,
  useUpdateBudget,
} from "@agent-management-platform/api-client";
import { useFeatureManagement } from "@agent-management-platform/feature-management";
import { useConfirmationDialog } from "@agent-management-platform/shared-component";
import {
  PageLayout,
  PageMetaItem,
  StateNotice,
} from "@agent-management-platform/views";
import {
  BUDGET_PROJECT_ANNOTATION,
  type Budget,
  type BudgetConsumer,
  type BudgetPeriod,
} from "@agent-management-platform/types";
import { amount, formatUsd, type CostNames } from "../costFormat";
import { PERIOD_LABELS, inSentence } from "./budgetLabels";
import { useEntityNames } from "../useEntityNames";

interface BudgetDetailProps {
  orgName?: string;
  budgetName?: string;
  /** The Budgets list this page returns to. */
  backHref: string;
  /** The budget's edit page. */
  editHref: string;
}

const KIND_LABEL: Record<BudgetConsumer["type"], string> = {
  project: "Project",
  agent: "Agent",
  apiKey: "API key",
  attribute: "Custom attribute",
};

/** USD with cents, for limits and spend on this page. */
function usd(value: number): string {
  return value.toLocaleString("en-US", { style: "currency", currency: "USD" });
}

function utcDate(d: Date): string {
  return d.toLocaleDateString("en-US", {
    month: "short",
    day: "numeric",
    timeZone: "UTC",
  });
}

/** The current period's first day and the day it resets (UTC). */
function periodSpan(
  start: string,
  period: BudgetPeriod,
): { from: string; resets: string } | null {
  const d = new Date(`${start}T00:00:00Z`);
  if (Number.isNaN(d.getTime())) return null;
  const from = utcDate(d);
  if (period === "day") d.setUTCDate(d.getUTCDate() + 1);
  else if (period === "week") d.setUTCDate(d.getUTCDate() + 7);
  else d.setUTCMonth(d.getUTCMonth() + 1);
  return { from, resets: utcDate(d) };
}

/** A rounded square holding an icon, as the reference design's section and row icons. */
function IconTile({
  children,
  accent,
}: {
  children: ReactNode;
  accent?: boolean;
}) {
  return (
    <Box
      sx={{
        width: 44,
        height: 44,
        borderRadius: 1.5,
        display: "grid",
        placeItems: "center",
        flexShrink: 0,
        bgcolor: "action.hover",
        color: accent ? "primary.main" : "text.secondary",
      }}
    >
      {children}
    </Box>
  );
}

/** Block reads as a warning; Allow as a go-ahead. */
function ActionChip({ action }: { action: "block" | "allow" }) {
  return (
    <Chip
      size="small"
      variant="outlined"
      color={action === "block" ? "warning" : "default"}
      label={action === "block" ? "Block" : "Allow"}
    />
  );
}

function actionSentence(
  action: "block" | "allow",
  limit: number,
  blockAt: number,
): string {
  return action === "block"
    ? `Requests are blocked when spending reaches ${usd((limit * blockAt) / 100)} (${blockAt}%).`
    : "Allow requests to continue and send warnings only.";
}

/** A labelled figure beside an accent icon, inside a card. */
function Figure({
  icon,
  label,
  value,
  hint,
}: {
  icon: ReactNode;
  label: string;
  value: ReactNode;
  hint?: string;
}) {
  return (
    <Stack direction="row" spacing={2} alignItems="center">
      <Box sx={{ color: "primary.main", display: "flex" }}>{icon}</Box>
      <Box>
        <Typography variant="body2" color="text.secondary">
          {label}
        </Typography>
        <Typography variant="h6" sx={{ fontVariantNumeric: "tabular-nums" }}>
          {value}
        </Typography>
        {hint && (
          <Typography variant="caption" color="text.secondary">
            {hint}
          </Typography>
        )}
      </Box>
    </Stack>
  );
}

/** This period's spend against the limit. */
function ThisPeriod({ budget }: { budget: Budget }) {
  const { limit: lim, warnings } = budget.spec;
  const limit = amount(lim.amount);
  const period = budget.status?.currentPeriod;
  const spent = amount(period?.spent);
  const share = limit > 0 ? (spent / limit) * 100 : 0;
  const blockAt = lim.action === "block" ? (lim.blockAt ?? 100) : undefined;
  const span = period ? periodSpan(period.periodStart, lim.period) : null;
  const fill =
    share >= (blockAt ?? 100)
      ? "error.main"
      : share >= (blockAt ?? 100) * 0.8
        ? "warning.main"
        : "primary.main";
  // Ticks where warnings fire and where blocking starts.
  // Ticks for thresholds within the limit; ones above 100% are listed under the bar,
  // since the bar ends at the limit.
  const thresholds = warnings?.thresholds ?? [];
  const beyond = thresholds.filter((t) => t > 100).sort((a, b) => a - b);
  const marks = [
    ...thresholds
      .filter((t) => t <= 100 && t !== blockAt)
      .map((t) => ({ at: t, block: false })),
    ...(blockAt !== undefined && blockAt < 100
      ? [{ at: blockAt, block: true }]
      : []),
  ];
  return (
    <Card variant="outlined" sx={{ height: "100%" }}>
      <CardContent sx={{ p: 3 }}>
        <Stack
          direction="row"
          justifyContent="space-between"
          alignItems="center"
        >
          <Typography variant="subtitle1" fontWeight={600}>
            This period
          </Typography>
          {span && (
            <Stack
              direction="row"
              spacing={1}
              alignItems="center"
              color="text.secondary"
            >
              <CalendarDays size={16} />
              <Typography variant="body2">
                {PERIOD_LABELS[lim.period]} · {span.from} – {span.resets} (UTC)
              </Typography>
            </Stack>
          )}
        </Stack>
        <Stack
          direction="row"
          justifyContent="space-between"
          alignItems="baseline"
          sx={{ mt: 2 }}
        >
          <Stack
            direction="row"
            spacing={1}
            alignItems="baseline"
            flexWrap="wrap"
          >
            <Typography
              fontWeight={700}
              sx={{
                fontSize: { xs: 32, md: 40 },
                lineHeight: 1.1,
                fontVariantNumeric: "tabular-nums",
              }}
            >
              {usd(spent)}
            </Typography>
            <Typography variant="h6" color="text.secondary" fontWeight={400}>
              of {usd(limit)} used
            </Typography>
          </Stack>
          <Typography
            variant="h5"
            fontWeight={600}
            sx={{ fontVariantNumeric: "tabular-nums" }}
          >
            {Math.round(share)}%
          </Typography>
        </Stack>
        <Box sx={{ position: "relative", mt: 2 }}>
          <Box
            role="meter"
            aria-label="Share of the limit spent"
            aria-valuenow={Math.round(share)}
            aria-valuemin={0}
            aria-valuemax={100}
            sx={{
              height: 14,
              borderRadius: 7,
              bgcolor: "action.hover",
              overflow: "hidden",
            }}
          >
            <Box
              sx={{
                width: `${Math.min(share, 100)}%`,
                height: "100%",
                bgcolor: fill,
                borderRadius: 7,
              }}
            />
          </Box>
          {marks.map((m) => (
            <Tooltip
              key={`${m.block}-${m.at}`}
              title={m.block ? `Blocks at ${m.at}%` : `Warns at ${m.at}%`}
            >
              <Box
                sx={{
                  position: "absolute",
                  left: `${m.at}%`,
                  top: -3,
                  width: 3,
                  height: 20,
                  borderRadius: 1,
                  transform: "translateX(-50%)",
                  bgcolor: m.block ? "error.main" : "text.secondary",
                }}
              />
            </Tooltip>
          ))}
        </Box>
        {beyond.length > 0 && (
          <Typography
            variant="caption"
            color="text.secondary"
            component="div"
            sx={{ mt: 1.5 }}
          >
            Also warns at{" "}
            {beyond.length === 1
              ? `${beyond[0]}%`
              : `${beyond
                  .slice(0, -1)
                  .map((t) => `${t}%`)
                  .join(", ")} and ${beyond[beyond.length - 1]}%`}
            , after the limit is exceeded.
          </Typography>
        )}
        <Divider sx={{ my: 2.5 }} />
        <Stack
          direction={{ xs: "column", sm: "row" }}
          divider={<Divider orientation="vertical" flexItem />}
          spacing={3}
        >
          <Box sx={{ flex: 1 }}>
            <Figure
              icon={<Coins size={28} />}
              {...remainingFigure(limit, spent, blockAt)}
            />
          </Box>
          <Box sx={{ flex: 1 }}>
            <Figure
              icon={<CalendarDays size={28} />}
              label="Resets"
              value={span?.resets ?? "—"}
              hint="UTC"
            />
          </Box>
        </Stack>
      </CardContent>
    </Card>
  );
}

/**
 * What is left this period: up to the block point for a Block budget (requests stop
 * there, not at the amount), up to the amount for an Allow budget, which can go over.
 */
function remainingFigure(limit: number, spent: number, blockAt?: number) {
  if (blockAt !== undefined) {
    const point = (limit * blockAt) / 100;
    return {
      label: "Remaining",
      value: usd(Math.max(point - spent, 0)),
      hint:
        spent >= point
          ? "Requests are blocked"
          : `Until blocking at ${usd(point)}`,
    };
  }
  return spent > limit
    ? {
        label: "Over the limit",
        value: usd(spent - limit),
        hint: "Allowed; warnings only",
      }
    : { label: "Remaining", value: usd(limit - spent) };
}

/** A small card: an icon tile, a title and its content. */
interface Titled {
  icon: ReactNode;
  title: string;
}

function SideCard({ icon, title, children }: Titled & { children: ReactNode }) {
  return (
    <Card variant="outlined" sx={{ flex: 1 }}>
      <CardContent sx={{ p: 3, "&:last-child": { pb: 3 } }}>
        <Stack direction="row" spacing={2}>
          <IconTile accent>{icon}</IconTile>
          <Box>
            <Typography variant="subtitle1" fontWeight={600}>
              {title}
            </Typography>
            <Box sx={{ mt: 0.75 }}>{children}</Box>
          </Box>
        </Stack>
      </CardContent>
    </Card>
  );
}

/** A card heading: an icon, a title and a one-line purpose. */
function CardHeading({ icon, title, purpose }: Titled & { purpose: string }) {
  return (
    <Stack direction="row" spacing={2} alignItems="flex-start" sx={{ mb: 2 }}>
      <Box sx={{ color: "text.secondary", display: "flex", mt: 0.25 }}>
        {icon}
      </Box>
      <Box>
        <Typography variant="h6">{title}</Typography>
        <Typography variant="body2" color="text.secondary">
          {purpose}
        </Typography>
      </Box>
    </Stack>
  );
}

/** One line of the scope: what it is, its value in bold, a note below. */
function ScopeRow({
  icon,
  label,
  value,
  note,
  badge,
}: {
  icon: ReactNode;
  label: string;
  value: ReactNode;
  note?: ReactNode;
  badge?: string;
}) {
  return (
    <Stack direction="row" spacing={2} alignItems="center" sx={{ py: 1.75 }}>
      <IconTile>{icon}</IconTile>
      <Box sx={{ flex: 1, minWidth: 0 }}>
        <Typography variant="body2" color="text.secondary">
          {label}
        </Typography>
        <Typography
          variant="body1"
          fontWeight={600}
          sx={{ wordBreak: "break-word" }}
        >
          {value}
        </Typography>
        {note && (
          <Typography variant="body2" color="text.secondary" component="div">
            {note}
          </Typography>
        )}
      </Box>
      {badge && <Chip size="small" variant="outlined" label={badge} />}
    </Stack>
  );
}

const allocationText = (a?: string) =>
  a === "separate" ? "Separate allowance each" : "Shared allowance";

/** A consumer or exemption as the page names it: display names where known. */
function entityText(
  e: { type: string; id?: string; project?: string; expression?: string },
  names: CostNames,
): string {
  if (e.type === "attribute") return e.expression ?? "";
  if (!e.id)
    return `Every ${inSentence(KIND_LABEL[e.type as BudgetConsumer["type"]])}`;
  if (e.type === "project") return names.project?.[e.id] ?? e.id;
  if (e.type === "agent") {
    const agent = e.project
      ? (names.agent?.[`${e.project}/${e.id}`] ?? e.id)
      : e.id;
    return e.project
      ? `${agent} (${names.project?.[e.project] ?? e.project})`
      : agent;
  }
  return e.id;
}

function Scope({ budget, orgName }: { budget: Budget; orgName: string }) {
  const { spec, metadata } = budget;
  const names = useEntityNames(orgName);
  const owner = metadata.annotations?.[BUDGET_PROJECT_ANNOTATION];
  const consumers = spec.consumers ?? [];
  const exemptions = spec.exemptions ?? [];
  return (
    <Card variant="outlined" sx={{ height: "100%" }}>
      <CardContent sx={{ p: 3 }}>
        <CardHeading
          icon={<Target size={24} />}
          title="Scope"
          purpose="The resources and consumers subject to this budget."
        />
        <Stack divider={<Divider flexItem />}>
          <ScopeRow
            icon={<BrainCircuit size={20} />}
            label="Resource"
            value={
              spec.resource?.id
                ? (names.llmProvider?.[spec.resource.id] ?? spec.resource.id)
                : "LLM provider"
            }
            note={spec.resource?.id ? "LLM provider" : "Any LLM provider"}
            badge={
              spec.resource?.id
                ? undefined
                : allocationText(spec.resource?.allocation)
            }
          />
          <ScopeRow
            icon={<Users size={20} />}
            label="Consumers"
            value={
              consumers.length
                ? consumers
                    // A named agent's project is shown with the agent.
                    .filter(
                      (c) =>
                        !(
                          c.type === "project" &&
                          consumers.some(
                            (a) => a.type === "agent" && a.project === c.id,
                          )
                        ),
                    )
                    .map((c) => entityText(c, names))
                    .join(", ")
                : "Everyone"
            }
            note={
              consumers.length
                ? consumers
                    .map(
                      (c) =>
                        `${KIND_LABEL[c.type]} · ${inSentence(allocationText(c.allocation))}`,
                    )
                    .join(" — ")
                : owner
                  ? `All traffic in project ${names.project?.[owner] ?? owner}`
                  : "All traffic in the organization"
            }
          />
          <ScopeRow
            icon={<Filter size={20} />}
            label="Exemptions"
            value={
              exemptions.length
                ? exemptions.map((e) => entityText(e, names)).join(", ")
                : "None"
            }
            note={
              exemptions.length
                ? "Not counted, though they match"
                : "All matching traffic counts."
            }
          />
        </Stack>
      </CardContent>
    </Card>
  );
}

/** A label and its value on one line, after an icon. */
function AlertRow({
  icon,
  label,
  children,
}: {
  icon: ReactNode;
  label: string;
  children: ReactNode;
}) {
  return (
    <Stack
      direction="row"
      spacing={2}
      alignItems="center"
      sx={{ py: 1.75, px: 1 }}
    >
      <Box sx={{ color: "text.secondary", display: "flex" }}>{icon}</Box>
      <Typography variant="body1" sx={{ width: 140, flexShrink: 0 }}>
        {label}
      </Typography>
      <Box sx={{ flex: 1 }}>{children}</Box>
    </Stack>
  );
}

function Alerts({ budget }: { budget: Budget }) {
  const { limit: lim, warnings } = budget.spec;
  const limit = amount(lim.amount);
  return (
    <Card variant="outlined" sx={{ height: "100%" }}>
      <CardContent sx={{ p: 3 }}>
        <CardHeading
          icon={<Bell size={24} />}
          title="Alerts"
          purpose="Warning thresholds and notification channels."
        />
        <Stack divider={<Divider flexItem />}>
          <AlertRow icon={<Flag size={22} />} label="Warn at">
            {warnings?.thresholds.length ? (
              <Stack direction="row" spacing={0.75} flexWrap="wrap" useFlexGap>
                {warnings.thresholds.map((t) => (
                  <Chip
                    key={t}
                    size="small"
                    label={`${t}% · ${formatUsd((limit * t) / 100)}`}
                  />
                ))}
              </Stack>
            ) : (
              <Typography variant="body1" fontWeight={500}>
                No warnings
              </Typography>
            )}
          </AlertRow>
          <AlertRow icon={<Mail size={22} />} label="Notify">
            {warnings?.channels.length ? (
              <Stack direction="row" spacing={0.75} flexWrap="wrap" useFlexGap>
                {warnings.channels.map((c) => (
                  <Chip key={c} size="small" variant="outlined" label={c} />
                ))}
              </Stack>
            ) : (
              <Typography variant="body1" fontWeight={500}>
                None
              </Typography>
            )}
          </AlertRow>
        </Stack>
      </CardContent>
    </Card>
  );
}

/** Spend per allowance, when the budget counts consumers separately. */
function Allowances({ budget, orgName }: { budget: Budget; orgName?: string }) {
  const limit = amount(budget.spec.limit.amount);
  const usage = useBudgetUsage(
    { orgName, budgetName: budget.metadata.name },
    { limit: 100 },
  );
  const allowances = (usage.data?.allowances ?? []).filter(
    (a) => Object.keys(a.allowance).length > 0,
  );
  if (usage.error) {
    return (
      <Alert
        severity="error"
        action={
          <Button
            color="inherit"
            size="small"
            onClick={() => void usage.refetch()}
          >
            Retry
          </Button>
        }
      >
        Couldn&apos;t load the spend per allowance.
      </Alert>
    );
  }
  if (!allowances.length) return null;
  return (
    <Card variant="outlined">
      <CardContent sx={{ p: 3 }}>
        <CardHeading
          icon={<Layers size={24} />}
          title="Spend per allowance"
          purpose={`Each consumer counted separately has its own ${usd(limit)} this period.`}
        />
        <ListingTable>
          <ListingTable.Head>
            <ListingTable.Row>
              <ListingTable.Cell>Allowance</ListingTable.Cell>
              <ListingTable.Cell align="right">Spent</ListingTable.Cell>
              <ListingTable.Cell align="right">Of its limit</ListingTable.Cell>
              <ListingTable.Cell align="right">Charged $0</ListingTable.Cell>
            </ListingTable.Row>
          </ListingTable.Head>
          <ListingTable.Body>
            {allowances.map((a) => {
              const value = amount(a.spent);
              return (
                <ListingTable.Row key={JSON.stringify(a.allowance)}>
                  <ListingTable.Cell>
                    {Object.values(a.allowance).join(" · ")}
                  </ListingTable.Cell>
                  <ListingTable.Cell align="right">
                    {formatUsd(value)}
                  </ListingTable.Cell>
                  <ListingTable.Cell align="right">
                    {limit > 0 ? `${Math.round((value / limit) * 100)}%` : "—"}
                  </ListingTable.Cell>
                  <ListingTable.Cell align="right">
                    {a.unpricedRequests}
                  </ListingTable.Cell>
                </ListingTable.Row>
              );
            })}
          </ListingTable.Body>
        </ListingTable>
      </CardContent>
    </Card>
  );
}

/** A budget: this period's spend, its enforcement, scope and limit; Edit, Disable, Delete. */
export function BudgetDetail({
  orgName,
  budgetName,
  backHref,
  editHref,
}: BudgetDetailProps) {
  const navigate = useNavigate();
  const { hasAccess } = useFeatureManagement();
  const canUpdate = hasAccess("budget_update");
  const canDelete = hasAccess("budget_delete");
  const {
    data: budget,
    isLoading,
    error,
  } = useGetBudget({ orgName, budgetName });
  const { mutate: update, isPending: isUpdating } = useUpdateBudget({
    orgName,
    budgetName,
  });
  const { mutate: remove, isPending: isDeleting } = useDeleteBudget();
  const { addConfirmation } = useConfirmationDialog();

  const fromGateway = budget?.status?.origin === "gateway_api";
  const inactive = budget?.spec.status === "inactive";
  const name =
    budget?.spec.displayName ?? budget?.metadata.name ?? budgetName ?? "";
  const owner = budget?.metadata.annotations?.[BUDGET_PROJECT_ANNOTATION];
  const ownerName = useEntityNames(orgName ?? "").project?.[owner ?? ""];
  const updatedAt = budget?.status?.updatedAt ?? budget?.status?.createdAt;

  const toggle = () => {
    if (!budget) return;
    // The kind goes back whole with the status flipped; the server sets `status`.
    const { apiVersion, kind, metadata, spec } = budget;
    update({
      apiVersion,
      kind,
      metadata,
      spec: { ...spec, status: inactive ? "active" : "inactive" },
    });
  };

  const confirmDelete = () =>
    addConfirmation({
      title: `Delete ${name}?`,
      description:
        "The budget stops counting and blocking at once, and its spend history is deleted. " +
        "This can't be undone.",
      confirmButtonText: "Delete",
      confirmButtonColor: "error",
      confirmButtonIcon: <Trash2 size={16} />,
      onConfirm: () =>
        remove(
          { orgName, budgetName },
          { onSuccess: () => navigate(backHref) },
        ),
    });

  const gatewayNote =
    "Created on a gateway: only its name, description and warnings change here.";
  // Each action shows only with its own access: update for Edit and Disable, delete for Delete.
  const actions = budget && (canUpdate || canDelete) && (
    <Stack direction="row" spacing={1}>
      {canUpdate && (
        <Button
          variant="outlined"
          startIcon={<Pencil size={16} />}
          onClick={() => navigate(editHref)}
        >
          Edit
        </Button>
      )}
      {canUpdate && (
        <Tooltip title={fromGateway ? gatewayNote : ""}>
          <span>
            <Button
              variant="outlined"
              startIcon={<Power size={16} />}
              onClick={toggle}
              disabled={fromGateway || isUpdating}
            >
              {inactive ? "Enable" : "Disable"}
            </Button>
          </span>
        </Tooltip>
      )}
      {canDelete && (
        <Tooltip title={fromGateway ? gatewayNote : ""}>
          <span>
            <Button
              variant="outlined"
              color="error"
              startIcon={<Trash2 size={16} />}
              onClick={confirmDelete}
              disabled={fromGateway || isDeleting}
            >
              Delete
            </Button>
          </span>
        </Tooltip>
      )}
    </Stack>
  );

  const unpriced = budget?.status?.currentPeriod?.unpricedRequests ?? 0;
  const lim = budget?.spec.limit;

  return (
    <PageLayout
      title={name}
      description={budget?.spec.description}
      backHref={backHref}
      backLabel="Back to budgets"
      isLoading={isLoading}
      avatar={{ children: <Wallet size={22} />, color: "primary.main" }}
      titleTail={
        budget && (
          <Stack direction="row" spacing={1} alignItems="center" sx={{ ml: 1 }}>
            <Chip
              size="small"
              variant="outlined"
              color={inactive ? "default" : "success"}
              label={inactive ? "Inactive" : "Active"}
            />
            <Chip
              size="small"
              label={owner ? `Project ${ownerName ?? owner}` : "Organization"}
            />
            {fromGateway && (
              <Chip size="small" variant="outlined" label="From a gateway" />
            )}
          </Stack>
        )
      }
      meta={
        updatedAt && (
          <PageMetaItem icon={<Clock size={12} />} label="Last updated">
            {`Last updated ${formatRelativeTime(new Date(updatedAt))}`}
          </PageMetaItem>
        )
      }
      actions={actions}
    >
      {error ? (
        <StateNotice
          icon={ServerCrash}
          tone="error"
          title="Couldn't load the budget"
          description="It may have been deleted. Go back to the budgets and try again."
        />
      ) : isLoading || !budget || !lim ? (
        <Stack spacing={2.5}>
          <Skeleton variant="rounded" height={260} />
          <Stack direction={{ xs: "column", lg: "row" }} spacing={2.5}>
            <Skeleton variant="rounded" height={340} sx={{ flex: 1 }} />
            <Skeleton variant="rounded" height={340} sx={{ flex: 1 }} />
          </Stack>
        </Stack>
      ) : (
        <Stack spacing={2.5}>
          {fromGateway && <Alert severity="info">{gatewayNote}</Alert>}
          {inactive && (
            <Alert severity="warning">
              Disabled: its spend is kept, but nothing is counted or blocked
              until it is enabled.
            </Alert>
          )}
          <Box
            sx={{
              display: "grid",
              gridTemplateColumns: {
                xs: "1fr",
                lg: "minmax(0, 2fr) minmax(0, 1fr)",
              },
              gap: 2.5,
            }}
          >
            <ThisPeriod budget={budget} />
            <Stack spacing={2.5}>
              <SideCard icon={<Zap size={22} />} title="Enforcement">
                <ActionChip action={lim.action} />
                <Typography
                  variant="body2"
                  color="text.secondary"
                  sx={{ mt: 0.75 }}
                >
                  {actionSentence(
                    lim.action,
                    amount(lim.amount),
                    lim.blockAt ?? 100,
                  )}
                </Typography>
              </SideCard>
              <SideCard icon={<Layers size={22} />} title="Unpriced requests">
                <Typography variant="h5" fontWeight={700}>
                  {unpriced}
                </Typography>
                <Typography variant="body2" color="text.secondary">
                  Requests charged $0 this period, not in the spend
                </Typography>
              </SideCard>
            </Stack>
          </Box>
          <Box
            sx={{
              display: "grid",
              gridTemplateColumns: {
                xs: "1fr",
                lg: "repeat(2, minmax(0, 1fr))",
              },
              gap: 2.5,
            }}
          >
            <Scope budget={budget} orgName={orgName ?? ""} />
            <Alerts budget={budget} />
          </Box>
          <Allowances budget={budget} orgName={orgName} />
        </Stack>
      )}
    </PageLayout>
  );
}
