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

// The budget form's state, and how it maps to and from the Budget kind. Kept free of
// React so the rules (handle, consumers, validation) are tested on their own.
import {
  BUDGET_API_VERSION,
  BUDGET_PROJECT_ANNOTATION,
  type Budget,
  type BudgetAction,
  type BudgetAllocation,
  type BudgetConsumer,
  type BudgetExemption,
  type BudgetPeriod,
} from "@agent-management-platform/types";

/**
 * What a consumer or exemption is about. Team and User are custom attributes with a
 * CEL expression on a token claim, offered by name as the design does (UX 3).
 */
export type ScopeKind =
  | "project"
  | "agent"
  | "apiKey"
  | "team"
  | "user"
  | "attribute";

/** The API's consumer type for a kind. */
export function apiType(kind: ScopeKind): BudgetConsumer["type"] {
  return kind === "team" || kind === "user" ? "attribute" : kind;
}

/** Kinds that take a CEL expression. */
export const isCelKind = (kind: ScopeKind) =>
  kind === "team" || kind === "user" || kind === "attribute";

/**
 * The expression a Team or User row starts with: with separate allowances the claim
 * itself (one allowance per value); shared rows name the team or user themselves.
 */
export function defaultExpression(
  kind: ScopeKind,
  allocation: BudgetAllocation,
): string {
  if (allocation !== "separate") return "";
  if (kind === "team") return 'request.Auth.Claims["groups"]';
  if (kind === "user") return 'request.Auth.Claims["email"]';
  return "";
}

/** An example for a shared Team or User row, or a custom attribute. */
export function expressionPlaceholder(kind: ScopeKind): string {
  if (kind === "team") return `e.g. 'R&D' in request.Auth.Claims["groups"]`;
  if (kind === "user")
    return `e.g. request.Auth.Claims["email"] == 'alice@wso2.com'`;
  return `e.g. request.Headers["x-cost-center"] == 'finance'`;
}

/** The kind a stored attribute reads as, from the claim it names. */
function kindOfExpression(expression?: string): "team" | "user" | "attribute" {
  if (expression?.includes('Claims["groups"]')) return "team";
  if (expression?.includes('Claims["email"]')) return "user";
  return "attribute";
}

/** A consumer as the form edits it; an agent carries its project. */
export interface ConsumerRow {
  type: ScopeKind;
  allocation: BudgetAllocation;
  /** Project, agent or API key; empty with separate allocation means "each one". */
  id: string;
  /** Agent only. */
  project: string;
  /** Attribute only: a CEL expression. */
  expression: string;
}

export interface ExemptionRow {
  type: Exclude<ScopeKind, "apiKey">;
  id: string;
  /** Agent only. */
  project: string;
  expression: string;
}

export interface BudgetFormState {
  displayName: string;
  description: string;
  /** "all": one allowance across every LLM provider; "provider": one provider, or each. */
  resourceType: "all" | "provider";
  /** The one LLM provider, with "provider" and a shared allowance. */
  providerId: string;
  /** With "provider": shared (one provider) or separate (each provider its own). */
  resourceAllocation: BudgetAllocation;
  consumers: ConsumerRow[];
  exemptions: ExemptionRow[];
  amount: string;
  period: BudgetPeriod;
  action: BudgetAction;
  /** Block only, 1-100. */
  blockAt: string;
  thresholds: number[];
  channels: string[];
}

export const EMPTY_FORM: BudgetFormState = {
  displayName: "",
  description: "",
  resourceType: "all",
  providerId: "",
  resourceAllocation: "shared",
  consumers: [],
  exemptions: [],
  amount: "",
  period: "month",
  action: "block",
  blockAt: "100",
  thresholds: [],
  channels: [],
};

export const newConsumer = (
  type: ScopeKind,
  allocation: BudgetAllocation = "shared",
): ConsumerRow => ({
  type,
  allocation,
  id: "",
  project: "",
  expression: defaultExpression(type, allocation),
});

export const newExemption = (type: ExemptionRow["type"]): ExemptionRow => ({
  type,
  id: "",
  project: "",
  expression: "",
});

/**
 * The budget's handle from its name: lowercase letters, digits and hyphens, at most 50
 * characters; a number is added while it is taken ("payments", "payments-2", ...).
 */
export function handleFor(name: string, taken: Iterable<string>): string {
  const base =
    name
      .toLowerCase()
      .normalize("NFKD")
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-+|-+$/g, "")
      .slice(0, 50)
      .replace(/-+$/, "") || "budget";
  const used = new Set(taken);
  if (!used.has(base)) return base;
  for (let n = 2; ; n++) {
    const suffix = `-${n}`;
    const candidate = `${base.slice(0, 50 - suffix.length).replace(/-+$/, "")}${suffix}`;
    if (!used.has(candidate)) return candidate;
  }
}

/** The form's state for an existing budget. Agent consumers take their project from the
 * project consumer added with them. */
export function formFromBudget(budget: Budget): BudgetFormState {
  const { spec } = budget;
  const consumers = spec.consumers ?? [];
  const projectOf =
    consumers.find((c) => c.type === "project" && c.id)?.id ?? "";
  const hasAgent = consumers.some((c) => c.type === "agent");
  return {
    displayName: spec.displayName ?? budget.metadata.name,
    description: spec.description ?? "",
    resourceType:
      spec.resource?.id || spec.resource?.allocation === "separate"
        ? "provider"
        : "all",
    providerId: spec.resource?.id ?? "",
    resourceAllocation: spec.resource?.allocation ?? "shared",
    consumers: consumers
      // The project added with an agent is shown with the agent, not on its own.
      .filter((c) => !(hasAgent && c.type === "project" && c.id === projectOf))
      .map((c) => ({
        type: c.type === "attribute" ? kindOfExpression(c.expression) : c.type,
        allocation: c.allocation ?? "shared",
        id: c.id ?? "",
        project: c.type === "agent" ? (c.project ?? projectOf) : "",
        expression: c.expression ?? "",
      })),
    exemptions: (spec.exemptions ?? []).map((e) => ({
      type: e.type === "attribute" ? kindOfExpression(e.expression) : e.type,
      id: e.id ?? "",
      project: e.project ?? "",
      expression: e.expression ?? "",
    })),
    amount: spec.limit.amount,
    period: spec.limit.period,
    action: spec.limit.action,
    blockAt: String(spec.limit.blockAt ?? 100),
    thresholds: spec.warnings?.thresholds ?? [],
    channels: spec.warnings?.channels ?? [],
  };
}

function consumersFor(rows: ConsumerRow[]): BudgetConsumer[] {
  const out: BudgetConsumer[] = [];
  const add = (c: BudgetConsumer) => {
    if (
      !out.some(
        (o) =>
          o.type === c.type && o.id === c.id && o.expression === c.expression,
      )
    ) {
      out.push(c);
    }
  };
  for (const r of rows) {
    if (isCelKind(r.type)) {
      add({
        type: "attribute",
        allocation: r.allocation,
        expression: r.expression.trim(),
      });
      continue;
    }
    // An agent's name is unique only within its project: the agent names it, and the
    // project is counted with it (shown as the agent's locked condition).
    const named = r.type === "agent" && r.id && r.project;
    if (named) add({ type: "project", allocation: "shared", id: r.project });
    add({
      type: r.type,
      allocation: r.allocation,
      ...(r.id ? { id: r.id } : {}),
      ...(named ? { project: r.project } : {}),
    });
  }
  return out;
}

function exemptionsFor(rows: ExemptionRow[]): BudgetExemption[] {
  return rows.map((r) =>
    isCelKind(r.type)
      ? { type: "attribute", expression: r.expression.trim() }
      : r.type === "agent"
        ? { type: "agent", id: r.id, project: r.project }
        : { type: "project", id: r.id },
  );
}

/**
 * The Budget kind to send. A new budget is named `name` and, on a project's page, owned by
 * that project; an edit keeps the existing metadata and status.
 */
export function budgetFromForm(
  form: BudgetFormState,
  opts: { name: string; projectOwner?: string; existing?: Budget },
): Budget {
  const metadata = opts.existing?.metadata ?? {
    name: opts.name,
    annotations: opts.projectOwner
      ? { [BUDGET_PROJECT_ANNOTATION]: opts.projectOwner }
      : {},
  };
  const thresholds = [...new Set(form.thresholds)].sort((a, b) => a - b);
  // Agent Manager stores an omitted resource as "all LLM providers, shared". Preserve
  // that representation on update: sending the equivalent explicit object would make
  // the API's immutable-resource check treat an otherwise valid edit as a change.
  const resource =
    form.resourceType === "provider" && form.resourceAllocation === "shared"
      ? {
          type: "llmProvider" as const,
          allocation: "shared" as const,
          id: form.providerId,
        }
      : form.resourceType === "provider"
        ? { type: "llmProvider" as const, allocation: "separate" as const }
        : opts.existing && !opts.existing.spec.resource
          ? undefined
          : { type: "llmProvider" as const, allocation: "shared" as const };
  return {
    apiVersion: BUDGET_API_VERSION,
    kind: "Budget",
    metadata,
    spec: {
      displayName: form.displayName.trim(),
      ...(form.description.trim()
        ? { description: form.description.trim() }
        : {}),
      ...(opts.existing?.spec.status
        ? { status: opts.existing.spec.status }
        : {}),
      limit: {
        amount: Number(form.amount).toFixed(2),
        currency: "USD",
        period: form.period,
        action: form.action,
        ...(form.action === "block" ? { blockAt: Number(form.blockAt) } : {}),
      },
      ...(resource ? { resource } : {}),
      consumers: consumersFor(form.consumers),
      exemptions: exemptionsFor(form.exemptions),
      ...(thresholds.length
        ? { warnings: { thresholds, channels: form.channels } }
        : {}),
    },
  };
}

/**
 * Which kinds an exemption may be: narrower than the budget's consumers (a project
 * budget can exempt an agent, not another project), and any kind counted separately,
 * which may exempt one of its own values (one user of a per-user budget).
 */
export function exemptionKinds(
  consumers: ConsumerRow[],
): ExemptionRow["type"][] {
  const has = (k: ScopeKind) => consumers.some((c) => c.type === k);
  const narrower: ExemptionRow["type"][] =
    has("user") || has("attribute")
      ? ["attribute"]
      : has("team") || has("apiKey")
        ? has("team")
          ? ["user", "attribute"]
          : ["attribute"]
        : has("agent")
          ? ["team", "user", "attribute"]
          : has("project")
            ? ["agent", "team", "user", "attribute"]
            : ["project", "agent", "team", "user", "attribute"];
  const separate = consumers
    .filter((c) => c.allocation === "separate" && c.type !== "apiKey")
    .map((c) => c.type as ExemptionRow["type"]);
  return [...new Set([...narrower, ...separate])];
}

/** What a budget started from an entity's page is about. */
export interface BudgetStart {
  /** A project's own budget. */
  project?: string;
  /** An agent, as project/agent. */
  agent?: string;
  /** One LLM provider, by handle. */
  provider?: string;
}

/** The form for a budget started from an entity's page (UX 2: the entity is filled in). */
export function formFor(start: BudgetStart): BudgetFormState {
  const form: BudgetFormState = { ...EMPTY_FORM, consumers: [] };
  if (start.provider) {
    form.resourceType = "provider";
    form.resourceAllocation = "shared";
    form.providerId = start.provider;
  }
  if (start.agent) {
    const [project = "", id = ""] = start.agent.split("/");
    form.consumers = [{ ...newConsumer("agent"), id, project }];
  }
  return form;
}

/** The start in a URL's query, as "Advanced options" passes it to the full form. */
export function startFromQuery(params: URLSearchParams): BudgetStart {
  return {
    project: params.get("project") ?? undefined,
    agent: params.get("agent") ?? undefined,
    provider: params.get("provider") ?? undefined,
  };
}

export function startQuery(start: BudgetStart): string {
  const q = new URLSearchParams();
  if (start.agent) q.set("agent", start.agent);
  if (start.provider) q.set("provider", start.provider);
  const text = q.toString();
  return text ? `?${text}` : "";
}

/** The amounts a budget takes: above zero, at most 12 whole digits and 2 decimals. Leading
 * zeros are allowed here, since the amount is sent normalized ("0100" becomes "100.00"). */
export const AMOUNT_PATTERN =
  /^0*([1-9]\d{0,11}(\.\d{1,2})?|0?\.(\d[1-9]|[1-9]\d?))$/;

export type BudgetFormErrors = Partial<Record<string, string>>;

/**
 * The form field a server error names (`additionalData.field`), so its message shows
 * next to the field. Errors about the consumer or exemption lists as a whole, or a
 * field the form has no input for, come back as "consumers", "exemptions" or "form".
 */
export function formFieldOf(serverField: string | undefined): string {
  const f = (serverField ?? "").replace(/^spec\./, "");
  const exemption = /^exemptions\[(\d+)\]/.exec(f);
  if (exemption) return `exemptions.${exemption[1]}`;
  if (f.startsWith("exemptions")) return "exemptions";
  if (f.startsWith("consumers")) return "consumers";
  if (f.startsWith("resource")) return "providerId";
  if (f.startsWith("warnings.thresholds")) return "thresholds";
  if (f.startsWith("warnings.channels")) return "channels";
  if (f === "limit.amount" || f === "limit.currency") return "amount";
  if (f === "limit.blockAt") return "blockAt";
  if (f === "displayName" || f === "metadata.name") return "displayName";
  if (f === "description") return "description";
  return "form";
}

/** The message and field of a failed save, from the API client's error. */
export function serverFieldError(error: unknown): {
  field: string;
  message: string;
} {
  const body = (
    error as {
      body?: { message?: string; additionalData?: { field?: string } };
    }
  )?.body;
  const message =
    body?.message ??
    (error as { message?: string })?.message ??
    "The budget couldn't be saved.";
  return { field: formFieldOf(body?.additionalData?.field), message };
}

/** What is wrong with the form, by field; empty when it can be saved. Mirrors the
 * server's rules so most mistakes show before saving. */
export function validateBudgetForm(form: BudgetFormState): BudgetFormErrors {
  const errors: BudgetFormErrors = {};
  if (!form.displayName.trim()) errors.displayName = "Enter a name.";
  if (!AMOUNT_PATTERN.test(form.amount.trim()) || Number(form.amount) <= 0) {
    errors.amount =
      "Enter an amount above 0, with at most 12 digits before the decimal point and 2 after.";
  }
  if (form.action === "block") {
    const blockAt = Number(form.blockAt);
    if (!Number.isInteger(blockAt) || blockAt < 1 || blockAt > 100) {
      errors.blockAt = "Enter a whole percentage from 1 to 100.";
    }
  }
  if (
    form.resourceType === "provider" &&
    form.resourceAllocation === "shared" &&
    !form.providerId
  ) {
    errors.providerId = "Choose an LLM provider.";
  }
  form.consumers.forEach((c, i) => {
    if (isCelKind(c.type) && !c.expression.trim()) {
      errors[`consumers.${i}`] = "Enter the CEL expression.";
    } else if (!isCelKind(c.type) && c.allocation === "shared" && !c.id) {
      errors[`consumers.${i}`] = "Choose one, or count each one separately.";
    }
  });
  form.exemptions.forEach((e, i) => {
    if (isCelKind(e.type) ? !e.expression.trim() : !e.id) {
      errors[`exemptions.${i}`] = isCelKind(e.type)
        ? "Enter the CEL expression."
        : "Choose one.";
    }
  });
  // The server's rules: 1-1000% (above 100 warns after the limit is passed), at most 10.
  if (form.thresholds.some((t) => !Number.isInteger(t) || t < 1 || t > 1000)) {
    errors.thresholds = "Enter whole percentages between 1 and 1000.";
  } else if (form.thresholds.length > 10) {
    errors.thresholds = "Add at most 10 warning thresholds.";
  }
  if (form.thresholds.length && !form.channels.length) {
    errors.channels = "Choose where warnings go (1 to 20 channels).";
  }
  if (form.channels.length > 20)
    errors.channels = "Choose at most 20 channels.";
  return errors;
}
