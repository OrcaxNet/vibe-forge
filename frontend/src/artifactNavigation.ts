import type { Stage } from "./contract";

export const ARTIFACT_TARGET_BY_STAGE = {
  pm: "product-spec",
  architect: "architecture",
  engineer: "source",
  qa: "build-result",
} as const satisfies Record<Stage, string>;

export type ArtifactTarget =
  (typeof ARTIFACT_TARGET_BY_STAGE)[keyof typeof ARTIFACT_TARGET_BY_STAGE];

const ARTIFACT_TARGETS = new Set<ArtifactTarget>(
  Object.values(ARTIFACT_TARGET_BY_STAGE),
);

type NavigationIssue = {
  reason: "invalid_artifact_target";
  targetType: string;
  targetLength?: number;
};

type NavigationIssueReporter = (issue: NavigationIssue) => void;

function isArtifactTarget(value: unknown): value is ArtifactTarget {
  return (
    typeof value === "string" &&
    ARTIFACT_TARGETS.has(value as ArtifactTarget)
  );
}

export function artifactTargetForStage(
  stage: unknown,
): ArtifactTarget | undefined {
  if (
    typeof stage !== "string" ||
    !Object.prototype.hasOwnProperty.call(ARTIFACT_TARGET_BY_STAGE, stage)
  ) {
    return undefined;
  }
  return ARTIFACT_TARGET_BY_STAGE[
    stage as keyof typeof ARTIFACT_TARGET_BY_STAGE
  ];
}

function reportNavigationIssue(issue: NavigationIssue) {
  console.error("[BuildPulse] artifact link suppressed", issue);
}

/**
 * Turns a trusted, short artifact target into a fragment href. Artifact data is
 * deliberately not accepted as navigation input: invalid values are suppressed
 * and only their type/length is reported, so Markdown bodies cannot leak into
 * the address bar or logs.
 */
export function safeArtifactHref(
  target: unknown,
  report: NavigationIssueReporter = reportNavigationIssue,
): `#${ArtifactTarget}` | undefined {
  if (!isArtifactTarget(target)) {
    report({
      reason: "invalid_artifact_target",
      targetType: typeof target,
      targetLength: typeof target === "string" ? target.length : undefined,
    });
    return undefined;
  }
  return `#${target}`;
}

export function artifactTargetFromHash(
  hash: string,
): ArtifactTarget | undefined {
  const candidate = hash.startsWith("#") ? hash.slice(1) : hash;
  return isArtifactTarget(candidate) ? candidate : undefined;
}
