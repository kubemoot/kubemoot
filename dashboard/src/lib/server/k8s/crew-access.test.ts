// @vitest-environment node
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  items: [] as { metadata?: { namespace?: string } }[],
  review: async (_ns: string): Promise<unknown> => ({
    status: { allowed: true },
  }),
  inFlight: 0,
  maxInFlight: 0,
}));
vi.mock("./client.js", () => ({
  getServiceAccountCustomObjectsApi: () => ({
    listClusterCustomObject: async () => ({ items: mocks.items }),
  }),
  getAuthorizationApi: () => ({
    createSelfSubjectAccessReview: async ({
      body,
    }: {
      body: { spec: { resourceAttributes: { namespace: string } } };
    }) => {
      mocks.inFlight++;
      mocks.maxInFlight = Math.max(mocks.maxInFlight, mocks.inFlight);
      try {
        return await mocks.review(body.spec.resourceAttributes.namespace);
      } finally {
        mocks.inFlight--;
      }
    },
  }),
}));

import {
  REVIEW_CONCURRENCY,
  crewNamespaces,
  mayGetCrews,
  readableNamespaces,
} from "./crew-access";

beforeEach(() => {
  mocks.items = [];
  mocks.review = async () => ({ status: { allowed: true } });
  mocks.inFlight = 0;
  mocks.maxInFlight = 0;
});

describe("crewNamespaces", () => {
  it("lists each namespace that holds a Crew once and skips items with none", async () => {
    mocks.items = [
      { metadata: { namespace: "a" } },
      { metadata: { namespace: "a" } },
      { metadata: { namespace: "b" } },
      {},
      { metadata: {} },
    ];
    expect((await crewNamespaces()).sort((a, b) => a.localeCompare(b))).toEqual(
      ["a", "b"],
    );
  });
});

describe("mayGetCrews", () => {
  it("is true only for a plain yes", async () => {
    expect(await mayGetCrews("a")).toBe(true);
    for (const answer of [
      { status: { allowed: false } },
      { status: {} },
      {},
      { status: { allowed: "true" } },
    ]) {
      mocks.review = async () => answer;
      expect(await mayGetCrews("a")).toBe(false);
    }
  });

  it("is false when the review fails", async () => {
    mocks.review = async () => {
      throw new Error("401");
    };
    expect(await mayGetCrews("a")).toBe(false);
  });
});

describe("readableNamespaces", () => {
  it("keeps the namespaces reviewed as allowed, and drops one whose review failed", async () => {
    mocks.review = async (ns) => {
      if (ns === "broken") throw new Error("boom");
      return { status: { allowed: ns !== "denied" } };
    };
    expect(await readableNamespaces(["ok", "denied", "broken"])).toEqual(
      new Set(["ok"]),
    );
  });

  it("reviews only a few at a time", async () => {
    mocks.review = async () => {
      await new Promise((r) => setTimeout(r, 1));
      return { status: { allowed: true } };
    };
    const names = Array.from(
      { length: REVIEW_CONCURRENCY * 3 },
      (_, i) => `ns-${i}`,
    );
    expect((await readableNamespaces(names)).size).toBe(names.length);
    expect(mocks.maxInFlight).toBeLessThanOrEqual(REVIEW_CONCURRENCY);
  });

  it("reviews nothing for no namespaces", async () => {
    expect(await readableNamespaces([])).toEqual(new Set());
  });
});
