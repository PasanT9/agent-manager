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
  Chip,
  FormControlLabel,
  IconButton,
  InputAdornment,
  Menu,
  MenuItem,
  Radio,
  RadioGroup,
  Skeleton,
  Stack,
  TextField,
  ToggleButton,
  ToggleButtonGroup,
  Tooltip,
  Typography,
} from "@wso2/oxygen-ui";
import {
  ArrowLeft,
  ArrowRight,
  Lock,
  Plus,
  SearchX,
  ServerCrash,
  X,
} from "@wso2/oxygen-ui-icons-react";
import { useNavigate, useSearchParams } from "react-router-dom";
import {
  useCreateBudget,
  useGetBudget,
  useListBudgets,
  useListEventDestinations,
  useListLLMProviders,
  useListLLMProvidersAPIKeys,
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
  allocationFormula,
  budgetFromForm,
  defaultExpression,
  EMPTY_FORM,
  consumerBreaksExemptions,
  exemptionKinds,
  expressionPlaceholder,
  MAX_EXPRESSION_LENGTH,
  formFor,
  formFromBudget,
  handleFor,
  isCelKind,
  newConsumer,
  newExemption,
  startFromQuery,
  duplicateNameError,
  isBudgetNotFound,
  serverFieldError,
  validateBudgetForm,
  type AllocationFactor,
  type BudgetFormErrors,
  type BudgetFormState,
  type ConsumerRow,
  type ExemptionRow,
  type ScopeKind,
} from "../budgetForm";
import { PERIOD_LABELS, inSentence } from "./budgetLabels";

interface BudgetWizardProps {
  orgName?: string;
  /** Set to edit an existing budget. */
  budgetName?: string;
  /** The page's back link: the Budgets list, or the budget. */
  backHref: string;
  backLabel: string;
  /** After a save: the budget's name. */
  onSaved: (budgetName: string) => void;
}

/** The form's pages, in order. */
const STEPS = [
  { key: "details", label: "Details" },
  { key: "resource", label: "Resources" },
  { key: "consumers", label: "Consumers and exemptions" },
  { key: "limit", label: "Limits & alerts" },
] as const;

/** The page a field's error belongs to. */
function stepOfField(field: string): number {
  if (field === "displayName" || field === "description") return 0;
  if (field === "providerId") return 1;
  if (field.startsWith("consumers") || field.startsWith("exemptions")) return 2;
  return 3;
}

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

/** A titled section of a step; optional ones say so, locked ones show a lock. */
function Section({
  title,
  description,
  optional,
  locked,
  children,
}: {
  title: string;
  /** One line under the title, only where a choice needs it. */
  description?: string;
  optional?: boolean;
  locked?: boolean;
  children: ReactNode;
}) {
  return (
    <Box component="section">
      <Stack direction="row" spacing={1} alignItems="center">
        <Typography variant="subtitle1">
          {title}
          {optional && (
            <Typography component="span" variant="body2" color="text.secondary">
              {" "}
              (optional)
            </Typography>
          )}
        </Typography>
        {locked && (
          <Tooltip title="Fixed once the budget is created.">
            <Box sx={{ display: "flex", color: "text.secondary" }}>
              <Lock size={16} />
            </Box>
          </Tooltip>
        )}
      </Stack>
      {description && (
        <Typography variant="body2" color="text.secondary" sx={{ mt: 0.25 }}>
          {description}
        </Typography>
      )}
      <Box sx={{ mt: 2 }}>{children}</Box>
    </Box>
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

/** Two or more choices as radio buttons in a row. */
function ChoiceField<T extends string>({
  label,
  value,
  options,
  onChange,
  disabled,
}: {
  label: string;
  value: T;
  options: { value: T; label: string; description?: string }[];
  onChange: (v: T) => void;
  disabled?: boolean;
}) {
  const described = options.some((o) => o.description);
  return (
    <Field label={label}>
      <RadioGroup
        row={!described}
        value={value}
        onChange={(_, v) => onChange(v as T)}
        aria-label={label}
      >
        {options.map((o) => (
          <FormControlLabel
            key={o.value}
            value={o.value}
            control={<Radio size="small" />}
            disabled={disabled}
            sx={described ? { alignItems: "flex-start", mb: 1 } : undefined}
            label={
              o.description ? (
                <Box sx={{ pt: 0.75 }}>
                  <Typography variant="body2" fontWeight={500}>
                    {o.label}
                  </Typography>
                  <Typography variant="caption" color="text.secondary">
                    {o.description}
                  </Typography>
                </Box>
              ) : (
                <Typography variant="body2">{o.label}</Typography>
              )
            }
          />
        ))}
      </RadioGroup>
    </Field>
  );
}

/** "Specific agent" or "Each agent separately": a consumer's two allocations. */
function allocationChoices(
  kind: ScopeKind,
): { value: BudgetAllocation; label: string }[] {
  if (kind === "attribute") {
    return [
      { value: "shared", label: "Requests matching a condition" },
      { value: "separate", label: "Each value — independent limit" },
    ];
  }
  const noun = inSentence(KIND_LABEL[kind]);
  return [
    { value: "shared", label: `Specific ${noun}` },
    { value: "separate", label: `Each ${noun} — independent limit` },
  ];
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
    kind === "apiKey"
      ? "No API keys on the selected LLM providers. Add one on the LLM provider's page."
      : kind === "agent"
        ? "No agents yet. Add one from a project's Agents page."
        : kind === "project" && placeholder.includes("LLM provider")
          ? "No LLM providers yet. Add one under Resources → LLM service providers."
          : "No projects yet. Add one from Projects.";
  // A saved key no longer listed (deleted, or an agent's own key) stays selectable.
  if (kind === "apiKey" && value && !options.some((o) => o.value === value)) {
    options = [...options, { value, label: value }];
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
  label,
  locked,
  note,
  onRemove,
  children,
}: {
  n: number;
  /** "Condition" for consumers, "Exemption" for exemptions. */
  title?: string;
  /** What it is about, after the number: "Condition 1 — User". */
  label?: string;
  locked?: boolean;
  note?: string;
  onRemove?: () => void;
  children: ReactNode;
}) {
  return (
    <Box sx={{ border: 1, borderColor: "divider", borderRadius: 1.5, p: 2.5 }}>
      <Stack direction="row" justifyContent="space-between" alignItems="center">
        <Typography variant="body2" fontWeight={600}>
          {title} {n}
          {label ? ` — ${label}` : ""}
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
      variant="text"
      size="small"
      startIcon={<Plus size={14} />}
      onClick={onClick}
      disabled={disabled}
      sx={{ textTransform: "none", px: 1, ml: -1, typography: "body2" }}
    >
      {label}
    </Button>
  );
}

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
  const [draftError, setDraftError] = useState("");
  const add = () => {
    const n = Number.parseInt(draft, 10);
    // Refused here, with the reason next to the input, rather than added and flagged.
    if (!Number.isFinite(n) || n < 1 || n > 1000) {
      setDraftError("Enter a percentage from 1 to 1000.");
      return;
    }
    if (value.includes(n)) {
      setDraftError(`${n}% is already a threshold.`);
      return;
    }
    onChange([...new Set([...value, n])].sort((a, b) => a - b));
    setDraft("");
    setDraftError("");
    setAdding(false);
  };
  return (
    <Box>
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
              onInputChange={(_, v) => {
                setDraft(v.replace(/[^\d]/g, ""));
                setDraftError("");
              }}
              sx={{ width: 150 }}
              renderInput={(params) => (
                <TextField
                  {...params}
                  autoFocus
                  placeholder="e.g. 80"
                  error={!!draftError}
                  helperText={draftError || undefined}
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
              onClick={() => {
                setAdding(false);
                setDraft("");
                setDraftError("");
              }}
            >
              Cancel
            </Button>
          </Stack>
        ) : (
          <AddChip label="Add warning" onClick={() => setAdding(true)} />
        )}
      </Stack>
      {error && (
        <Typography
          variant="caption"
          color="error.main"
          component="div"
          sx={{ mt: 1.5 }}
        >
          {error}
        </Typography>
      )}
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
/**
 * Create or edit a budget, one step at a time, as the console's other wizards do (the
 * evaluation Monitor wizard): a page with a back link, Previous and Next under the
 * content.
 */
export function BudgetWizard({
  orgName,
  budgetName,
  backHref,
  backLabel,
  onSaved,
}: BudgetWizardProps) {
  const navigate = useNavigate();
  const editing = !!budgetName;
  const existing = useGetBudget({ orgName, budgetName });
  // A budget created on a gateway: only its name, description and warnings change here.
  const fromGateway = existing.data?.status?.origin === "gateway_api";
  const budgets = useListBudgets({ orgName }, { limit: 100 });
  const providers = useListLLMProviders({ orgName });
  const projects = useListProjects({ orgName }, { limit: 100 });
  const orgAgents = useListOrgAgents({ orgName });
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
  useEffect(() => {
    if (existing.data) {
      const loaded = formFromBudget(existing.data);
      setForm(loaded);
      setInitial(loaded);
    }
  }, [existing.data]);

  // API keys of the providers the budget covers: the one selected, or all of them.
  const scopedProviders = (providers.data?.providers ?? []).filter(
    (p) =>
      form.resourceType === "all" ||
      form.resourceAllocation === "separate" ||
      p.id === form.providerId,
  );
  const providerKeys = useListLLMProvidersAPIKeys(
    orgName,
    scopedProviders.map((p) => p.uuid),
  );

  // The pages: an edit can open any of them; a new budget goes forward with Next.
  const [step, setStep] = useState(0);
  // Pages whose errors are shown: those Next or Save has checked.
  const [checked, setChecked] = useState<Set<number>>(new Set());

  const isDirty = JSON.stringify(form) !== JSON.stringify(initial);
  const { allowNavigation } = useUnsavedChangesGuard(isDirty);
  // A check that failed brings the first field in error into view and focuses it.
  const [failedChecks, setFailedChecks] = useState(0);
  useEffect(() => {
    if (!failedChecks) return;
    const first = document.querySelector<HTMLElement>('[aria-invalid="true"]');
    first?.scrollIntoView({ behavior: "smooth", block: "center" });
    first?.focus({ preventScroll: true });
  }, [failedChecks, step]);
  // A failed save's message sits next to the field the server named, until the next change.
  const [serverErrors, setServerErrors] = useState<BudgetFormErrors>({});
  const duplicateName = duplicateNameError(
    form.displayName,
    budgets.data?.budgets ?? [],
    budgetName,
  );
  const allErrors: BudgetFormErrors = {
    ...validateBudgetForm(form),
    ...(duplicateName ? { displayName: duplicateName } : {}),
  };
  const errorsOn = (n: number) =>
    Object.keys(allErrors).filter((k) => stepOfField(k) === n);
  const errors: BudgetFormErrors = {
    ...Object.fromEntries(
      Object.entries(allErrors).filter(([k]) => checked.has(stepOfField(k))),
    ),
    ...serverErrors,
  };
  const patch = (changes: Partial<BudgetFormState>) => {
    setServerErrors({});
    setForm((f) => ({ ...f, ...changes }));
  };
  const set = <K extends keyof BudgetFormState>(
    key: K,
    value: BudgetFormState[K],
  ) => patch({ [key]: value } as Partial<BudgetFormState>);

  const providerOptions: Option[] = (providers.data?.providers ?? []).map(
    (p) => ({ value: p.id, label: p.name }),
  );
  // API keys by the name gateways see, shown as "Mock OpenAI / team-alpha-key"
  // (owner first, as agents are shown "Project / Agent").
  const apiKeyOptions: Option[] = providerKeys.keys.map(
    ({ providerId, key }) => ({
      value: key.name,
      label: `${
        scopedProviders.find((p) => p.uuid === providerId)?.name ??
        "LLM provider"
      } / ${key.displayName || key.name}`,
    }),
  );
  const apiKeyList: ListState = {
    noun: "API keys",
    loading: providerKeys.isLoading,
  };
  const projectOptions: Option[] = (projects.data?.projects ?? []).map((p) => ({
    value: p.name,
    label: p.displayName || p.name,
  }));
  // Agents keyed project/agent: names are unique only within a project.
  const agentOptions: Option[] = (orgAgents.data?.agents ?? []).map((a) => ({
    value: `${a.projectName}/${a.name}`,
    label: `${a.projectDisplayName || a.projectName} / ${a.displayName || a.name}`,
  }));
  const projectLabel = (name: string) =>
    projectOptions.find((p) => p.value === name)?.label ?? name;
  const agentLabel = (project: string, agent: string) =>
    (orgAgents.data?.agents ?? []).find(
      (a) => a.projectName === project && a.name === agent,
    )?.displayName || agent;
  const channelOptions = (destinations.data?.destinations ?? []).map(
    (d) => d.name,
  );
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
    loading: orgAgents.isLoading,
    failed: !!orgAgents.error,
    retry: () => void orgAgents.refetch(),
  };
  const channelList: ListState = {
    noun: "notification channels",
    loading: destinations.isLoading,
    failed: !!destinations.error,
    retry: () => void destinations.refetch(),
  };
  const usedKinds = new Set(form.consumers.map((c) => c.type));
  const allowedExemptionKinds = exemptionKinds(form.consumers);
  const taken = useMemo(
    () => (budgets.data?.budgets ?? []).map((b) => b.metadata.name),
    [budgets.data],
  );
  const handle = handleFor(form.displayName, taken);
  const limit = Number(form.amount) || 0;

  // The resource: all LLM providers (one budget, or one per provider), or one provider.
  const ALL = "*";
  const allProviders =
    form.resourceType === "all" || form.resourceAllocation === "separate";
  const perProvider =
    form.resourceType === "provider" && form.resourceAllocation === "separate";
  const resourceOptions: Option[] = [
    { value: ALL, label: "All LLM providers" },
    ...providerOptions,
  ];

  // What gets its own allowance, with today's counts where they can be known.
  const factors: AllocationFactor[] = [];
  if (perProvider) {
    factors.push({
      noun: "provider",
      plural: "providers",
      count: providers.data ? providerOptions.length : undefined,
    });
  }
  const sharedProject = form.consumers.find(
    (c) => c.type === "project" && c.allocation === "shared" && c.id,
  )?.id;
  // Exempted projects and agents get no allowance of their own.
  const exemptProjects = new Set(
    form.exemptions.filter((e) => e.type === "project").map((e) => e.id),
  );
  const exemptAgents = new Set(
    form.exemptions
      .filter((e) => e.type === "agent" && e.id)
      .map((e) => `${e.project}/${e.id}`),
  );
  for (const c of form.consumers) {
    if (c.allocation !== "separate") continue;
    if (c.type === "project") {
      const counted = projectOptions.filter(
        (p) => !exemptProjects.has(p.value),
      );
      factors.push({
        noun: "project",
        plural: "projects",
        count: projects.data ? counted.length : undefined,
      });
    } else if (c.type === "agent") {
      const agents = (orgAgents.data?.agents ?? []).filter(
        (a) =>
          (!sharedProject || a.projectName === sharedProject) &&
          !exemptAgents.has(`${a.projectName}/${a.name}`),
      );
      factors.push({
        noun: "agent",
        plural: "agents",
        count: orgAgents.data ? agents.length : undefined,
      });
    } else {
      const noun =
        c.type === "attribute" ? "value" : inSentence(KIND_LABEL[c.type]);
      factors.push({ noun, plural: `${noun}s` });
    }
  }
  const allocation = allocationFormula(limit, form.period, factors);
  // What the formula covers, from the selections: the resource, the shared consumers and
  // the exemptions.
  const scope: string[] = [];
  if (!perProvider) {
    scope.push(
      allProviders
        ? "Across all LLM providers"
        : `On ${providerOptions.find((p) => p.value === form.providerId)?.label ?? (form.providerId || "the selected LLM provider")}`,
    );
  }
  const shared = form.consumers.filter((c) => c.allocation === "shared");
  for (const c of shared) {
    if (c.type === "project")
      scope.push(`for project ${projectLabel(c.id) || "(not selected)"}`);
    else if (c.type === "agent")
      scope.push(
        `for agent ${c.id ? agentLabel(c.project, c.id) : "(not selected)"}`,
      );
    else if (c.type === "apiKey")
      scope.push(`for API key ${c.id || "(not named)"}`);
    else
      scope.push(
        `for requests matching the ${inSentence(KIND_LABEL[c.type])} condition`,
      );
  }
  if (!form.consumers.length) scope.push("for all consumers");
  if (form.exemptions.length) {
    scope.push(
      `excluding ${form.exemptions.length} ${form.exemptions.length === 1 ? "exemption" : "exemptions"}`,
    );
  }
  const scopeText = scope.length
    ? `${scope.join(", ").replace(/^./, (ch) => ch.toUpperCase())}.`
    : "";

  const updateConsumer = (i: number, change: Partial<ConsumerRow>) =>
    set(
      "consumers",
      form.consumers.map((c, j) => (j === i ? { ...c, ...change } : c)),
    );
  const setConsumerAllocation = (i: number, value: BudgetAllocation) => {
    const c = form.consumers[i];
    // Team and User rows swap between a named value and the claim itself.
    const expression =
      c.type === "team" || c.type === "user"
        ? defaultExpression(c.type, value)
        : c.expression;
    updateConsumer(i, { allocation: value, expression });
  };
  const updateExemption = (i: number, change: Partial<ExemptionRow>) =>
    set(
      "exemptions",
      form.exemptions.map((e, j) => (j === i ? { ...e, ...change } : e)),
    );
  const agentValue = (r: { id: string; project: string }) =>
    r.id ? `${r.project}/${r.id}` : "";
  const splitAgent = (v: string) => {
    const [project = "", id = ""] = v.split("/");
    return { project, id };
  };
  const pickerPlaceholder = (kind: ScopeKind) =>
    kind === "apiKey"
      ? "Choose an API key"
      : `Choose ${kind === "agent" ? "an agent" : kind === "project" ? "a project" : "one"}`;

  const check = (pages: number[]) => {
    setChecked((c) => new Set([...c, ...pages]));
    const failing = pages.filter((n) => errorsOn(n).length > 0);
    if (failing.length) {
      setStep(failing[0]);
      setFailedChecks((n) => n + 1);
    }
    return failing.length === 0;
  };
  const goTo = (n: number) => {
    setStep(n);
    window.scrollTo({ top: 0, behavior: "smooth" });
  };
  const next = () => {
    if (check([step])) goTo(step + 1);
  };

  const onSaveError = (err: unknown) => {
    const { field, message } = serverFieldError(err);
    setServerErrors({ [field]: message });
    setStep(field === "form" ? STEPS.length - 1 : stepOfField(field));
    setFailedChecks((n) => n + 1);
  };

  const save = () => {
    if (!check(STEPS.map((_, i) => i))) return;
    const body = budgetFromForm(form, {
      name: handle,
      existing: existing.data,
    });
    const done = (name: string) => allowNavigation(() => onSaved(name));
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

  // The add-condition menu's anchor; conditions whose expression is shown; the review.
  const [addAnchor, setAddAnchor] = useState<HTMLElement | null>(null);
  const [advanced, setAdvanced] = useState<Set<number>>(new Set());

  const title = editing
    ? `Edit ${initial.displayName || budgetName}`
    : "Create budget";
  const saving = create.isPending || update.isPending;
  const lockedSteps = fromGateway
    ? new Set([1, 2])
    : editing
      ? new Set([1])
      : new Set<number>();
  // The page, as the console's other wizards: a back link, the step in the description.
  const shell = (body: ReactNode, actions?: ReactNode) => (
    <PageLayout
      title={title}
      description={
        actions && !editing
          ? `Step ${step + 1} of ${STEPS.length} · ${STEPS[step].label}${lockedSteps.has(step) ? " · Fixed once created" : ""}`
          : undefined
      }
      disableIcon
      backHref={backHref}
      backLabel={backLabel}
    >
      <Stack spacing={3} sx={{ maxWidth: 960 }}>
        {body}
        {actions}
      </Stack>
    </PageLayout>
  );
  if (editing && existing.error) {
    const notFound = isBudgetNotFound(existing.error);
    return shell(
      notFound ? (
        <StateNotice
          icon={SearchX}
          title={`There is no budget named ${budgetName}`}
          description="It may have been deleted, or the link is wrong."
        />
      ) : (
        <StateNotice
          icon={ServerCrash}
          tone="error"
          title="Couldn't load the budget"
          description="The server didn't answer. Try again in a moment."
          actions={
            <Button variant="outlined" onClick={() => void existing.refetch()}>
              Retry
            </Button>
          }
        />
      ),
    );
  }
  if (editing && existing.isLoading) {
    return shell(<Skeleton variant="rounded" height={320} />);
  }

  const pluralOf = (k: ScopeKind) =>
    k === "attribute" ? "values" : `${inSentence(KIND_LABEL[k])}s`;
  const toggleAdvanced = (i: number) =>
    setAdvanced((cur) => {
      const nextSet = new Set(cur);
      if (nextSet.has(i)) nextSet.delete(i);
      else nextSet.add(i);
      return nextSet;
    });

  // Condition boxes, numbered in order.
  let conditionNo = 0;
  const conditions = form.consumers.flatMap((c, i) => {
    const boxes: ReactNode[] = [];
    const kindLabel = inSentence(KIND_LABEL[c.type]);
    conditionNo += 1;
    // Team and User fill the identity expression in; it is shown on request.
    const filledIn =
      (c.type === "team" || c.type === "user") && c.allocation === "separate";
    const identity = c.allocation === "separate";
    const expressionField = (
      <Field label={identity ? "Identity expression" : "Match expression"}>
        <TextInput
          size="small"
          fullWidth
          multiline
          minRows={2}
          value={c.expression}
          disabled={editing}
          onChange={(e) => updateConsumer(i, { expression: e.target.value })}
          placeholder={expressionPlaceholder(c.type)}
          error={!!errors[`consumers.${i}`]}
          helperText={
            errors[`consumers.${i}`] ??
            (identity
              ? `CEL expression used to identify individual ${pluralOf(c.type)}.`
              : "CEL expression that selects the requests this condition covers.")
          }
          maxLength={MAX_EXPRESSION_LENGTH}
          hideCharacterCount
          sx={{ "& textarea": { fontFamily: "monospace" } }}
        />
      </Field>
    );
    boxes.push(
      <ConditionBox
        key={i}
        n={conditionNo}
        label={KIND_LABEL[c.type]}
        locked={editing}
        onRemove={() =>
          set(
            "consumers",
            form.consumers.filter((_, j) => j !== i),
          )
        }
      >
        <Stack spacing={2}>
          <ChoiceField
            label="Allocation"
            value={c.allocation}
            options={allocationChoices(c.type)}
            onChange={(v) => setConsumerAllocation(i, v)}
            disabled={editing}
          />
          {isCelKind(c.type) ? (
            filledIn && !advanced.has(i) && !errors[`consumers.${i}`] ? (
              <Box>
                <Button
                  size="small"
                  variant="text"
                  onClick={() => toggleAdvanced(i)}
                  sx={{ px: 0 }}
                >
                  Advanced: identity expression
                </Button>
              </Box>
            ) : (
              expressionField
            )
          ) : (
            c.allocation === "shared" && (
              <Field label={`Select ${kindLabel}`}>
                <ValuePicker
                  kind={c.type}
                  value={c.type === "agent" ? agentValue(c) : c.id}
                  onChange={(v) =>
                    updateConsumer(
                      i,
                      c.type === "agent" ? splitAgent(v) : { id: v },
                    )
                  }
                  options={
                    c.type === "agent"
                      ? agentOptions
                      : c.type === "apiKey"
                        ? apiKeyOptions
                        : projectOptions
                  }
                  list={
                    c.type === "agent"
                      ? agentList
                      : c.type === "apiKey"
                        ? apiKeyList
                        : projectList
                  }
                  disabled={editing}
                  error={errors[`consumers.${i}`]}
                  placeholder={pickerPlaceholder(c.type)}
                />
              </Field>
            )
          )}
        </Stack>
      </ConditionBox>,
    );
    return boxes;
  });

  const fieldWidth = {};

  const details = (
    <Section title="Budget information">
      <Stack spacing={3} sx={fieldWidth}>
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
          minRows={3}
          value={form.description}
          onChange={(e) => set("description", e.target.value)}
          placeholder="e.g. Monthly OpenAI spend for the Payments team, funded by Finance"
          error={!!errors.description}
          helperText={errors.description}
          maxLength={INPUT_LIMITS.DESCRIPTION}
          hideCharacterCount
        />
      </Stack>
    </Section>
  );

  const resource = (
    <Section title="Resources" locked={editing}>
      <Stack spacing={3} sx={fieldWidth}>
        <Field label="Resource type">
          <TextInput select size="small" fullWidth value="llmProvider" disabled>
            <MenuItem value="llmProvider">LLM provider</MenuItem>
          </TextInput>
        </Field>
        <Field label="Resource selection">
          <ValuePicker
            kind="project"
            value={allProviders ? ALL : form.providerId}
            onChange={(v) =>
              v === ALL || !v
                ? patch({
                    resourceType: "all",
                    resourceAllocation: "shared",
                    providerId: "",
                  })
                : patch({
                    resourceType: "provider",
                    resourceAllocation: "shared",
                    providerId: v,
                  })
            }
            options={resourceOptions}
            list={providerList}
            disabled={editing}
            error={errors.providerId}
            placeholder="Choose an LLM provider"
          />
        </Field>
        {allProviders && (
          <ChoiceField
            label="Budget allocation"
            value={perProvider ? "separate" : "shared"}
            options={[
              {
                value: "shared",
                label: "Shared limit",
                description:
                  "All selected LLM providers share one spending limit.",
              },
              {
                value: "separate",
                label: "Per-provider limit",
                description:
                  "Each LLM provider has an independent spending limit.",
              },
            ]}
            onChange={(v) =>
              patch(
                v === "separate"
                  ? { resourceType: "provider", resourceAllocation: "separate" }
                  : { resourceType: "all", resourceAllocation: "shared" },
              )
            }
            disabled={editing}
          />
        )}
      </Stack>
    </Section>
  );

  const consumers = (
    <Stack spacing={4}>
      <Section
        title="Consumers"
        description="All consumers are included unless you add a condition."
        optional
        locked={editing}
      >
        {errors.consumers && (
          <Alert severity="error" sx={{ mb: 2 }}>
            {errors.consumers}
          </Alert>
        )}
        {conditions.length > 1 && (
          <Typography variant="body2" color="text.secondary" sx={{ mb: 1.5 }}>
            Requests must match all conditions (AND).
          </Typography>
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
          <Box sx={{ mt: conditions.length ? 2 : 0 }}>
            <AddChip
              label="Add consumer condition"
              onClick={(e) => setAddAnchor(e.currentTarget)}
            />
            <Menu
              open={!!addAnchor}
              anchorEl={addAnchor}
              onClose={() => setAddAnchor(null)}
            >
              {/* One note for the types an exemption rules out, not one per type. */}
              {CONSUMER_KINDS.some(
                (k) =>
                  !(k !== "attribute" && usedKinds.has(k)) &&
                  consumerBreaksExemptions(form, k),
              ) && (
                <Typography
                  component="li"
                  variant="caption"
                  color="text.secondary"
                  sx={{ display: "block", px: 2, py: 1, maxWidth: 280 }}
                >
                  Some types are unavailable: an exemption must stay narrower
                  than the consumers. Remove the exemption to use them.
                </Typography>
              )}
              {CONSUMER_KINDS.map((k) => (
                <MenuItem
                  key={k}
                  disabled={
                    (k !== "attribute" && usedKinds.has(k)) ||
                    consumerBreaksExemptions(form, k)
                  }
                  onClick={() => {
                    setAddAnchor(null);
                    set("consumers", [...form.consumers, newConsumer(k)]);
                  }}
                >
                  {KIND_LABEL[k]}
                </MenuItem>
              ))}
            </Menu>
          </Box>
        )}
      </Section>

      <Section
        title="Exemptions"
        description="Exclude matching traffic from this budget."
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
              label={KIND_LABEL[e.type]}
              locked={fromGateway}
              onRemove={() =>
                set(
                  "exemptions",
                  form.exemptions.filter((_, j) => j !== i),
                )
              }
            >
              <Stack spacing={2}>
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
                        newExemption(ev.target.value as ExemptionRow["type"]),
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
                  <Field label="Match expression">
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
                      helperText={
                        errors[`exemptions.${i}`] ??
                        "CEL expression that selects the requests to exclude."
                      }
                      maxLength={MAX_EXPRESSION_LENGTH}
                      hideCharacterCount
                      sx={{ "& input": { fontFamily: "monospace" } }}
                    />
                  </Field>
                ) : (
                  <Field label={`Select ${inSentence(KIND_LABEL[e.type])}`}>
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
            </Box>
          )}
        </Stack>
      </Section>
    </Stack>
  );

  const limitAndWarnings = (
    <Stack spacing={4}>
      <Section title="Spending limit" locked={fromGateway}>
        <Stack spacing={3} sx={fieldWidth}>
          <Field label="Amount" required>
            <TextInput
              size="small"
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
          </Field>
          <Field label="Reset period">
            <TextInput
              select
              size="small"
              fullWidth
              value={form.period}
              disabled={editing}
              onChange={(e) =>
                set("period", e.target.value as typeof form.period)
              }
            >
              {(["day", "week", "month"] as const).map((p) => (
                <MenuItem key={p} value={p}>
                  {PERIOD_LABELS[p]} (UTC)
                </MenuItem>
              ))}
            </TextInput>
          </Field>
          <ChoiceField
            label="Enforcement"
            value={form.action}
            disabled={fromGateway}
            onChange={(v) => set("action", v)}
            options={[
              {
                value: "block",
                label: "Block requests",
                description:
                  "Requests are refused once spending reaches the block threshold.",
              },
              {
                value: "allow",
                label: "Allow requests",
                description: "Requests continue; only warnings are sent.",
              },
            ]}
          />
          {form.action === "block" && (
            <Field label="Block threshold">
              <TextInput
                size="small"
                fullWidth
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
              />
            </Field>
          )}
          {limit > 0 && (
            <Field label="Total budget allocation">
              <Box aria-live="polite">
                <Typography variant="body1" fontWeight={600}>
                  {allocation.formula}
                </Typography>
                {(scopeText || allocation.legend) && (
                  <Typography variant="body2" color="text.secondary">
                    {[scopeText, allocation.legend].filter(Boolean).join(" ")}
                  </Typography>
                )}
              </Box>
            </Field>
          )}
        </Stack>
      </Section>

      <Section title="Warning thresholds" optional>
        <Stack spacing={2} sx={fieldWidth}>
          <Thresholds
            value={form.thresholds}
            onChange={(v) => set("thresholds", v)}
            error={errors.thresholds}
          />
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
      </Section>
    </Stack>
  );

  const pages = [details, resource, consumers, limitAndWarnings];
  const last = step === STEPS.length - 1;

  // Edit shows every section on one page; create walks through them one step at a time.
  return shell(
    <Stack spacing={editing ? 5 : 3}>
      {fromGateway && (
        <Alert severity="info">
          Created on a gateway: only the name, description and warnings can
          change here.
        </Alert>
      )}
      {editing
        ? pages.map((page, i) => <Box key={i}>{page}</Box>)
        : pages[step]}
      {errors.form && <Alert severity="error">{errors.form}</Alert>}
    </Stack>,
    editing ? (
      <Stack direction="row" gap={2}>
        <Button
          variant="contained"
          color="primary"
          onClick={save}
          disabled={saving}
        >
          Save changes
        </Button>
        <Button
          variant="outlined"
          color="primary"
          onClick={() => allowNavigation(() => navigate(backHref))}
          disabled={saving}
        >
          Cancel
        </Button>
      </Stack>
    ) : (
      <Stack direction="row" gap={2}>
        {step > 0 && (
          <Button
            variant="outlined"
            color="primary"
            startIcon={<ArrowLeft size={20} />}
            onClick={() => goTo(step - 1)}
            disabled={saving}
          >
            Previous
          </Button>
        )}
        {!last && (
          <Button
            variant="contained"
            color="primary"
            endIcon={<ArrowRight size={20} />}
            onClick={next}
            disabled={saving}
          >
            Next
          </Button>
        )}
        {last && (
          <Button
            variant="contained"
            color="primary"
            onClick={save}
            disabled={saving}
          >
            Create budget
          </Button>
        )}
      </Stack>
    ),
  );
}
