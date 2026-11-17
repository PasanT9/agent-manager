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
  budgetFromForm,
  EMPTY_FORM,
  AMOUNT_PATTERN,
  exemptionKinds,
  formFieldOf,
  formFromBudget,
  handleFor,
  newConsumer,
  validateBudgetForm,
  type BudgetFormState,
} from "./budgetForm";

const form = (over: Partial<BudgetFormState>): BudgetFormState => ({
  ...EMPTY_FORM,
  displayName: "Payments on OpenAI",
  amount: "1000",
  ...over,
});

describe("budgetForm", () => {
  it("derives a handle from the name and numbers it while taken", () => {
    expect(handleFor("Payments on OpenAI!", [])).toBe("payments-on-openai");
    expect(handleFor("Payments", ["payments", "payments-2"])).toBe(
      "payments-3",
    );
    expect(handleFor("   ", [])).toBe("budget");
    expect(handleFor("x".repeat(80), []).length).toBeLessThanOrEqual(50);
  });

  it("sends an agent with its project, and reads it back as one row", () => {
    const agent = {
      ...newConsumer("agent"),
      id: "checkout",
      project: "payments",
    };
    const budget = budgetFromForm(form({ consumers: [agent] }), {
      name: "payments",
    });
    expect(budget.spec.consumers).toEqual([
      { type: "project", allocation: "shared", id: "payments" },
      {
        type: "agent",
        allocation: "shared",
        id: "checkout",
        project: "payments",
      },
    ]);
    const back = formFromBudget(budget);
    expect(back.consumers).toHaveLength(1);
    expect(back.consumers[0]).toMatchObject({
      type: "agent",
      id: "checkout",
      project: "payments",
    });
  });

  it("owns a new budget by the project of the page, and keeps metadata on edit", () => {
    const created = budgetFromForm(form({}), {
      name: "payments",
      projectOwner: "payments",
    });
    expect(created.metadata.annotations).toEqual({
      "gateway.api-platform.wso2.com/project-id": "payments",
    });
    const edited = budgetFromForm(form({ amount: "1500" }), {
      name: "ignored",
      existing: created,
    });
    expect(edited.metadata).toBe(created.metadata);
    expect(edited.spec.limit.amount).toBe("1500.00");
  });

  it("sends block-at only for Block, and warnings only with thresholds", () => {
    const allow = budgetFromForm(form({ action: "allow", blockAt: "90" }), {
      name: "a",
    });
    expect(allow.spec.limit.blockAt).toBeUndefined();
    expect(allow.spec.warnings).toBeUndefined();
    const warned = budgetFromForm(
      form({ thresholds: [100, 80, 80], channels: ["ops-slack"] }),
      {
        name: "a",
      },
    );
    expect(warned.spec.warnings).toEqual({
      thresholds: [80, 100],
      channels: ["ops-slack"],
    });
  });

  it("sends the resource as one provider, every provider, or each provider separately", () => {
    const all = budgetFromForm(form({}), { name: "a" }).spec.resource;
    expect(all).toEqual({ type: "llmProvider", allocation: "shared" });
    const one = budgetFromForm(
      form({ resourceType: "provider", providerId: "openai" }),
      {
        name: "a",
      },
    ).spec.resource;
    expect(one).toEqual({
      type: "llmProvider",
      allocation: "shared",
      id: "openai",
    });
    const each = budgetFromForm(
      form({ resourceType: "provider", resourceAllocation: "separate" }),
      { name: "a" },
    ).spec.resource;
    expect(each).toEqual({ type: "llmProvider", allocation: "separate" });
    expect(
      formFromBudget(
        budgetFromForm(
          form({ resourceType: "provider", resourceAllocation: "separate" }),
          { name: "a" },
        ),
      ).resourceType,
    ).toBe("provider");
  });

  it("preserves an omitted all-providers resource when editing", () => {
    const existing = budgetFromForm(form({}), { name: "a" });
    delete existing.spec.resource;
    const edited = budgetFromForm(
      form({
        exemptions: [
          { type: "project", id: "internal", project: "", expression: "" },
        ],
      }),
      {
        name: "a",
        existing,
      },
    );
    expect(edited.spec.resource).toBeUndefined();
    expect(edited.spec.exemptions).toEqual([
      { type: "project", id: "internal" },
    ]);
  });

  it("preserves an explicit all-providers resource when editing", () => {
    const existing = budgetFromForm(form({}), { name: "a" });
    const edited = budgetFromForm(form({ description: "Updated" }), {
      name: "a",
      existing,
    });
    expect(edited.spec.resource).toEqual({
      type: "llmProvider",
      allocation: "shared",
    });
  });

  it("sends Team and User as attributes, and reads them back by their claim", () => {
    const team = newConsumer("team", "separate");
    expect(team.expression).toBe('request.Auth.Claims["groups"]');
    const budget = budgetFromForm(form({ consumers: [team] }), { name: "a" });
    expect(budget.spec.consumers?.[0]).toEqual({
      type: "attribute",
      allocation: "separate",
      expression: 'request.Auth.Claims["groups"]',
    });
    expect(formFromBudget(budget).consumers[0].type).toBe("team");
  });

  it("offers only exemption kinds narrower than the consumers", () => {
    expect(exemptionKinds([])).toEqual([
      "project",
      "agent",
      "team",
      "user",
      "attribute",
    ]);
    expect(exemptionKinds([newConsumer("project")])).toEqual([
      "agent",
      "team",
      "user",
      "attribute",
    ]);
    expect(exemptionKinds([newConsumer("team")])).toEqual([
      "user",
      "attribute",
    ]);
    // A per-user budget may exempt one user.
    expect(exemptionKinds([newConsumer("user", "separate")])).toEqual([
      "attribute",
      "user",
    ]);
  });

  it("accepts thresholds up to 1000% and at most 10 of them", () => {
    expect(
      validateBudgetForm(form({ thresholds: [150, 1000], channels: ["ops"] })),
    ).toEqual({});
    expect(
      validateBudgetForm(form({ thresholds: [1001], channels: ["ops"] })),
    ).toHaveProperty("thresholds");
    const eleven = Array.from({ length: 11 }, (_, i) => (i + 1) * 10);
    expect(
      validateBudgetForm(form({ thresholds: eleven, channels: ["ops"] })),
    ).toHaveProperty("thresholds");
  });

  it("takes only amounts a gateway accepts", () => {
    for (const ok of ["1", "100", "0100", "0.5", "0.05", "999999999999.99"]) {
      expect(AMOUNT_PATTERN.test(ok), ok).toBe(true);
    }
    for (const bad of [
      "0",
      "0.00",
      "1000000000000",
      "10.005",
      "-5",
      "1e3",
      "",
    ]) {
      expect(AMOUNT_PATTERN.test(bad), bad).toBe(false);
    }
  });

  it("maps the server's field names onto the form's fields", () => {
    expect(formFieldOf("spec.limit.amount")).toBe("amount");
    expect(formFieldOf("warnings.channels")).toBe("channels");
    expect(formFieldOf("warnings.thresholds[0]")).toBe("thresholds");
    expect(formFieldOf("spec.exemptions[1].expression")).toBe("exemptions.1");
    expect(formFieldOf("spec.consumers[2].id")).toBe("consumers");
    expect(formFieldOf("spec.resource.id")).toBe("providerId");
    expect(formFieldOf("metadata.annotations")).toBe("form");
    expect(formFieldOf(undefined)).toBe("form");
  });

  it("checks what the server would refuse", () => {
    expect(validateBudgetForm(form({}))).toEqual({});
    const errors = validateBudgetForm(
      form({
        displayName: " ",
        amount: "10.005",
        blockAt: "0",
        thresholds: [80],
        consumers: [newConsumer("project")],
        resourceType: "provider",
      }),
    );
    expect(Object.keys(errors).sort()).toEqual(
      [
        "amount",
        "blockAt",
        "channels",
        "consumers.0",
        "displayName",
        "providerId",
      ].sort(),
    );
  });
});
