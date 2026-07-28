import { describe, expect, it, vi } from "vitest";
import {
  ARTIFACT_TARGET_BY_STAGE,
  artifactTargetFromHash,
  safeArtifactHref,
} from "./artifactNavigation";

describe("Build Pulse artifact navigation", () => {
  it("maps all four stages to stable, distinct short fragments", () => {
    expect(
      Object.entries(ARTIFACT_TARGET_BY_STAGE).map(([stage, target]) => [
        stage,
        safeArtifactHref(target),
      ]),
    ).toEqual([
      ["pm", "#product-spec"],
      ["architect", "#architecture"],
      ["engineer", "#source"],
      ["qa", "#build-result"],
    ]);
    expect(new Set(Object.values(ARTIFACT_TARGET_BY_STAGE)).size).toBe(4);
  });

  it("never derives a URL from artifact Markdown or special characters", () => {
    const markdown =
      "# Product Spec: 计算器\n\n中文 空格 `code` ## architecture";
    const report = vi.fn();

    expect(safeArtifactHref(markdown, report)).toBeUndefined();
    expect(report).toHaveBeenCalledWith({
      reason: "invalid_artifact_target",
      targetType: "string",
      targetLength: markdown.length,
    });
    expect(JSON.stringify(report.mock.calls)).not.toContain("Product Spec");
  });

  it("suppresses empty, missing, and abnormally long targets", () => {
    const report = vi.fn();
    expect(safeArtifactHref("", report)).toBeUndefined();
    expect(safeArtifactHref(undefined, report)).toBeUndefined();
    expect(safeArtifactHref("x".repeat(10_000), report)).toBeUndefined();
    expect(report).toHaveBeenCalledTimes(3);
  });

  it("restores only known targets from a copied or refreshed URL", () => {
    expect(artifactTargetFromHash("#product-spec")).toBe("product-spec");
    expect(artifactTargetFromHash("#source")).toBe("source");
    expect(artifactTargetFromHash("#%20Product%20Spec")).toBeUndefined();
    expect(artifactTargetFromHash("#unknown")).toBeUndefined();
  });
});
