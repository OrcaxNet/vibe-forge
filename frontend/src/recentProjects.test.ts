import { describe, expect, it } from "vitest";
import {
  RECENT_PROJECTS_COLLAPSED_LIMIT,
  shouldShowRecentProjectsToggle,
  visibleRecentProjects,
} from "./recentProjects";

const projects = Array.from({ length: 7 }, (_, index) => ({
  id: `project-${index + 1}`,
}));

describe("recent project visibility", () => {
  it("keeps all projects visible at the six-project boundary", () => {
    const boundary = projects.slice(0, RECENT_PROJECTS_COLLAPSED_LIMIT);

    expect(visibleRecentProjects(boundary, false)).toEqual(boundary);
    expect(shouldShowRecentProjectsToggle("ready", boundary.length)).toBe(
      false,
    );
  });

  it("shows six of seven projects until expanded without changing order", () => {
    expect(visibleRecentProjects(projects, false).map(({ id }) => id)).toEqual([
      "project-1",
      "project-2",
      "project-3",
      "project-4",
      "project-5",
      "project-6",
    ]);
    expect(visibleRecentProjects(projects, true)).toEqual(projects);
    expect(shouldShowRecentProjectsToggle("ready", projects.length)).toBe(true);
  });

  it("never exposes the toggle while loading or after an error", () => {
    expect(shouldShowRecentProjectsToggle("loading", projects.length)).toBe(
      false,
    );
    expect(shouldShowRecentProjectsToggle("error", projects.length)).toBe(
      false,
    );
  });
});
