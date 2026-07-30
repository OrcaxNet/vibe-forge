import { describe, expect, it } from "vitest";
import {
  CONTEXT_COMPONENTS,
  filterContextComponents,
  scanInjection,
} from "./contextCatalogData";

describe("context catalog", () => {
  it("ships at least twenty searchable components", () => {
    expect(CONTEXT_COMPONENTS.length).toBeGreaterThanOrEqual(20);
    const result = filterContextComponents(
      CONTEXT_COMPONENTS,
      "retrieval",
      "all",
    );
    expect(result.items.map((component) => component.id)).toContain("search-api");
    expect(result.elapsedMs).toBeLessThan(500);
  });

  it("filters by component type without crossing groups", () => {
    const result = filterContextComponents(
      CONTEXT_COMPONENTS,
      "",
      "prompt",
    );
    expect(result.items.length).toBeGreaterThan(0);
    expect(result.items.every((component) => component.type === "prompt")).toBe(
      true,
    );
  });

  it("includes the three canonical isolated stories", () => {
    const search = CONTEXT_COMPONENTS.find(
      (component) => component.id === "search-api",
    );
    expect(search?.stories.map((story) => story.name)).toEqual(
      expect.arrayContaining([
        "default",
        "missing-required",
        "multilingual",
      ]),
    );
    expect(
      search?.stories.every(
        (story) =>
          story.renderedContent.length > 0 &&
          Number.isInteger(story.tokenCount) &&
          story.tokenCount > 0,
      ),
    ).toBe(true);
  });

  it("surfaces injection markers without mutating the source text", () => {
    const story = CONTEXT_COMPONENTS.find(
      (component) => component.id === "search-api",
    )?.stories.find((candidate) => candidate.name === "injection-attempt");
    expect(story).toBeDefined();
    expect(scanInjection(story?.renderedContent ?? "").length).toBeGreaterThan(
      0,
    );
    expect(story?.renderedContent).toContain("<img");
  });
});
