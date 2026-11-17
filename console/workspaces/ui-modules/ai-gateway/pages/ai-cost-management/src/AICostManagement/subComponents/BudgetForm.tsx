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

import {
  useEffect,
  useMemo,
  useState,
  type MouseEvent,
  type ReactNode,
} from "react";
import {
  Alert,
  Autocomplete,
  Box,
  Button,
  Card,
  CardContent,
  Chip,
  Divider,
  IconButton,
  InputAdornment,
  Link,
  Menu,
  MenuItem,
  Skeleton,
  Stack,
  TextField,
  ToggleButton,
  ToggleButtonGroup,
  Tooltip,
  Typography,
} from "@wso2/oxygen-ui";
import { Lock, Plus, ServerCrash, X } from "@wso2/oxygen-ui-icons-react";
import { useNavigate, useSearchParams } from "react-router-dom";
import {
  useCreateBudget,
  useGetBudget,
  useListAgents,
  useListBudgets,
  useListEventDestinations,
  useListLLMProviders,
  useListOrgAgents,
  useListProjects,
  useUpdateBudget,
} from "@agent-management-platform/api-client";
import { useUnsavedChangesGuard } from "@agent-management-platform/shared-component";
import {
  PageLayout,
  StateNotice,
  TextInput,
} from "@agent-management-platform/views";
import {
  INPUT_LIMITS,
  type BudgetAllocation,
} from "@agent-management-platform/types";
import {
  budgetFromForm,
  defaultExpression,
  EMPTY_FORM,
  exemptionKinds,
  expressionPlaceholder,
  formFor,
  formFromBudget,
  handleFor,
  isCelKind,
  newConsumer,
  newExemption,
  startFromQuery,
  serverFieldError,
  validateBudgetForm,
  type BudgetFormErrors,
  type BudgetFormState,
  type ConsumerRow,
  type ExemptionRow,
  type ScopeKind,
} from "../budgetForm";
import { PERIOD_LABELS } from "./budgetLabels";

interface BudgetFormPageProps {
  orgName?: string;
  /** Set on a project's page: the new budget is owned by this project. */
  projectName?: string;
  /** Set to edit an existing budget. */
  budgetName?: string;
  /** Where Cancel returns to. */
  backHref: string;
  backLabel: string;
  /** A budget's detail page, opened after saving. */
  viewHref: (budgetName: string) => string;
}

/** The form's three parts and six cards, in page order (UX 3). */
const PARTS = [
  {
    id: "about",
    title: "About the budget",
    cards: [{ id: "details", n: 1, title: "Budget details" }],
  },
  {
    id: "scope",
    title: "Scope",
    cards: [
      { id: "resources", n: 2, title: "Resources" },
      { id: "consumers", n: 3, title: "Consumers" },
      { id: "exemptions", n: 4, title: "Exemptions" },
    ],
  },
  {
    id: "limit-and-alerts",
    title: "Limit and alerts",
    cards: [
      { id: "limit", n: 5, title: "Limit" },
      { id: "warnings", n: 6, title: "Warnings" },
    ],
  },
];

const KIND_LABEL: Record<ScopeKind, string> = {
  project: "Project",
  agent: "Agent",
  apiKey: "API key",
  team: "Team",
  user: "User",
  attribute: "Custom attribute",
};
const CONSUMER_KINDS: ScopeKind[] = [
  "project",
  "agent",
  "apiKey",
  "team",
  "user",
  "attribute",
];
const THRESHOLD_SUGGESTIONS = [50, 80, 90, 100];
const PILL = { borderRadius: 999, textTransform: "none" } as const;

type Option = { value: string; label: string };

/** A list the form loads (providers, projects, agents, channels), for its menus' states. */
export interface ListState {
  noun: string;
  loading?: boolean;
  failed?: boolean;
  retry?: () => void;
}

/** "Couldn't load …" with a Retry, shown where the list's options would be. */
function LoadFailed({ list }: { list: ListState }) {
  return (
    <Stack direction="row" spacing={1} alignItems="center">
      <Typography variant="body2" color="error.main">
        Couldn&apos;t load {list.noun}.
      </Typography>
      {list.retry && (
        <Button
          size="small"
          variant="text"
          // onMouseDown, so the click lands before the menu closes on blur.
          onMouseDown={(e) => {
            e.preventDefault();
            list.retry?.();
          }}
        >
          Retry
        </Button>
      )}
    </Stack>
  );
}

const usd = (v: number) =>
  v.toLocaleString("en-US", { style: "currency", currency: "USD" });

/** "PART n OF 3", the part's title and purpose, then a rule. */
function PartHeading({ n, id }: { n: number; id: string }) {
  const part = PARTS[n - 1];
  return (
    <Box id={id} sx={{ scrollMarginTop: 80, pt: n === 1 ? 0 : 2 }}>
      <Typography
        variant="caption"
        color="primary.main"
        fontWeight={600}
        sx={{ letterSpacing: "0.08em" }}
      >
        PART {n} OF 3
      </Typography>
      <Typography variant="h5" sx={{ mt: 0.25 }}>
        {part.title}
      </Typography>
      <Divider sx={{ mt: 2 }} />
    </Box>
  );
}

/** A numbered card with its purpose; optional ones say so, locked ones show a lock. */
function FormCard({
  id,
  n,
  title,
  purpose,
  optional,
  locked,
  children,
}: {
  id: string;
  n: number;
  title: string;
  purpose: string;
  optional?: boolean;
  locked?: boolean;
  children: ReactNode;
}) {
  return (
    <Card id={id} variant="outlined" sx={{ scrollMarginTop: 80 }}>
      <Box sx={{ px: 3, py: 2.5 }}>
        <Stack direction="row" spacing={1.5} alignItems="center">
          <Box
            sx={{
              width: 28,
              height: 28,
              borderRadius: "50%",
              display: "grid",
              placeItems: "center",
              bgcolor: "action.hover",
              color: "primary.main",
              typography: "body2",
              fontWeight: 700,
              flexShrink: 0,
            }}
          >
            {n}
          </Box>
          <Typography variant="h6">{title}</Typography>
          {optional && (
            <Chip size="small" variant="outlined" label="Optional" />
          )}
          {locked && (
            <Tooltip title="Fixed once the budget is created. Create a new budget to change it.">
              <Box sx={{ display: "flex", color: "text.secondary" }}>
                <Lock size={16} />
              </Box>
            </Tooltip>
          )}
        </Stack>
        <Typography
          variant="body2"
          color="text.secondary"
          sx={{ mt: 0.5, ml: 5.5 }}
        >
          {purpose}
        </Typography>
      </Box>
      <Divider />
      <CardContent sx={{ p: 3, "&:last-child": { pb: 3 } }}>
        {children}
      </CardContent>
    </Card>
  );
}

/** A static label over a field, as the console's TextInput draws it. */
export function Field({
  label,
  required,
  children,
}: {
  label: string;
  required?: boolean;
  children: ReactNode;
}) {
  return (
    <Box sx={{ width: "100%" }}>
      <Typography variant="body2" sx={{ mb: 0.75 }}>
        {label}
        {required && (
          <Typography component="span" color="primary.main">
            {" *"}
          </Typography>
        )}
      </Typography>
      {children}
    </Box>
  );
}

/** A pill of choices; one is on. */
export function Segmented<T extends string>({
  value,
  options,
  onChange,
  disabled,
  label,
}: {
  value: T;
  options: { value: T; label: string }[];
  onChange: (v: T) => void;
  disabled?: boolean;
  label: string;
}) {
  return (
    <ToggleButtonGroup
      exclusive
      fullWidth
      size="small"
      value={value}
      onChange={(_, v: T | null) => v && onChange(v)}
      disabled={disabled}
      aria-label={label}
      sx={{ "& .MuiToggleButton-root": { textTransform: "none", px: 2 } }}
    >
      {options.map((o) => (
        <ToggleButton key={o.value} value={o.value}>
          {o.label}
        </ToggleButton>
      ))}
    </ToggleButtonGroup>
  );
}

/** Shared or separate allowances, with what each means for this kind. */
function AllocationField({
  value,
  onChange,
  disabled,
  each,
}: {
  value: BudgetAllocation;
  onChange: (v: BudgetAllocation) => void;
  disabled?: boolean;
  /** What gets its own allowance, e.g. "LLM provider", "user". */
  each: string;
}) {
  return (
    <Field label="Budget allocation">
      <Segmented
        label="Budget allocation"
        value={value}
        disabled={disabled}
        onChange={onChange}
        options={[
          { value: "shared", label: "Shared allowance" },
          { value: "separate", label: "Separate allowances" },
        ]}
      />
      <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
        {value === "separate"
          ? `Each matching ${each} gets its own allowance.`
          : "All matching usage draws from one allowance."}
      </Typography>
    </Field>
  );
}

/** Picks a project, an agent or an LLM provider from a list, or names an API key. */
function ValuePicker({
  kind,
  value,
  onChange,
  options,
  disabled,
  error,
  placeholder,
  list,
}: {
  kind: ScopeKind;
  value: string;
  onChange: (v: string) => void;
  options: Option[];
  disabled?: boolean;
  error?: string;
  placeholder: string;
  list?: ListState;
}) {
  const empty =
    kind === "agent"
      ? "No agents yet. Add one from a project's Agents page."
      : kind === "project" && placeholder.includes("LLM provider")
        ? "No LLM providers yet. Add one under Resources → LLM service providers."
        : "No projects yet. Add one from Projects.";
  if (kind === "apiKey") {
    return (
      <TextInput
        size="small"
        fullWidth
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        disabled={disabled}
        error={!!error}
        helperText={error}
        maxLength={INPUT_LIMITS.HANDLE}
        hideCharacterCount
      />
    );
  }
  const selected = options.find((o) => o.value === value) ?? null;
  return (
    <Autocomplete
      size="small"
      fullWidth
      options={options}
      value={selected}
      onChange={(_, o) => onChange(o?.value ?? "")}
      getOptionLabel={(o) => o.label}
      isOptionEqualToValue={(a, b) => a.value === b.value}
      disabled={disabled}
      loading={list?.loading}
      loadingText={`Loading ${list?.noun ?? "options"}…`}
      noOptionsText={list?.failed ? <LoadFailed list={list} /> : empty}
      renderInput={(params) => (
        <TextField
          {...params}
          placeholder={placeholder}
          error={!!error}
          helperText={error}
        />
      )}
    />
  );
}

/** One consumer condition, in its own box. */
function ConditionBox({
  n,
  title = "Condition",
  locked,
  note,
  onRemove,
  children,
}: {
  n: number;
  /** "Condition" for consumers, "Exemption" for exemptions. */
  title?: string;
  locked?: boolean;
  note?: string;
  onRemove?: () => void;
  children: ReactNode;
}) {
  return (
    <Box sx={{ border: 1, borderColor: "divider", borderRadius: 1.5, p: 2.5 }}>
      <Stack direction="row" justifyContent="space-between" alignItems="center">
        <Typography variant="subtitle1" fontWeight={600}>
          {title} {n}
        </Typography>
        {locked ? (
          <Stack
            direction="row"
            spacing={0.75}
            alignItems="center"
            color="text.secondary"
          >
            <Lock size={14} />
            <Typography variant="body2">Locked</Typography>
          </Stack>
        ) : (
          onRemove && (
            <IconButton
              size="small"
              aria-label={`Remove ${title.toLowerCase()} ${n}`}
              onClick={onRemove}
            >
              <X size={16} />
            </IconButton>
          )
        )}
      </Stack>
      {note && (
        <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>
          {note}
        </Typography>
      )}
      <Box sx={{ mt: 2 }}>{children}</Box>
    </Box>
  );
}

function AndRule() {
  return (
    <Typography
      variant="body2"
      color="primary.main"
      fontWeight={700}
      align="center"
      sx={{ py: 1.5, letterSpacing: "0.05em" }}
    >
      AND
    </Typography>
  );
}

/** A rounded "+ Kind" button that adds a condition, a warning or a channel. */
export function AddChip({
  label,
  onClick,
  disabled,
}: {
  label: string;
  onClick: (e: MouseEvent<HTMLElement>) => void;
  disabled?: boolean;
}) {
  return (
    <Button
      variant="outlined"
      size="small"
      startIcon={<Plus size={14} />}
      onClick={onClick}
      disabled={disabled}
      sx={PILL}
    >
      {label}
    </Button>
  );
}

function OnThisPage({ locked }: { locked: string[] }) {
  const lockedCards = new Set(locked);
  return (
    <Box
      sx={{ position: "sticky", top: 88, display: { xs: "none", lg: "block" } }}
    >
      <Typography variant="caption" color="text.secondary" fontWeight={600}>
        ON THIS PAGE
      </Typography>
      <Stack spacing={1.5} sx={{ mt: 1.5 }}>
        {PARTS.map((part) => (
          <Box key={part.id}>
            <Link
              href={`#${part.id}`}
              underline="hover"
              color="text.primary"
              variant="body2"
            >
              {part.title}
            </Link>
            <Stack spacing={0.5} sx={{ mt: 0.5, pl: 1.5 }}>
              {part.cards.map((card) => (
                <Stack
                  key={card.id}
                  direction="row"
                  spacing={0.5}
                  alignItems="center"
                >
                  <Link
                    href={`#${card.id}`}
                    underline="hover"
                    color="text.secondary"
                    variant="body2"
                  >
                    {card.n}. {card.title}
                  </Link>
                  {lockedCards.has(card.id) && <Lock size={12} />}
                </Stack>
              ))}
            </Stack>
          </Box>
        ))}
      </Stack>
    </Box>
  );
}

/** Thresholds as chips, with an inline field to add one. */
export function Thresholds({
  value,
  onChange,
  error,
}: {
  value: number[];
  onChange: (v: number[]) => void;
  error?: string;
}) {
  const [adding, setAdding] = useState(false);
  const [draft, setDraft] = useState("");
  const add = () => {
    const n = Number.parseInt(draft, 10);
    if (Number.isFinite(n))
      onChange([...new Set([...value, n])].sort((a, b) => a - b));
    setDraft("");
    setAdding(false);
  };
  return (
    <Box>
      {value.length === 0 && !adding && (
        <Typography variant="body2" color="text.secondary" sx={{ mb: 1.5 }}>
          No warning thresholds configured.
        </Typography>
      )}
      <Stack
        direction="row"
        spacing={1}
        alignItems="center"
        flexWrap="wrap"
        useFlexGap
      >
        {value.map((t) => (
          <Chip
            key={t}
            label={`${t}%`}
            onDelete={() => onChange(value.filter((x) => x !== t))}
          />
        ))}
        {adding ? (
          <Stack direction="row" spacing={1} alignItems="center">
            <Autocomplete
              freeSolo
              size="small"
              options={THRESHOLD_SUGGESTIONS.filter(
                (s) => !value.includes(s),
              ).map(String)}
              inputValue={draft}
              onInputChange={(_, v) => setDraft(v.replace(/[^\d]/g, ""))}
              sx={{ width: 150 }}
              renderInput={(params) => (
                <TextField
                  {...params}
                  autoFocus
                  placeholder="e.g. 80"
                  onKeyDown={(e) => {
                    if (e.key === "Enter") {
                      e.preventDefault();
                      add();
                    }
                  }}
                  slotProps={{
                    input: {
                      ...params.InputProps,
                      endAdornment: (
                        <InputAdornment position="end">%</InputAdornment>
                      ),
                    },
                    htmlInput: {
                      ...params.inputProps,
                      "aria-label": "Threshold",
                    },
                  }}
                />
              )}
            />
            <Button
              size="small"
              variant="contained"
              onClick={add}
              disabled={!draft}
            >
              Add
            </Button>
            <Button
              size="small"
              variant="text"
              onClick={() => setAdding(false)}
            >
              Cancel
            </Button>
          </Stack>
        ) : (
          <AddChip label="Add warning" onClick={() => setAdding(true)} />
        )}
      </Stack>
      <Typography
        variant="caption"
        color={error ? "error.main" : "text.secondary"}
        component="div"
        sx={{ mt: 1.5 }}
      >
        {error ??
          "Enter a percentage between 1 and 1000. Values above 100 warn after the limit is exceeded."}
      </Typography>
    </Box>
  );
}

/** The organization's event destinations as chips, with a menu to add one. */
export function Channels({
  value,
  onChange,
  options,
  error,
  list,
}: {
  value: string[];
  onChange: (v: string[]) => void;
  options: string[];
  error?: string;
  list?: ListState;
}) {
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);
  const available = options.filter((o) => !value.includes(o));
  return (
    <Field label="Notification channels">
      <Stack
        direction="row"
        spacing={1}
        alignItems="center"
        flexWrap="wrap"
        useFlexGap
      >
        {value.map((c) => (
          <Chip
            key={c}
            label={c}
            variant="outlined"
            onDelete={() => onChange(value.filter((x) => x !== c))}
          />
        ))}
        <AddChip
          label="Add notification channel"
          onClick={(e) => setAnchor(e.currentTarget)}
        />
        <Menu open={!!anchor} anchorEl={anchor} onClose={() => setAnchor(null)}>
          {list?.loading ? (
            <MenuItem disabled>Loading notification channels…</MenuItem>
          ) : list?.failed ? (
            <Box sx={{ px: 2, py: 1 }}>
              <LoadFailed list={list} />
            </Box>
          ) : available.length ? (
            available.map((c) => (
              <MenuItem
                key={c}
                onClick={() => {
                  onChange([...value, c]);
                  setAnchor(null);
                }}
              >
                {c}
              </MenuItem>
            ))
          ) : (
            <MenuItem disabled>
              {options.length
                ? "Every channel is added"
                : "No event destinations yet"}
            </MenuItem>
          )}
        </Menu>
      </Stack>
      <Typography
        variant="caption"
        color={error ? "error.main" : "text.secondary"}
        component="div"
        sx={{ mt: 1 }}
      >
        {error ??
          "Notification channels are the organization's event destinations, managed on the " +
            "Events page."}
      </Typography>
    </Field>
  );
}

/** Create or edit a budget: three parts, six numbered cards (UX 3-4). */
export function BudgetFormPage({
  orgName,
  projectName,
  budgetName,
  backHref,
  backLabel,
  viewHref,
}: BudgetFormPageProps) {
  const navigate = useNavigate();
  const editing = !!budgetName;
  const existing = useGetBudget({ orgName, budgetName });
  // A budget created on a gateway: only its name, description and warnings change here.
  const fromGateway = existing.data?.status?.origin === "gateway_api";
  const budgets = useListBudgets({ orgName }, { limit: 100 });
  const providers = useListLLMProviders({ orgName });
  const projects = useListProjects({ orgName }, { limit: 100 });
  const orgAgents = useListOrgAgents({ orgName });
  const projectAgents = useListAgents(
    { orgName, projName: projectName },
    { limit: 100 },
  );
  const destinations = useListEventDestinations(orgName);
  const create = useCreateBudget({ orgName });
  const update = useUpdateBudget({ orgName, budgetName });

  // "Advanced options" on an entity's page opens this form with the entity filled in.
  // Those values are where the form starts, not changes, so they don't make it dirty.
  const [searchParams] = useSearchParams();
  const [start] = useState(() =>
    editing ? EMPTY_FORM : formFor(startFromQuery(searchParams)),
  );
  const [form, setForm] = useState<BudgetFormState>(start);
  const [initial, setInitial] = useState<BudgetFormState>(start);
  const [submitted, setSubmitted] = useState(false);
  useEffect(() => {
    if (existing.data) {
      const loaded = formFromBudget(existing.data);
      setForm(loaded);
      setInitial(loaded);
    }
  }, [existing.data]);

  const isDirty = JSON.stringify(form) !== JSON.stringify(initial);
  const { allowNavigation } = useUnsavedChangesGuard(isDirty);
  // A failed save's message sits next to the field the server named, until the next change.
  const [serverErrors, setServerErrors] = useState<BudgetFormErrors>({});
  const errors: BudgetFormErrors = {
    ...(submitted ? validateBudgetForm(form) : {}),
    ...serverErrors,
  };
  const set = <K extends keyof BudgetFormState>(
    key: K,
    value: BudgetFormState[K],
  ) => {
    setServerErrors({});
    setForm((f) => ({ ...f, [key]: value }));
  };

  const providerOptions: Option[] = (providers.data?.providers ?? []).map(
    (p) => ({
      value: p.id,
      label: p.name,
    }),
  );
  const projectOptions: Option[] = (projects.data?.projects ?? []).map((p) => ({
    value: p.name,
    label: p.displayName || p.name,
  }));
  // Agents keyed project/agent: names are unique only within a project.
  const agentOptions: Option[] = projectName
    ? (projectAgents.data?.agents ?? []).map((a) => ({
        value: `${projectName}/${a.name}`,
        label: a.displayName || a.name,
      }))
    : (orgAgents.data?.agents ?? []).map((a) => ({
        value: `${a.projectName}/${a.name}`,
        label: `${a.projectDisplayName || a.projectName} / ${a.displayName || a.name}`,
      }));
  const projectLabel = (name: string) =>
    projectOptions.find((p) => p.value === name)?.label ?? name;
  const channelOptions = (destinations.data?.destinations ?? []).map(
    (d) => d.name,
  );
  const agentsQuery = projectName ? projectAgents : orgAgents;
  const providerList: ListState = {
    noun: "LLM providers",
    loading: providers.isLoading,
    failed: !!providers.error,
    retry: () => void providers.refetch(),
  };
  const projectList: ListState = {
    noun: "projects",
    loading: projects.isLoading,
    failed: !!projects.error,
    retry: () => void projects.refetch(),
  };
  const agentList: ListState = {
    noun: "agents",
    loading: agentsQuery.isLoading,
    failed: !!agentsQuery.error,
    retry: () => void agentsQuery.refetch(),
  };
  const channelList: ListState = {
    noun: "notification channels",
    loading: destinations.isLoading,
    failed: !!destinations.error,
    retry: () => void destinations.refetch(),
  };
  // On a project's page the project is the owner and is added for you.
  const consumerKinds = CONSUMER_KINDS.filter(
    (k) => !(projectName && k === "project"),
  );
  const usedKinds = new Set(form.consumers.map((c) => c.type));
  const allowedExemptionKinds = exemptionKinds(form.consumers).filter(
    (k) => !(projectName && k === "project"),
  );
  const taken = useMemo(
    () => (budgets.data?.budgets ?? []).map((b) => b.metadata.name),
    [budgets.data],
  );
  const handle = handleFor(form.displayName, taken);
  const limit = Number(form.amount) || 0;
  const blockAt = Number(form.blockAt) || 0;

  const updateConsumer = (i: number, patch: Partial<ConsumerRow>) =>
    set(
      "consumers",
      form.consumers.map((c, j) => (j === i ? { ...c, ...patch } : c)),
    );
  const setConsumerAllocation = (i: number, allocation: BudgetAllocation) => {
    const c = form.consumers[i];
    // Team and User rows swap between a named value and the claim itself.
    const expression =
      c.type === "team" || c.type === "user"
        ? defaultExpression(c.type, allocation)
        : c.expression;
    updateConsumer(i, { allocation, expression });
  };
  const updateExemption = (i: number, patch: Partial<ExemptionRow>) =>
    set(
      "exemptions",
      form.exemptions.map((e, j) => (j === i ? { ...e, ...patch } : e)),
    );
  const agentValue = (r: { id: string; project: string }) =>
    r.id ? `${r.project}/${r.id}` : "";
  const splitAgent = (v: string) => {
    const [project = "", id = ""] = v.split("/");
    return { project, id };
  };
  const pickerPlaceholder = (kind: ScopeKind) =>
    kind === "apiKey"
      ? "e.g. support-prod"
      : `Choose ${kind === "agent" ? "an agent" : kind === "project" ? "a project" : "one"}`;

  const onSaveError = (err: unknown) => {
    const { field, message } = serverFieldError(err);
    setServerErrors({ [field]: message });
  };

  const save = () => {
    setSubmitted(true);
    if (Object.keys(validateBudgetForm(form)).length) return;
    const body = budgetFromForm(form, {
      name: handle,
      projectOwner: projectName,
      existing: existing.data,
    });
    const done = (name: string) =>
      allowNavigation(() => navigate(viewHref(name)));
    if (editing) {
      update.mutate(body, {
        onSuccess: () => done(body.metadata.name),
        onError: onSaveError,
      });
    } else {
      create.mutate(body, {
        onSuccess: (b) => done(b.metadata.name),
        onError: onSaveError,
      });
    }
  };

  const title = editing
    ? `Edit ${initial.displayName || budgetName}`
    : "Create budget";
  const saving = create.isPending || update.isPending;
  if (editing && existing.error) {
    return (
      <PageLayout
        title="Edit budget"
        backHref={backHref}
        backLabel={backLabel}
        avatar={null}
      >
        <StateNotice
          icon={ServerCrash}
          tone="error"
          title="Couldn't load the budget"
          description="It may have been deleted, or the server is unavailable."
          actions={
            <Button variant="outlined" onClick={() => void existing.refetch()}>
              Retry
            </Button>
          }
        />
      </PageLayout>
    );
  }
  if (editing && existing.isLoading) {
    return (
      <PageLayout
        title={title}
        backHref={backHref}
        backLabel={backLabel}
        avatar={null}
      >
        <Skeleton variant="rounded" height={480} />
      </PageLayout>
    );
  }

  // Condition boxes, numbered in order; an agent's project is its own locked box.
  let conditionNo = 0;
  const conditions = form.consumers.flatMap((c, i) => {
    const boxes: ReactNode[] = [];
    const kindLabel = KIND_LABEL[c.type].toLowerCase();
    if (c.type === "agent" && c.project) {
      conditionNo += 1;
      boxes.push(
        <ConditionBox
          key={`${i}-project`}
          n={conditionNo}
          locked
          note={
            `Added because ${c.id} belongs to ${projectLabel(c.project)}. ` +
            "Agent names are unique only within a project."
          }
        >
          <Stack direction={{ xs: "column", md: "row" }} spacing={3}>
            <Field label="Consumer type">
              <TextInput select size="small" fullWidth value="project" disabled>
                <MenuItem value="project">Project</MenuItem>
              </TextInput>
            </Field>
            <AllocationField
              value="shared"
              onChange={() => undefined}
              disabled
              each="project"
            />
          </Stack>
          <Box sx={{ mt: 2 }}>
            <Field label="Project">
              <TextInput
                size="small"
                fullWidth
                value={projectLabel(c.project)}
                disabled
              />
            </Field>
          </Box>
        </ConditionBox>,
      );
    }
    conditionNo += 1;
    boxes.push(
      <ConditionBox
        key={i}
        n={conditionNo}
        locked={editing}
        onRemove={() =>
          set(
            "consumers",
            form.consumers.filter((_, j) => j !== i),
          )
        }
      >
        <Stack direction={{ xs: "column", md: "row" }} spacing={3}>
          <Field label="Consumer type">
            <TextInput
              select
              size="small"
              fullWidth
              value={c.type}
              disabled={editing}
              onChange={(e) =>
                updateConsumer(
                  i,
                  newConsumer(e.target.value as ScopeKind, c.allocation),
                )
              }
            >
              {consumerKinds
                .filter(
                  (k) => k === c.type || k === "attribute" || !usedKinds.has(k),
                )
                .map((k) => (
                  <MenuItem key={k} value={k}>
                    {KIND_LABEL[k]}
                  </MenuItem>
                ))}
            </TextInput>
          </Field>
          <AllocationField
            value={c.allocation}
            onChange={(v) => setConsumerAllocation(i, v)}
            disabled={editing}
            each={kindLabel}
          />
        </Stack>
        <Box sx={{ mt: 2 }}>
          {isCelKind(c.type) ? (
            <Field label="CEL expression">
              <TextInput
                size="small"
                fullWidth
                multiline
                minRows={2}
                value={c.expression}
                disabled={editing}
                onChange={(e) =>
                  updateConsumer(i, { expression: e.target.value })
                }
                placeholder={expressionPlaceholder(c.type)}
                error={!!errors[`consumers.${i}`]}
                helperText={errors[`consumers.${i}`]}
                maxLength={INPUT_LIMITS.SHORT_TEXT}
                hideCharacterCount
                sx={{ "& textarea": { fontFamily: "monospace" } }}
              />
            </Field>
          ) : c.allocation === "separate" ? (
            <Typography variant="body2" color="text.secondary">
              Every {kindLabel} is counted, each with its own allowance.
            </Typography>
          ) : (
            <Field
              label={
                c.type === "apiKey" ? "API key name" : `Select ${kindLabel}`
              }
            >
              <ValuePicker
                kind={c.type}
                value={c.type === "agent" ? agentValue(c) : c.id}
                onChange={(v) =>
                  updateConsumer(
                    i,
                    c.type === "agent" ? splitAgent(v) : { id: v },
                  )
                }
                options={c.type === "agent" ? agentOptions : projectOptions}
                list={c.type === "agent" ? agentList : projectList}
                disabled={editing}
                error={errors[`consumers.${i}`]}
                placeholder={pickerPlaceholder(c.type)}
              />
            </Field>
          )}
        </Box>
      </ConditionBox>,
    );
    return boxes;
  });

  return (
    <PageLayout
      title={title}
      description={
        projectName && !editing ? (
          <span>
            The budget is owned by project <strong>{projectName}</strong>.
          </span>
        ) : undefined
      }
      backHref={backHref}
      backLabel={backLabel}
      avatar={null}
    >
      <Box
        sx={{
          display: "grid",
          gridTemplateColumns: {
            xs: "minmax(0, 1fr)",
            lg: "minmax(0, 1060px) 220px",
          },
          gap: 4,
          alignItems: "start",
        }}
      >
        <Stack spacing={3}>
          {fromGateway && (
            <Alert severity="info">
              This budget was created on a gateway. Its limit and scope are
              managed there; only the name, description and warnings can be
              changed here.
            </Alert>
          )}
          <PartHeading n={1} id="about" />
          <FormCard
            id="details"
            n={1}
            title="Budget details"
            purpose="Specify the budget name and description."
          >
            <Stack direction={{ xs: "column", md: "row" }} spacing={3}>
              <TextInput
                label="Name"
                required
                fullWidth
                value={form.displayName}
                onChange={(e) => set("displayName", e.target.value)}
                placeholder="e.g. Payments on OpenAI"
                error={!!errors.displayName}
                helperText={errors.displayName}
                maxLength={INPUT_LIMITS.NAME}
                hideCharacterCount
              />
              <TextInput
                label="Description (optional)"
                fullWidth
                multiline
                minRows={2}
                value={form.description}
                onChange={(e) => set("description", e.target.value)}
                error={!!errors.description}
                helperText={errors.description}
                placeholder="e.g. Payments team's OpenAI spend, funded by Finance"
                maxLength={INPUT_LIMITS.DESCRIPTION}
                hideCharacterCount
              />
            </Stack>
          </FormCard>

          <PartHeading n={2} id="scope" />
          <FormCard
            id="resources"
            n={2}
            title="Resources"
            purpose="Select the LLM providers subject to this budget."
            locked={editing}
          >
            <Stack direction={{ xs: "column", md: "row" }} spacing={3}>
              <Field label="Resource type">
                <TextInput
                  select
                  size="small"
                  fullWidth
                  value={form.resourceType}
                  disabled={editing}
                  onChange={(e) =>
                    set(
                      "resourceType",
                      e.target.value as BudgetFormState["resourceType"],
                    )
                  }
                >
                  <MenuItem value="all">All LLM providers</MenuItem>
                  <MenuItem value="provider">LLM provider</MenuItem>
                </TextInput>
              </Field>
              {form.resourceType === "provider" ? (
                <AllocationField
                  value={form.resourceAllocation}
                  onChange={(v) => set("resourceAllocation", v)}
                  disabled={editing}
                  each="LLM provider"
                />
              ) : (
                <Box sx={{ width: "100%", pt: { md: 4 } }}>
                  <Typography variant="body2" color="text.secondary">
                    A shared budget applies across all LLM providers.
                  </Typography>
                </Box>
              )}
            </Stack>
            {errors.providerId &&
              !(
                form.resourceType === "provider" &&
                form.resourceAllocation === "shared"
              ) && (
                <Alert severity="error" sx={{ mt: 2 }}>
                  {errors.providerId}
                </Alert>
              )}
            {form.resourceType === "provider" &&
              form.resourceAllocation === "shared" && (
                <Box sx={{ mt: 2 }}>
                  <Field label="Select resource">
                    <ValuePicker
                      kind="project"
                      value={form.providerId}
                      onChange={(v) => set("providerId", v)}
                      options={providerOptions}
                      list={providerList}
                      disabled={editing}
                      error={errors.providerId}
                      placeholder="Choose an LLM provider"
                    />
                  </Field>
                </Box>
              )}
          </FormCard>

          <FormCard
            id="consumers"
            n={3}
            title="Consumers"
            purpose={
              projectName
                ? "Define the consumers in this project subject to this budget."
                : "Define the consumers subject to this budget."
            }
            optional
            locked={editing}
          >
            {errors.consumers && (
              <Alert severity="error" sx={{ mb: 2 }}>
                {errors.consumers}
              </Alert>
            )}
            <Stack>
              {conditions.map((box, i) => (
                <Box key={i}>
                  {i > 0 && <AndRule />}
                  {box}
                </Box>
              ))}
            </Stack>
            {!editing && (
              <Box sx={{ mt: conditions.length ? 2.5 : 0 }}>
                {!conditions.length && (
                  <Typography
                    variant="body2"
                    color="text.secondary"
                    sx={{ mb: 2 }}
                  >
                    {projectName
                      ? "Specify consumer conditions. If none are configured, the budget applies " +
                        "to all consumers in this project."
                      : "Specify consumer conditions. If none are configured, the budget applies " +
                        "to all consumers."}
                  </Typography>
                )}
                <Typography variant="body2" sx={{ mb: 1 }}>
                  Add consumer condition
                </Typography>
                <Stack direction="row" spacing={1} flexWrap="wrap" useFlexGap>
                  {consumerKinds.map((k) => (
                    <AddChip
                      key={k}
                      label={KIND_LABEL[k]}
                      disabled={k !== "attribute" && usedKinds.has(k)}
                      onClick={() =>
                        set("consumers", [...form.consumers, newConsumer(k)])
                      }
                    />
                  ))}
                </Stack>
              </Box>
            )}
          </FormCard>

          <FormCard
            id="exemptions"
            n={4}
            title="Exemptions"
            purpose="Exclude specific consumers from budget enforcement."
            optional
            locked={fromGateway}
          >
            {errors.exemptions && (
              <Alert severity="error" sx={{ mb: 2 }}>
                {errors.exemptions}
              </Alert>
            )}
            <Stack spacing={2}>
              {form.exemptions.map((e, i) => (
                <ConditionBox
                  key={i}
                  n={i + 1}
                  title="Exemption"
                  locked={fromGateway}
                  onRemove={() =>
                    set(
                      "exemptions",
                      form.exemptions.filter((_, j) => j !== i),
                    )
                  }
                >
                  <Stack
                    direction={{ xs: "column", md: "row" }}
                    spacing={3}
                    alignItems="flex-start"
                  >
                    <Field label="Type">
                      <TextInput
                        select
                        size="small"
                        fullWidth
                        value={e.type}
                        disabled={fromGateway}
                        onChange={(ev) =>
                          updateExemption(
                            i,
                            newExemption(
                              ev.target.value as ExemptionRow["type"],
                            ),
                          )
                        }
                      >
                        {[...new Set([e.type, ...allowedExemptionKinds])].map(
                          (k) => (
                            <MenuItem key={k} value={k}>
                              {KIND_LABEL[k]}
                            </MenuItem>
                          ),
                        )}
                      </TextInput>
                    </Field>
                    {isCelKind(e.type) ? (
                      <Field label="CEL expression">
                        <TextInput
                          size="small"
                          fullWidth
                          value={e.expression}
                          disabled={fromGateway}
                          onChange={(ev) =>
                            updateExemption(i, { expression: ev.target.value })
                          }
                          placeholder={expressionPlaceholder(e.type)}
                          error={!!errors[`exemptions.${i}`]}
                          helperText={errors[`exemptions.${i}`]}
                          maxLength={INPUT_LIMITS.SHORT_TEXT}
                          hideCharacterCount
                          sx={{ "& input": { fontFamily: "monospace" } }}
                        />
                      </Field>
                    ) : (
                      <Field
                        label={`Select ${KIND_LABEL[e.type].toLowerCase()}`}
                      >
                        <ValuePicker
                          kind={e.type}
                          value={e.type === "agent" ? agentValue(e) : e.id}
                          onChange={(v) =>
                            updateExemption(
                              i,
                              e.type === "agent" ? splitAgent(v) : { id: v },
                            )
                          }
                          options={
                            e.type === "agent" ? agentOptions : projectOptions
                          }
                          list={e.type === "agent" ? agentList : projectList}
                          error={errors[`exemptions.${i}`]}
                          placeholder={pickerPlaceholder(e.type)}
                          disabled={fromGateway}
                        />
                      </Field>
                    )}
                  </Stack>
                </ConditionBox>
              ))}
              {!fromGateway && (
                <Box>
                  <Tooltip
                    title={
                      allowedExemptionKinds.length
                        ? ""
                        : "Nothing is narrower than the budget's consumers."
                    }
                  >
                    <span>
                      <AddChip
                        label="Add exemption"
                        disabled={!allowedExemptionKinds.length}
                        onClick={() =>
                          set("exemptions", [
                            ...form.exemptions,
                            newExemption(allowedExemptionKinds[0]),
                          ])
                        }
                      />
                    </span>
                  </Tooltip>
                </Box>
              )}
            </Stack>
          </FormCard>

          <PartHeading n={3} id="limit-and-alerts" />
          <FormCard
            id="limit"
            n={5}
            locked={fromGateway}
            title="Limit"
            purpose="Define the spending threshold, reset period, and enforcement behavior."
          >
            <Stack spacing={3}>
              <Stack direction={{ xs: "column", md: "row" }} spacing={3}>
                <TextInput
                  label="Amount"
                  required
                  fullWidth
                  value={form.amount}
                  disabled={fromGateway}
                  onChange={(e) => set("amount", e.target.value)}
                  placeholder="e.g. 1000"
                  error={!!errors.amount}
                  helperText={errors.amount}
                  inputMode="decimal"
                  slotProps={{
                    input: {
                      startAdornment: (
                        <InputAdornment position="start">$</InputAdornment>
                      ),
                      endAdornment: (
                        <InputAdornment position="end">USD</InputAdornment>
                      ),
                    },
                  }}
                />
                <Field label="Period">
                  <Segmented
                    label="Period"
                    value={form.period}
                    disabled={editing}
                    onChange={(v) => set("period", v)}
                    options={(["day", "week", "month"] as const).map((p) => ({
                      value: p,
                      label: PERIOD_LABELS[p],
                    }))}
                  />
                  <Typography
                    variant="caption"
                    color="text.secondary"
                    component="div"
                    sx={{ mt: 1 }}
                  >
                    {editing
                      ? "Fixed once created."
                      : "Budget periods are calculated using UTC."}
                  </Typography>
                </Field>
              </Stack>
              <Field label="When the limit is reached">
                <Segmented
                  label="When the limit is reached"
                  value={form.action}
                  disabled={fromGateway}
                  onChange={(v) => set("action", v)}
                  options={[
                    { value: "block", label: "Block" },
                    { value: "allow", label: "Allow" },
                  ]}
                />
                <Typography
                  variant="body2"
                  color="text.secondary"
                  sx={{ mt: 1 }}
                >
                  {form.action === "block"
                    ? "Reject requests when accumulated spending reaches the configured threshold."
                    : "Allow requests to continue and send warnings only."}
                </Typography>
              </Field>
              {form.action === "block" && (
                <Box>
                  <TextInput
                    label="Block at"
                    value={form.blockAt}
                    disabled={fromGateway}
                    onChange={(e) => set("blockAt", e.target.value)}
                    error={!!errors.blockAt}
                    helperText={errors.blockAt}
                    inputMode="numeric"
                    slotProps={{
                      input: {
                        endAdornment: (
                          <InputAdornment position="end">%</InputAdornment>
                        ),
                      },
                    }}
                    sx={{ maxWidth: 280 }}
                  />
                  <Typography
                    variant="body2"
                    color="text.secondary"
                    sx={{ mt: 1 }}
                  >
                    {`Requests are blocked when spending reaches ${usd((limit * blockAt) / 100)}.`}
                  </Typography>
                </Box>
              )}
            </Stack>
          </FormCard>

          <FormCard
            id="warnings"
            n={6}
            title="Warnings"
            purpose="Configure warning thresholds and notification channels."
            optional
          >
            <Stack spacing={3}>
              <Field label="Thresholds">
                <Thresholds
                  value={form.thresholds}
                  onChange={(v) => set("thresholds", v)}
                  error={errors.thresholds}
                />
              </Field>
              {form.thresholds.length > 0 && (
                <Channels
                  value={form.channels}
                  onChange={(v) => set("channels", v)}
                  options={channelOptions}
                  list={channelList}
                  error={errors.channels}
                />
              )}
            </Stack>
          </FormCard>

          {errors.form ? (
            <Alert severity="error">{errors.form}</Alert>
          ) : (
            submitted &&
            Object.keys(errors).length > 0 && (
              <Alert severity="error">
                Fix the highlighted fields to save the budget.
              </Alert>
            )
          )}
          <Stack direction="row" spacing={1.5}>
            <Button
              variant="outlined"
              onClick={() => allowNavigation(() => navigate(backHref))}
              disabled={saving}
            >
              Cancel
            </Button>
            <Button variant="contained" onClick={save} disabled={saving}>
              {editing ? "Save changes" : "Create budget"}
            </Button>
          </Stack>
        </Stack>
        <OnThisPage
          locked={
            fromGateway
              ? ["resources", "consumers", "exemptions", "limit"]
              : editing
                ? ["resources", "consumers"]
                : []
          }
        />
      </Box>
    </PageLayout>
  );
}
